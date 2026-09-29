// Package webhooks receives hub events: Standard Webhooks signature
// verification (webhook-id, webhook-timestamp, webhook-signature) and typed
// decoding of the event envelope.
package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Event types.
const (
	AppInstalled       = "org.app_installed"
	AppRemoved         = "org.app_removed"
	MemberAdded        = "member.added"
	MemberRemoved      = "member.removed"
	MemberRoleChanged  = "member.role_changed"
	SessionRevoked     = "session.revoked"
	EntitlementChanged = "entitlement.changed"
)

// Headers.
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
)

// DefaultTolerance bounds how far a delivery's timestamp may be from now.
const DefaultTolerance = 5 * time.Minute

var (
	ErrBadSecret    = errors.New("circlexo: webhook secret is not whsec_<base64>")
	ErrBadSignature = errors.New("circlexo: webhook signature mismatch")
	ErrStale        = errors.New("circlexo: webhook timestamp outside tolerance")
	ErrMissing      = errors.New("circlexo: webhook headers missing")
)

// Envelope is the body of a delivery.
type Envelope struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Version    int             `json:"version"`
	OccurredAt time.Time       `json:"occurred_at"`
	OrgID      string          `json:"org_id,omitempty"`
	App        *AppTenant      `json:"app,omitempty"`
	Data       json.RawMessage `json:"data"`
}

// AppTenant is the receiving app and its tenant for the org (empty while
// the app is provisioning it).
type AppTenant struct {
	ID                string `json:"id"`
	ProductTenantID   string `json:"product_tenant_id,omitempty"`
	ProductTenantSlug string `json:"product_tenant_slug,omitempty"`
}

// Person is a hub user; ProductUserID is the app's own id when linked.
type Person struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	Locale        string `json:"locale,omitempty"`
	ProductUserID string `json:"product_user_id,omitempty"`
}

// OrgData is the data of org.app_installed and org.app_removed.
type OrgData struct {
	Slug          string  `json:"slug"`
	Name          string  `json:"name"`
	DefaultLocale string  `json:"default_locale"`
	By            *Person `json:"by,omitempty"`
}

// MemberData is the data of member.* events.
type MemberData struct {
	User         Person  `json:"user"`
	Role         string  `json:"role,omitempty"`
	PreviousRole string  `json:"previous_role,omitempty"`
	By           *Person `json:"by,omitempty"`
}

// SessionData is the data of session.revoked: end the app sessions whose
// tokens carry this sid.
type SessionData struct {
	User      Person `json:"user"`
	SessionID string `json:"sid"`
}

// EntitlementData is the data of entitlement.changed.
type EntitlementData struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

// Org decodes org.* data.
func (e *Envelope) Org() (OrgData, error) { var d OrgData; return d, json.Unmarshal(e.Data, &d) }

// Member decodes member.* data.
func (e *Envelope) Member() (MemberData, error) {
	var d MemberData
	return d, json.Unmarshal(e.Data, &d)
}

// Session decodes session.revoked data.
func (e *Envelope) Session() (SessionData, error) {
	var d SessionData
	return d, json.Unmarshal(e.Data, &d)
}

// Entitlement decodes entitlement.changed data.
func (e *Envelope) Entitlement() (EntitlementData, error) {
	var d EntitlementData
	return d, json.Unmarshal(e.Data, &d)
}

func secretKey(secret string) ([]byte, error) {
	raw, ok := strings.CutPrefix(secret, "whsec_")
	if !ok {
		return nil, ErrBadSecret
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) < 16 {
		return nil, ErrBadSecret
	}
	return key, nil
}

func mac(key []byte, id string, ts int64, body []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(id + "." + strconv.FormatInt(ts, 10) + "."))
	h.Write(body)
	return h.Sum(nil)
}

// Sign returns the webhook-signature value for a delivery (for tests and
// local replays).
func Sign(secret, id string, ts time.Time, body []byte) (string, error) {
	key, err := secretKey(secret)
	if err != nil {
		return "", err
	}
	return "v1," + base64.StdEncoding.EncodeToString(mac(key, id, ts.Unix(), body)), nil
}

