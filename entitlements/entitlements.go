// Package entitlements caches what each org may use in the app and applies
// CircleXO's fail-closed policy: when the hub cannot be reached, the last
// known entitlements are served read-only for a grace window (24 hours by
// default) after the last successful fetch; after that paid features are off.
// Data is never deleted because of it.
//
// A cached copy is refetched when it is older than TTL, when a token carries
// a higher ent_v, or when an entitlement.changed webhook invalidates it.
package entitlements

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/circlexo/circlexo-go"
)

// ErrUnavailable means the hub could not be reached and there is no copy
// within the grace window: treat the org as having no paid features.
var ErrUnavailable = errors.New("circlexo: entitlements unavailable")

// Fetcher is the hub call; *circlexo.Client implements it.
type Fetcher interface {
	Entitlements(ctx context.Context, orgID string) (circlexo.Entitlements, error)
}

// Set is an org's entitlements as the app should apply them.
type Set struct {
	circlexo.Entitlements
	// ReadOnly is set while serving a stale copy in the grace window: let the
	// org read and export, but refuse writes that need a paid feature.
	ReadOnly bool
	// FetchedAt is when the hub last confirmed these.
	FetchedAt time.Time
}

// Has reports whether feature is enabled and the org may use the app.
func (s *Set) Has(feature string) bool {
	if s == nil || !s.Active {
		return false
	}
	f, ok := s.Feature(feature)
	return ok && f.Enabled
}

// CanWrite reports whether feature may be used for changes: Has and not
// read-only.
func (s *Set) CanWrite(feature string) bool { return s.Has(feature) && !s.ReadOnly }

// Limit returns feature's limit; ok is false when it is unlimited or off.
func (s *Set) Limit(feature string) (limit int64, ok bool) {
	if !s.Has(feature) {
		return 0, false
	}
	f, _ := s.Feature(feature)
	if f.Limit == nil {
		return 0, false
	}
	return *f.Limit, true
}

// Remaining is how much of a metered feature is left this period before
// overage (included minus used, at least 0).
func (s *Set) Remaining(feature string) int64 {
	if !s.Has(feature) {
		return 0
	}
	f, _ := s.Feature(feature)
	if r := f.IncludedQty - f.UsedInPeriod; r > 0 {
		return r
	}
	return 0
}

// Cache holds entitlements per org.
type Cache struct {
	Fetcher Fetcher
	// TTL is how long a copy is used without asking the hub (default 5 min).
	TTL time.Duration
	// Grace is how long after the last successful fetch a stale copy is
	// served read-only when the hub is unreachable (default 24h).
	Grace time.Duration
	Now   func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	set     Set
	stale   bool // invalidated by a webhook or a newer ent_v
	minVer  int64
	fetchMu sync.Mutex
}

// New returns a cache over f with the default TTL and grace.
func New(f Fetcher) *Cache {
	return &Cache{Fetcher: f, TTL: 5 * time.Minute, Grace: 24 * time.Hour}
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cache) entry(orgID string) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*entry{}
	}
	e, ok := c.entries[orgID]
	if !ok {
		e = &entry{}
		c.entries[orgID] = e
	}
	return e
}

// Get returns orgID's entitlements. minVersion is the ent_v of the caller's
// token (0 if unknown); a cached copy older than it is refetched.
func (c *Cache) Get(ctx context.Context, orgID string, minVersion int64) (*Set, error) {
	e := c.entry(orgID)
	e.fetchMu.Lock()
	defer e.fetchMu.Unlock()
	now := c.now()
	c.mu.Lock()
	if minVersion > e.minVer {
		e.minVer = minVersion
	}
	fresh := !e.set.FetchedAt.IsZero() && !e.stale && e.set.Version >= e.minVer && now.Sub(e.set.FetchedAt) < c.ttl()
	cur := e.set
	c.mu.Unlock()
	if fresh {
		return &cur, nil
	}
	got, err := c.Fetcher.Entitlements(ctx, orgID)
	if err == nil {
		c.mu.Lock()
		e.set = Set{Entitlements: got, FetchedAt: now}
		e.stale = false
		cur = e.set
		c.mu.Unlock()
		return &cur, nil
	}
	// A definite answer that the org has nothing (not installed) is not an outage.
	if circlexo.IsStatus(err, 404) || circlexo.IsStatus(err, 403) {
		c.Invalidate(orgID, 0)
		return nil, err
	}
	if !cur.FetchedAt.IsZero() && now.Sub(cur.FetchedAt) < c.grace() {
		cur.ReadOnly = true
		return &cur, nil
	}
	return nil, errors.Join(ErrUnavailable, err)
}

// Invalidate marks orgID's copy stale, for an entitlement.changed webhook
// carrying version (0 when unknown). The next Get refetches.
func (c *Cache) Invalidate(orgID string, version int64) {
	e := c.entry(orgID)
	c.mu.Lock()
	defer c.mu.Unlock()
	e.stale = true
	if version > e.minVer {
		e.minVer = version
	}
}

func (c *Cache) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return 5 * time.Minute
}

func (c *Cache) grace() time.Duration {
	if c.Grace > 0 {
		return c.Grace
	}
	return 24 * time.Hour
}

type ctxKey struct{}

// NewContext returns ctx carrying s.
func NewContext(ctx context.Context, s *Set) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// FromContext returns the entitlements the middleware put in ctx, or nil.
func FromContext(ctx context.Context) *Set { s, _ := ctx.Value(ctxKey{}).(*Set); return s }

// Has reports whether ctx's org has feature enabled.
func Has(ctx context.Context, feature string) bool { return FromContext(ctx).Has(feature) }

// CanWrite reports whether ctx's org may use feature for changes right now.
func CanWrite(ctx context.Context, feature string) bool { return FromContext(ctx).CanWrite(feature) }

// Limit returns ctx's org's limit for feature.
func Limit(ctx context.Context, feature string) (int64, bool) { return FromContext(ctx).Limit(feature) }
