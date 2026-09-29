package entitlements_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
	"github.com/circlexo/circlexo-go/entitlements"
)

var ctx = context.Background()

func TestCacheFailClosed(t *testing.T) {
	h := circlexotest.New(t, "demo")
	lim := int64(10)
	set := func(v int64, plan string) {
		h.Lock()
		h.Entitlements["org-1"] = circlexo.Entitlements{OrgID: "org-1", Active: true, Plan: plan, Version: v, Features: []circlexo.Entitlement{
			{Key: "invoices", Kind: "boolean", Enabled: true}, {Key: "seats", Kind: "limit", Enabled: true, Limit: &lim},
			{Key: "api_calls", Kind: "metered", Enabled: true, IncludedQty: 100, UsedInPeriod: 40}, {Key: "sso", Kind: "boolean"},
		}}
		h.Unlock()
	}
	set(1, "pro")
	now := time.Unix(1_800_000_000, 0)
	c := entitlements.New(circlexo.NewClient(h.Config(), nil))
	c.Now = func() time.Time { return now }

	s, err := c.Get(ctx, "org-1", 1)
	if err != nil || !s.Has("invoices") || s.Has("sso") || s.Has("nope") || s.ReadOnly || s.Remaining("api_calls") != 60 {
		t.Fatalf("set = %+v, %v", s, err)
	}
	if l, ok := s.Limit("seats"); !ok || l != 10 {
		t.Fatalf("limit = %d %v", l, ok)
	}

	// Cached: a hub change is not seen until the TTL, a newer ent_v or a webhook.
	set(2, "business")
	if s, _ := c.Get(ctx, "org-1", 1); s.Plan != "pro" {
		t.Fatalf("not cached: %s", s.Plan)
	}
	if s, _ := c.Get(ctx, "org-1", 2); s.Plan != "business" {
		t.Fatalf("newer ent_v not refetched: %s", s.Plan)
	}
	set(3, "enterprise")
	c.Invalidate("org-1", 3)
	if s, _ := c.Get(ctx, "org-1", 0); s.Plan != "enterprise" {
		t.Fatalf("webhook invalidation ignored: %s", s.Plan)
	}

	// The hub goes down: within the grace the last copy is served read-only.
	h.Lock()
	h.Fail = 503
	h.Unlock()
	now = now.Add(2 * time.Hour)
	s, err = c.Get(ctx, "org-1", 0)
	if err != nil || !s.ReadOnly || !s.Has("invoices") || s.CanWrite("invoices") {
		t.Fatalf("grace = %+v, %v", s, err)
	}
	// After the grace, paid features are off.
	now = now.Add(23 * time.Hour)
	if _, err := c.Get(ctx, "org-1", 0); !errors.Is(err, entitlements.ErrUnavailable) {
		t.Fatalf("after grace: %v", err)
	}
	var none *entitlements.Set
	if none.Has("invoices") {
		t.Fatal("nil set has features")
	}

	// Back up: fresh and writable again.
	h.Lock()
	h.Fail = 0
	h.Unlock()
	if s, err = c.Get(ctx, "org-1", 0); err != nil || s.ReadOnly || !s.CanWrite("invoices") {
		t.Fatalf("recovered = %+v, %v", s, err)
	}

	// An org that never installed the app is a definite no, not an outage.
	if _, err := c.Get(ctx, "org-9", 0); !circlexo.IsStatus(err, 404) {
		t.Fatalf("unknown org: %v", err)
	}

	cx := entitlements.NewContext(ctx, s)
	if !entitlements.Has(cx, "invoices") || !entitlements.CanWrite(cx, "invoices") || entitlements.Has(ctx, "invoices") {
		t.Fatal("context helpers")
	}
}