// Verify checks a delivery against any of secrets (more than one during a
// rotation). tolerance 0 means DefaultTolerance.
func Verify(secrets []string, h http.Header, body []byte, tolerance time.Duration, now time.Time) error {
	id, rawTS, sigs := h.Get(HeaderID), h.Get(HeaderTimestamp), h.Get(HeaderSignature)
	if id == "" || rawTS == "" || sigs == "" {
		return ErrMissing
	}
	ts, err := strconv.ParseInt(rawTS, 10, 64)
	if err != nil {
		return ErrBadSignature
	}
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return ErrStale
	}
	var anyKey bool
	for _, s := range secrets {
		key, err := secretKey(s)
		if err != nil {
			continue
		}
		anyKey = true
		want := mac(key, id, ts, body)
		for _, sig := range strings.Fields(sigs) {
			v, b64, ok := strings.Cut(sig, ",")
			if !ok || v != "v1" {
				continue
			}
			if got, err := base64.StdEncoding.DecodeString(b64); err == nil && hmac.Equal(got, want) {
				return nil
			}
		}
	}
	if !anyKey {
		return ErrBadSecret
	}
	return ErrBadSignature
}

// Handler is an http.Handler for the app's webhook URL. It verifies,
// drops redeliveries it has already handled, and calls On[type] (or
// Default). A handler error answers 500 so the hub retries.
type Handler struct {
	Secrets   []string
	Tolerance time.Duration
	On        map[string]func(context.Context, *Envelope) error
	// Default handles types without an On entry; nil acknowledges them.
	Default func(context.Context, *Envelope) error
	// Seen dedupes by webhook-id; nil uses an in-memory set of the last 10,000.
	Seen Deduper
	Now  func() time.Time
}

// Deduper remembers handled event ids. Use a shared store (Redis, the
// product's database) when the app runs more than one instance.
type Deduper interface {
	// Seen reports whether id was already handled.
	Seen(ctx context.Context, id string) (bool, error)
	// Done records id as handled.
	Done(ctx context.Context, id string) error
}

// NewHandler returns a handler verifying with secret.
func NewHandler(secret string) *Handler {
	return &Handler{Secrets: []string{secret}, On: map[string]func(context.Context, *Envelope) error{}}
}

// Handle registers fn for an event type.
func (h *Handler) Handle(eventType string, fn func(context.Context, *Envelope) error) *Handler {
	if h.On == nil {
		h.On = map[string]func(context.Context, *Envelope) error{}
	}
	h.On[eventType] = fn
	return h
}

var defaultSeen = &memSeen{ids: map[string]struct{}{}}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	if err := Verify(h.Secrets, r.Header, body, h.Tolerance, now); err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil || env.Type == "" {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	id := r.Header.Get(HeaderID)
	seen := h.Seen
	if seen == nil {
		seen = defaultSeen
	}
	ctx := r.Context()
	if dup, err := seen.Seen(ctx, id); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	} else if dup {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	fn := h.On[env.Type]
	if fn == nil {
		fn = h.Default
	}
	if fn != nil {
		if err := fn(ctx, &env); err != nil {
			http.Error(w, "handler failed", http.StatusInternalServerError)
			return
		}
	}
	_ = seen.Done(ctx, id)
	w.WriteHeader(http.StatusNoContent)
}

type memSeen struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order []string
}

func (m *memSeen) Seen(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.ids[id]
	return ok, nil
}

func (m *memSeen) Done(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.ids[id]; ok {
		return nil
	}
	m.ids[id] = struct{}{}
	m.order = append(m.order, id)
	if len(m.order) > 10000 {
		delete(m.ids, m.order[0])
		m.order = m.order[1:]
	}
	return nil
}

// NewMemoryDeduper returns an in-memory Deduper (single instance only).
func NewMemoryDeduper() Deduper { return &memSeen{ids: map[string]struct{}{}} }
