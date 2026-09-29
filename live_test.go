package circlexo_test

import (
	"os"
	"testing"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/entitlements"
)

// TestLive runs against a real hub when CIRCLEXO_LIVE_ORG is set, with
// CIRCLEXO_ISSUER, CIRCLEXO_APP_ID and CIRCLEXO_APP_KEY. The org must have
// installed the app. It only reads.
func TestLive(t *testing.T) {
	org := os.Getenv("CIRCLEXO_LIVE_ORG")
	if org == "" {
		t.Skip("CIRCLEXO_LIVE_ORG not set")
	}
	cfg, err := circlexo.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	d, err := cfg.Discover(ctx)
	if err != nil || d.TokenEndpoint == "" {
		t.Fatalf("discovery: %+v %v", d, err)
	}
	keys, err := circlexo.FetchJWKS(ctx, cfg.HTTP(), d.JWKSURI)
	if err != nil || len(keys) == 0 {
		t.Fatalf("jwks: %d keys, %v", len(keys), err)
	}
	cl := circlexo.NewClient(cfg, nil)
	tn, err := cl.TenantByOrg(ctx, org)
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	o, err := cl.Org(ctx, org)
	if err != nil || o.ID != org || len(o.Members) == 0 {
		t.Fatalf("org: %+v %v", o, err)
	}
	s, err := entitlements.New(cl).Get(ctx, org, 0)
	if err != nil {
		t.Fatalf("entitlements: %v", err)
	}
	t.Logf("%d keys; tenant %s (%s); %d members; plan %q active %v, %d features, ent_v %d",
		len(keys), tn.ProductTenantID, tn.Status, len(o.Members), s.Plan, s.Active, len(s.Features), s.Version)
}
