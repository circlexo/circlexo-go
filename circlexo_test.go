package circlexo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
)

var ctx = context.Background()

func TestVerifyAccessToken(t *testing.T) {
	h := circlexotest.New(t, "demo")
	v := circlexo.NewVerifier(h.Config())

	c, err := v.VerifyAccessToken(ctx, h.AccessToken(map[string]any{"scope": "openid org", "ent_v": 7,
		"act": map[string]any{"sub": "cxo_gateway", "act": map[string]any{"sub": "agent:a1", "agent_id": "a1"}}}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "user-1" || c.OrgID != "org-1" || c.OrgRole != "owner" || c.EntVersion != 7 || !c.HasScope("org") || c.HasScope("admin") {
		t.Fatalf("claims = %+v", c)
	}
	if c.Actor == nil || c.Actor.Subject != "cxo_gateway" || c.Actor.Actor.AgentID != "a1" {
		t.Fatalf("act = %+v", c.Actor)
	}

	bad := map[string]string{
		"other audience": h.AccessToken(map[string]any{"aud": "other"}),
		"expired":        h.AccessToken(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}),
		"no exp":         h.AccessToken(map[string]any{"exp": nil}),
		"other issuer":   h.AccessToken(map[string]any{"iss": "https://evil.example"}),
		"id token typ":   h.Sign(map[string]any{"iss": h.URL, "aud": "demo", "sub": "u", "exp": time.Now().Add(time.Minute).Unix()}, ""),
		"garbage":        "not.a.jwt",
	}
	for name, tok := range bad {
		if _, err := v.VerifyAccessToken(ctx, tok); !errors.Is(err, circlexo.ErrInvalidToken) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A token signed by a key the hub never published fails.
	other := circlexotest.New(t, "demo")
	tok := other.AccessToken(map[string]any{"iss": h.URL})
	if _, err := v.VerifyAccessToken(ctx, tok); !errors.Is(err, circlexo.ErrInvalidToken) {
		t.Fatalf("foreign key: %v", err)
	}
}

func TestClient(t *testing.T) {
	h := circlexotest.New(t, "demo")
	cl := circlexo.NewClient(h.Config(), nil)
	lim := int64(3)
	h.Entitlements["org-1"] = circlexo.Entitlements{OrgID: "org-1", Active: true, Plan: "pro", Version: 2,
		Features: []circlexo.Entitlement{{Key: "seats", Kind: "limit", Enabled: true, Limit: &lim}}}

	e, err := cl.Entitlements(ctx, "org-1")
	if f, ok := e.Feature("seats"); err != nil || !ok || *f.Limit != 3 || e.Version != 2 {
		t.Fatalf("entitlements = %+v, %v", e, err)
	}
	if _, err := cl.Entitlements(ctx, "org-2"); !circlexo.IsStatus(err, 404) {
		t.Fatalf("missing org: %v", err)
	}

	r, err := cl.ReportUsage(ctx, circlexo.UsageReport{OrgID: "org-1", Feature: "api_calls", Qty: 2, IdempotencyKey: "k1"})
	if err != nil || r.Qty != 2 || r.Replayed {
		t.Fatalf("usage = %+v, %v", r, err)
	}
	if r, _ := cl.ReportUsage(ctx, circlexo.UsageReport{OrgID: "org-1", Feature: "api_calls", Qty: 2, IdempotencyKey: "k1"}); !r.Replayed {
		t.Fatal("replay not reported")
	}

	if _, err := cl.ConfirmTenant(ctx, "org-1", "t-1", "acme"); !circlexo.IsStatus(err, 404) {
		t.Fatalf("not installed: %v", err)
	}
	h.Install("org-1")
	h.Members["org-1"] = []circlexo.Member{{UserID: "user-1", Role: "owner", Email: "owner@example.com"}}
	tn, err := cl.ConfirmTenant(ctx, "org-1", "t-1", "acme")
	if err != nil || tn.Status != "active" || tn.ProductTenantID != "t-1" {
		t.Fatalf("confirm = %+v, %v", tn, err)
	}
	if tn, err := cl.TenantByProductID(ctx, "t-1"); err != nil || tn.OrgID != "org-1" {
		t.Fatalf("by product id = %+v, %v", tn, err)
	}
	if tn, err := cl.TenantByOrg(ctx, "org-1"); err != nil || tn.ProductTenantID != "t-1" {
		t.Fatalf("by org = %+v, %v", tn, err)
	}
	o, err := cl.Org(ctx, "org-1")
	if err != nil || len(o.Members) != 1 || o.Members[0].Role != "owner" || o.Tenant.ProductTenantID != "t-1" {
		t.Fatalf("org = %+v, %v", o, err)
	}

	// A wrong key is refused.
	bad := circlexo.NewClient(h.Config(), circlexo.StaticToken("cxa_wrong"))
	var ae *circlexo.APIError
	if _, err := bad.Org(ctx, "org-1"); !errors.As(err, &ae) || ae.Status != 401 || ae.Code != "invalid_app_key" {
		t.Fatalf("bad key: %v", err)
	}
}

func TestConfig(t *testing.T) {
	t.Setenv("CIRCLEXO_ISSUER", "https://accounts.example/")
	t.Setenv("CIRCLEXO_APP_ID", "demo")
	t.Setenv("CIRCLEXO_API_URL", "")
	c, err := circlexo.FromEnv()
	if err != nil || c.Issuer != "https://accounts.example" || c.ClientID != "cxo_app_demo" || c.APIURL != c.Issuer {
		t.Fatalf("config = %+v, %v", c, err)
	}
	t.Setenv("CIRCLEXO_APP_ID", "")
	if _, err := circlexo.FromEnv(); err == nil {
		t.Fatal("missing app id accepted")
	}
}
