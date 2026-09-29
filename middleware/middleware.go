// Package middleware authenticates requests to a product with hub access
// tokens and puts a Principal (user, org, product tenant, scopes,
// entitlements) in the request context. It works with net/http and chi.
package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/entitlements"
)

// Principal is who is calling.
type Principal struct {
	UserID  string
	OrgID   string
	OrgRole string // owner, admin, billing, member
	// TenantID is the product's own tenant for OrgID; empty without a Tenants resolver.
	TenantID string
	Scopes   []string
	ClientID string
	// AgentID is set when an agent acts for the user.
	AgentID string
	// Actor is the act chain of an exchanged token (the MCP gateway, an agent).
	Actor  *circlexo.Actor
	Claims *circlexo.Claims
	// Entitlements is nil without an Entitlements cache or when the org has none.
	Entitlements *entitlements.Set
	// Token is the raw bearer, for calling the hub or exchanging it.
	Token string
}

// HasScope reports whether scope was granted.
func (p *Principal) HasScope(scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

type ctxKey struct{}

// FromContext returns the request's Principal, or nil.
func FromContext(ctx context.Context) *Principal { p, _ := ctx.Value(ctxKey{}).(*Principal); return p }

// NewContext returns ctx carrying p (and its entitlements).
func NewContext(ctx context.Context, p *Principal) context.Context {
	ctx = context.WithValue(ctx, ctxKey{}, p)
	if p.Entitlements != nil {
		ctx = entitlements.NewContext(ctx, p.Entitlements)
	}
	return ctx
}

// TenantResolver maps a hub org to the product tenant; *circlexo.Client
// (TenantByOrg) is wrapped by CachedTenants.
type TenantResolver interface {
	Tenant(ctx context.Context, orgID string) (string, error)
}

// Options configures Auth.
type Options struct {
	Verifier *circlexo.Verifier
	// Session, if set, supplies a token when there is no Authorization
	// header (e.g. from the product's session cookie after oidc sign-in).
	Session func(r *http.Request) string
	// Tenants resolves the product tenant; a failure answers 403.
	Tenants TenantResolver
	// Entitlements loads the org's entitlements; with none reachable the
	// request goes on with nil entitlements (paid features off).
	Entitlements *entitlements.Cache
	// Optional lets requests without a token through without a Principal.
	Optional bool
	// OnError writes the error answer; nil writes a JSON 401/403.
	OnError func(w http.ResponseWriter, r *http.Request, status int, err error)
}

// Errors passed to OnError.
var (
	ErrNoToken   = errors.New("circlexo: no access token")
	ErrNoTenant  = errors.New("circlexo: org has no tenant in this app")
	ErrForbidden = errors.New("circlexo: missing scope")
)

// Auth returns middleware that requires (or, with Optional, accepts) a hub
// access token for the app.
func Auth(o Options) func(http.Handler) http.Handler {
	fail := o.OnError
	if fail == nil {
		fail = writeError
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := bearer(r)
			if tok == "" && o.Session != nil {
				tok = o.Session(r)
			}
			if tok == "" {
				if o.Optional {
					next.ServeHTTP(w, r)
					return
				}
				w.Header().Set("WWW-Authenticate", `Bearer realm="circlexo"`)
				fail(w, r, http.StatusUnauthorized, ErrNoToken)
				return
			}
			ctx := r.Context()
			c, err := o.Verifier.VerifyAccessToken(ctx, tok)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="circlexo", error="invalid_token"`)
				fail(w, r, http.StatusUnauthorized, err)
				return
			}
			p := &Principal{UserID: c.Subject, OrgID: c.OrgID, OrgRole: c.OrgRole, Scopes: c.Scopes(), ClientID: c.ClientID,
				AgentID: c.AgentID, Actor: c.Actor, Claims: c, Token: tok}
			if p.OrgID != "" && o.Tenants != nil {
				if p.TenantID, err = o.Tenants.Tenant(ctx, p.OrgID); err != nil || p.TenantID == "" {
					fail(w, r, http.StatusForbidden, ErrNoTenant)
					return
				}
			}
			if p.OrgID != "" && o.Entitlements != nil {
				p.Entitlements, _ = o.Entitlements.Get(ctx, p.OrgID, c.EntVersion)
			}
			next.ServeHTTP(w, r.WithContext(NewContext(ctx, p)))
		})
	}
}

// RequireScope answers 403 unless the Principal was granted every scope.
func RequireScope(scopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := FromContext(r.Context())
			for _, s := range scopes {
				if p == nil || !p.HasScope(s) {
					w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+strings.Join(scopes, " ")+`"`)
					writeError(w, r, http.StatusForbidden, ErrForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireFeature answers 402 unless the org's entitlements enable feature;
// for unsafe methods it also refuses while entitlements are read-only.
func RequireFeature(feature string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s := entitlements.FromContext(r.Context())
			ok := s.Has(feature)
			if ok && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				ok = s.CanWrite(feature)
			}
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(`{"code":"feature_unavailable","feature":"` + feature + `"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func writeError(w http.ResponseWriter, _ *http.Request, status int, err error) {
	code := "unauthorized"
	switch {
	case errors.Is(err, ErrNoTenant):
		code = "no_tenant"
	case errors.Is(err, ErrForbidden):
		code = "insufficient_scope"
	case errors.Is(err, circlexo.ErrInvalidToken):
		code = "invalid_token"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":"` + code + `"}`))
}

// CachedTenants resolves tenants through the hub and caches them for TTL
// (default 10 min). Call Forget on org.app_removed.
type CachedTenants struct {
	Client *circlexo.Client
	TTL    time.Duration

	mu sync.Mutex
	m  map[string]tenantEntry
}

type tenantEntry struct {
	id string
	at time.Time
}

// Tenant returns orgID's product tenant id.
func (c *CachedTenants) Tenant(ctx context.Context, orgID string) (string, error) {
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	c.mu.Lock()
	e, ok := c.m[orgID]
	c.mu.Unlock()
	if ok && time.Since(e.at) < ttl {
		return e.id, nil
	}
	t, err := c.Client.TenantByOrg(ctx, orgID)
	if err != nil {
		return "", err
	}
	if t.ProductTenantID == "" {
		return "", ErrNoTenant
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]tenantEntry{}
	}
	c.m[orgID] = tenantEntry{t.ProductTenantID, time.Now()}
	c.mu.Unlock()
	return t.ProductTenantID, nil
}

// Forget drops orgID from the cache.
func (c *CachedTenants) Forget(orgID string) {
	c.mu.Lock()
	delete(c.m, orgID)
	c.mu.Unlock()
}
