package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
	"github.com/circlexo/circlexo-go/entitlements"
	"github.com/circlexo/circlexo-go/middleware"
)

func TestAuth(t *testing.T) {
	h := circlexotest.New(t, "demo")
	cl := circlexo.NewClient(h.Config(), nil)
	h.Install("org-1")
	if _, err := cl.ConfirmTenant(t.Context(), "org-1", "t-1", "acme"); err != nil {
		t.Fatal(err)
	}
	h.Entitlements["org-1"] = circlexo.Entitlements{OrgID: "org-1", Active: true, Version: 1,
		Features: []circlexo.Entitlement{{Key: "invoices", Enabled: true}}}

	var seen *middleware.Principal
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = middleware.FromContext(r.Context())
		w.WriteHeader(200)
	})
	auth := middleware.Auth(middleware.Options{Verifier: circlexo.NewVerifier(h.Config()), Tenants: &middleware.CachedTenants{Client: cl},
		Entitlements: entitlements.New(cl)})
	mux := http.NewServeMux()
	mux.Handle("/read", auth(app))
	mux.Handle("/invoices", auth(middleware.RequireFeature("invoices")(app)))
	mux.Handle("/sso", auth(middleware.RequireFeature("sso")(app)))
	mux.Handle("/admin", auth(middleware.RequireScope("admin")(app)))

	call := func(path, tok string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	tok := h.AccessToken(nil)
	if c := call("/read", tok); c != 200 || seen.UserID != "user-1" || seen.TenantID != "t-1" || !seen.Entitlements.Has("invoices") {
		t.Fatalf("read = %d, %+v", c, seen)
	}
	for path, want := range map[string]int{"/invoices": 200, "/sso": 402, "/admin": 403} {
		if c := call(path, tok); c != want {
			t.Errorf("%s = %d, want %d", path, c, want)
		}
	}
	if c := call("/read", ""); c != 401 {
		t.Fatalf("no token = %d", c)
	}
	if c := call("/read", h.AccessToken(map[string]any{"aud": "other"})); c != 401 {
		t.Fatalf("wrong audience = %d", c)
	}
	// An org that has not installed the app has no tenant here.
	if c := call("/read", h.AccessToken(map[string]any{"org_id": "org-2"})); c != 403 {
		t.Fatalf("no tenant = %d", c)
	}

	// Optional lets anonymous requests through.
	opt := middleware.Auth(middleware.Options{Verifier: circlexo.NewVerifier(h.Config()), Optional: true})(app)
	w := httptest.NewRecorder()
	seen = &middleware.Principal{}
	opt.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != 200 || seen != nil {
		t.Fatalf("optional = %d, %+v", w.Code, seen)
	}
}
