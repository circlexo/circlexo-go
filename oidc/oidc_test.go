package oidc_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
	"github.com/circlexo/circlexo-go/middleware"
	"github.com/circlexo/circlexo-go/oidc"
)

func TestSignIn(t *testing.T) {
	for _, par := range []bool{true, false} {
		h := circlexotest.New(t, "demo")
		mux := http.NewServeMux()
		app := httptest.NewServer(mux)
		t.Cleanup(app.Close)

		rp, err := oidc.New(h.Config(), app.URL+"/auth/circlexo/callback", []byte("0123456789abcdef0123456789abcdef"))
		if err != nil {
			t.Fatal(err)
		}
		rp.UsePAR = par
		var linked string
		rp.OnLogin = func(_ http.ResponseWriter, _ *http.Request, l *oidc.Login) error {
			linked = l.IDClaims.Subject + "/" + l.IDClaims.Raw["email"].(string) + "/" + l.AccessClaims.OrgID
			return nil
		}
		mux.HandleFunc("/auth/circlexo/login", rp.Login)
		mux.HandleFunc("/auth/circlexo/callback", rp.Callback)
		mux.Handle("/auth/circlexo/logout", rp.Logout(app.URL+"/bye"))
		mux.Handle("/me", rp.Refresher(middleware.Auth(middleware.Options{Verifier: circlexo.NewVerifier(h.Config()), Session: rp.SessionToken})(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(middleware.FromContext(r.Context()).UserID))
			}))))
		mux.HandleFunc("/bye", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("bye")) })

		jar, _ := cookiejar.New(nil)
		var hops []string
		c := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			hops = append(hops, req.URL.Path)
			return nil
		}}
		// return_to must be local; an absolute URL is dropped.
		res, err := c.Get(app.URL + "/auth/circlexo/login?return_to=/me")
		if err != nil {
			t.Fatal(err)
		}
		body := read(res)
		if res.StatusCode != 200 || body != "user-1" || linked != "user-1/owner@example.com/org-1" {
			t.Fatalf("par=%v: %d %q linked %q hops %v", par, res.StatusCode, body, linked, hops)
		}
		if res, _ := c.Get(app.URL + "/auth/circlexo/login?return_to=//evil.example"); read(res) == "" || res.Request.URL.Host != strings.TrimPrefix(app.URL, "http://") {
			t.Fatalf("open redirect to %s", res.Request.URL)
		}

		nt, err := rp.Refresh(t.Context(), oidc.Tokens{RefreshToken: "rt_old", IDToken: "keep"})
		if err != nil || nt.AccessToken == "" || nt.RefreshToken == "rt_old" || nt.IDToken != "keep" || nt.Expiry.IsZero() {
			t.Fatalf("refresh = %+v, %v", nt, err)
		}
		if _, err := rp.Refresh(t.Context(), oidc.Tokens{RefreshToken: "bogus"}); err == nil {
			t.Fatal("bogus refresh accepted")
		}

		// A replayed callback (state cookie gone) fails.
		cb := app.URL + "/auth/circlexo/callback?code=x&state=y"
		if res, _ := c.Get(cb); res.StatusCode != 400 {
			t.Fatalf("replayed callback = %d", res.StatusCode)
		}

		// Sign-out clears the session and goes through the hub's end-session.
		c.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
			if strings.HasPrefix(req.URL.String(), h.URL+"/oauth2/logout") {
				if req.URL.Query().Get("id_token_hint") == "" {
					t.Errorf("no id_token_hint")
				}
				return http.ErrUseLastResponse
			}
			return nil
		}
		res, _ = c.Get(app.URL + "/auth/circlexo/logout")
		if res.StatusCode != http.StatusFound {
			t.Fatalf("logout = %d", res.StatusCode)
		}
		u, _ := url.Parse(app.URL)
		for _, ck := range jar.Cookies(u) {
			if ck.Name == "cxo_session" {
				t.Fatal("session survived logout")
			}
		}
		if res, _ := c.Get(app.URL + "/me"); res.StatusCode != 401 {
			t.Fatalf("after logout = %d", res.StatusCode)
		}
	}
}

func read(res *http.Response) string {
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}
