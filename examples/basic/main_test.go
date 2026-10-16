package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
)

func setupApp(t *testing.T, enabled bool) (*circlexotest.Hub, *httptest.Server) {
	t.Helper()
	hub := circlexotest.New(t, "basic-test")
	hub.Install("org-1")
	client := circlexo.NewClient(hub.Config(), nil)
	if _, err := client.ConfirmTenant(context.Background(), "org-1", "tenant-1", "demo"); err != nil {
		t.Fatal(err)
	}
	hub.Entitlements["org-1"] = circlexo.Entitlements{OrgID: "org-1", Active: true, Version: 1,
		Features: []circlexo.Entitlement{{Key: "api_calls", Enabled: enabled}}}
	proxy := http.NewServeMux()
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	handler, closeReporter, err := newApp(hub.Config(), server.URL+"/auth/callback", key, "api_calls")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeReporter(context.Background()); err != nil {
			t.Error(err)
		}
	})
	proxy.Handle("/", handler)
	return hub, server
}

func TestSignInAndCookieWriteProtection(t *testing.T) {
	_, app := setupApp(t, true)
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	res, err := browser.Get(app.URL + "/auth/login?return_to=/profile")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(body), `"user_id":"user-1"`) {
		t.Fatalf("sign-in: %d %s", res.StatusCode, body)
	}
	req, _ := http.NewRequest(http.MethodPost, app.URL+"/use", nil)
	req.Header.Set("Idempotency-Key", "cookie-test")
	res, err = browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin write = %d", res.StatusCode)
	}
	req.Header.Set("Origin", app.URL)
	res, err = browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("same-origin write = %d", res.StatusCode)
	}
}

func TestEntitlementsAndIdempotentUsage(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			hub, app := setupApp(t, enabled)
			anonymous, err := http.Get(app.URL + "/profile")
			if err != nil {
				t.Fatal(err)
			}
			anonymous.Body.Close()
			if anonymous.StatusCode != http.StatusUnauthorized {
				t.Fatal(anonymous.StatusCode)
			}
			for i := 0; i < 2; i++ {
				req, _ := http.NewRequest(http.MethodPost, app.URL+"/use", nil)
				req.Header.Set("Authorization", "Bearer "+hub.AccessToken(nil))
				req.Header.Set("Idempotency-Key", "same-work")
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				if !enabled {
					res.Body.Close()
					if res.StatusCode != http.StatusPaymentRequired {
						t.Fatal(res.StatusCode)
					}
					continue
				}
				var result circlexo.UsageResult
				err = json.NewDecoder(res.Body).Decode(&result)
				res.Body.Close()
				if err != nil || res.StatusCode != 200 || result.Replayed != (i == 1) {
					t.Fatalf("usage: %d %+v %v", res.StatusCode, result, err)
				}
			}
			hub.Lock()
			defer hub.Unlock()
			want := 0
			if enabled {
				want = 1
			}
			if len(hub.Usage) != want {
				t.Fatalf("usage reports = %d", len(hub.Usage))
			}
		})
	}
}
