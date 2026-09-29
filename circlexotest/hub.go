// Package circlexotest is an in-memory CircleXO hub for tests: discovery,
// JWKS, the token endpoint (authorization_code with PKCE, PAR,
// client_credentials, token exchange), entitlements, usage, tenants and the
// org directory. Products use it to test their integration without a hub.
package circlexotest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/circlexo/circlexo-go"
)

// Hub is a fake hub. Set the exported maps before (or during) a test; guard
// changes during requests with Lock/Unlock.
type Hub struct {
	*httptest.Server
	sync.Mutex

	AppID        string
	ClientID     string
	ClientSecret string
	AppKey       string
	// Entitlements by org id; a missing org answers 404.
	Entitlements map[string]circlexo.Entitlements
	// Usage records every accepted report by idempotency key.
	Usage map[string]circlexo.UsageReport
	// Tenants by org id.
	Tenants map[string]circlexo.Tenant
	// Members by org id.
	Members map[string][]circlexo.Member
	// Fail makes every app API call answer this status (to test fail-closed).
	Fail int
	// Users by authorization code subject: the next code grants this user.
	User User

	key   ed25519.PrivateKey
	codes map[string]codeGrant
	pars  map[string]string
	now   func() time.Time
}

// User is who the fake hub signs in.
type User struct {
	ID, Email, Name, OrgID, OrgRole string
}

type codeGrant struct {
	challenge, clientID, redirect, nonce, scope string
	user                                        User
}

// New starts a hub for appID. It closes when the test ends.
func New(t testing.TB, appID string) *Hub {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := &Hub{
		AppID: appID, ClientID: "cxo_app_" + appID, ClientSecret: "cxs_test_secret", AppKey: "cxa_test_key",
		Entitlements: map[string]circlexo.Entitlements{}, Usage: map[string]circlexo.UsageReport{},
		Tenants: map[string]circlexo.Tenant{}, Members: map[string][]circlexo.Member{},
		User: User{ID: "user-1", Email: "owner@example.com", Name: "Owner", OrgID: "org-1", OrgRole: "owner"},
		key:  key, codes: map[string]codeGrant{}, pars: map[string]string{}, now: time.Now,
	}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.Close)
	return h
}

// Config is a client configuration pointing at the hub.
func (h *Hub) Config() circlexo.Config {
	c := circlexo.Config{Issuer: h.URL, AppID: h.AppID, ClientSecret: h.ClientSecret, AppKey: h.AppKey, HTTPClient: h.Client()}
	_ = c.Validate()
	return c
}

// AccessToken signs an access token for the app with the given claims on
// top of the defaults (sub, org, scope, 10 minute expiry).
func (h *Hub) AccessToken(extra map[string]any) string {
	now := h.now()
	c := jwt.MapClaims{
		"iss": h.URL, "aud": h.AppID, "sub": h.User.ID, "client_id": h.ClientID, "scope": "openid org",
		"org_id": h.User.OrgID, "org_role": h.User.OrgRole, "ent_v": 1, "sid": "sid-1", "jti": randID(),
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(), "auth_time": now.Unix(),
	}
	for k, v := range extra {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return h.sign(c, "at+jwt")
}

// Sign signs arbitrary claims with the hub key (for negative tests).
func (h *Hub) Sign(c map[string]any, typ string) string { return h.sign(jwt.MapClaims(c), typ) }

func (h *Hub) sign(c jwt.MapClaims, typ string) string {
	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	t.Header["kid"] = "k1"
	if typ != "" {
		t.Header["typ"] = typ
	}
	s, err := t.SignedString(h.key)
	if err != nil {
		panic(err)
	}
	return s
}

func randID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"code": code, "message": code})
}

func oauthErr(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

func (h *Hub) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/.well-known/openid-configuration":
		writeJSON(w, 200, map[string]any{
			"issuer": h.URL, "authorization_endpoint": h.URL + "/oauth2/authorize", "token_endpoint": h.URL + "/oauth2/token",
			"userinfo_endpoint": h.URL + "/oauth2/userinfo", "jwks_uri": h.URL + "/.well-known/jwks.json",
			"revocation_endpoint": h.URL + "/oauth2/revoke", "end_session_endpoint": h.URL + "/oauth2/logout",
			"pushed_authorization_request_endpoint": h.URL + "/oauth2/par",
		})
	case r.URL.Path == "/.well-known/jwks.json":
		k, _ := circlexo.JWKOf("k1", h.key.Public())
		writeJSON(w, 200, map[string]any{"keys": []any{k}})
	case r.URL.Path == "/oauth2/authorize":
		h.authorize(w, r)
	case r.URL.Path == "/oauth2/par":
		h.par(w, r)
	case r.URL.Path == "/oauth2/token":
		h.token(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		h.api(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Hub) clientOK(r *http.Request) bool {
	id, sec, ok := r.BasicAuth()
	if !ok {
		id, sec = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	if at := r.PostFormValue("client_assertion"); at != "" {
		// private_key_jwt: the fake accepts any well-formed assertion whose
		// iss and sub are the client id (real key checks happen in the hub).
		p := jwt.NewParser()
		c := jwt.MapClaims{}
		if _, _, err := p.ParseUnverified(at, c); err != nil {
			return false
		}
		return c["iss"] == h.ClientID && c["sub"] == h.ClientID && r.PostFormValue("client_assertion_type") == "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	}
	return id == h.ClientID && sec == h.ClientSecret
}

func (h *Hub) par(w http.ResponseWriter, r *http.Request) {
	if !h.clientOK(r) {
		oauthErr(w, "invalid_client")
		return
	}
	_ = r.ParseForm()
	uri := "urn:ietf:params:oauth:request_uri:" + randID()
	h.Lock()
	h.pars[uri] = r.PostForm.Encode()
	h.Unlock()
	writeJSON(w, 201, map[string]any{"request_uri": uri, "expires_in": 60})
}

// authorize signs in h.User at once and redirects back with a code.
func (h *Hub) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if uri := q.Get("request_uri"); uri != "" {
		h.Lock()
		enc, ok := h.pars[uri]
		delete(h.pars, uri)
		h.Unlock()
		if !ok {
			http.Error(w, "bad request_uri", 400)
			return
		}
		q, _ = url.ParseQuery(enc)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("client_id") != h.ClientID {
		http.Error(w, "pkce required", 400)
		return
	}
	code := randID()
	h.Lock()
	h.codes[code] = codeGrant{q.Get("code_challenge"), q.Get("client_id"), q.Get("redirect_uri"), q.Get("nonce"), q.Get("scope"), h.User}
	h.Unlock()
	http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+q.Get("state")+"&iss="+h.URL, http.StatusFound)
}

func (h *Hub) token(w http.ResponseWriter, r *http.Request) {
	if !h.clientOK(r) {
		writeJSON(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	now := h.now()
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		h.Lock()
		g, ok := h.codes[r.PostFormValue("code")]
		delete(h.codes, r.PostFormValue("code"))
		h.Unlock()
		sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge || r.PostFormValue("redirect_uri") != g.redirect {
			oauthErr(w, "invalid_grant")
			return
		}
		at := h.AccessToken(map[string]any{"sub": g.user.ID, "org_id": g.user.OrgID, "org_role": g.user.OrgRole, "scope": g.scope})
		id := h.sign(jwt.MapClaims{"iss": h.URL, "aud": h.ClientID, "sub": g.user.ID, "nonce": g.nonce, "email": g.user.Email,
			"name": g.user.Name, "sid": "sid-1", "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(), "org_id": g.user.OrgID}, "")
		writeJSON(w, 200, map[string]any{"access_token": at, "id_token": id, "refresh_token": "rt_" + randID(), "token_type": "Bearer", "expires_in": 600, "scope": g.scope})
	case "refresh_token":
		if !strings.HasPrefix(r.PostFormValue("refresh_token"), "rt_") {
			oauthErr(w, "invalid_grant")
			return
		}
		writeJSON(w, 200, map[string]any{"access_token": h.AccessToken(nil), "refresh_token": "rt_" + randID(), "token_type": "Bearer", "expires_in": 600})
	case "client_credentials":
		scope := r.PostFormValue("scope")
		if scope == "" {
			scope = "billing.entitlements billing.usage orgs.read tenants.read tenants.write"
		}
		tok := h.sign(jwt.MapClaims{"iss": h.URL, "aud": h.URL, "sub": h.ClientID, "client_id": h.ClientID, "app_id": h.AppID,
			"scope": scope, "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(), "jti": randID()}, "at+jwt")
		writeJSON(w, 200, map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 600, "scope": scope})
	case "urn:ietf:params:oauth:grant-type:token-exchange":
		sub := r.PostFormValue("subject_token")
		aud := r.PostFormValue("audience")
		if sub == "" || aud == "" {
			oauthErr(w, "invalid_request")
			return
		}
		tok := h.AccessToken(map[string]any{"aud": aud, "act": map[string]any{"sub": h.ClientID, "client_id": h.ClientID}})
		writeJSON(w, 200, map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 600,
			"issued_token_type": "urn:ietf:params:oauth:token-type:access_token"})
	default:
		oauthErr(w, "unsupported_grant_type")
	}
}

// appAuthed accepts the app key or a service token the hub signed.
func (h *Hub) appAuthed(r *http.Request) bool {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == h.AppKey {
		return true
	}
	c := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(tok, c, func(*jwt.Token) (any, error) { return h.key.Public(), nil }, jwt.WithIssuer(h.URL))
	return err == nil && c["app_id"] == h.AppID
}

func (h *Hub) api(w http.ResponseWriter, r *http.Request) {
	if !h.appAuthed(r) {
		apiErr(w, 401, "invalid_app_key")
		return
	}
	h.Lock()
	defer h.Unlock()
	if h.Fail != 0 {
		apiErr(w, h.Fail, "unavailable")
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/billing/entitlements":
		e, ok := h.Entitlements[r.URL.Query().Get("org_id")]
		if !ok {
			apiErr(w, 404, "not_found")
			return
		}
		writeJSON(w, 200, e)
	case r.Method == "POST" && r.URL.Path == "/api/billing/usage":
		var u circlexo.UsageReport
		if json.NewDecoder(r.Body).Decode(&u) != nil || u.IdempotencyKey == "" || u.Qty <= 0 {
			apiErr(w, 422, "invalid_usage")
			return
		}
		_, replay := h.Usage[u.IdempotencyKey]
		h.Usage[u.IdempotencyKey] = u
		writeJSON(w, 200, circlexo.UsageResult{Qty: u.Qty, BillableQty: u.Qty, Replayed: replay})
	case r.Method == "POST" && r.URL.Path == "/api/apps/tenants":
		var in struct{ OrgID, ProductTenantID, Slug string }
		var raw map[string]string
		_ = json.NewDecoder(r.Body).Decode(&raw)
		in.OrgID, in.ProductTenantID, in.Slug = raw["org_id"], raw["product_tenant_id"], raw["slug"]
		t, ok := h.Tenants[in.OrgID]
		if !ok {
			apiErr(w, 404, "not_installed")
			return
		}
		t.ProductTenantID, t.ProductTenantSlug, t.Status = in.ProductTenantID, in.Slug, "active"
		h.Tenants[in.OrgID] = t
		writeJSON(w, 200, t)
	case r.Method == "GET" && r.URL.Path == "/api/apps/tenants":
		q := r.URL.Query()
		for _, t := range h.Tenants {
			if (q.Get("org_id") != "" && t.OrgID == q.Get("org_id")) || (q.Get("product_tenant_id") != "" && t.ProductTenantID == q.Get("product_tenant_id")) {
				writeJSON(w, 200, t)
				return
			}
		}
		apiErr(w, 404, "not_installed")
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/apps/orgs/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/apps/orgs/")
		t, ok := h.Tenants[id]
		if !ok {
			apiErr(w, 404, "not_installed")
			return
		}
		writeJSON(w, 200, circlexo.Org{ID: id, Slug: "org", Name: "Org", DefaultLocale: "en", Status: "active", Tenant: t, Members: h.Members[id]})
	default:
		apiErr(w, 404, "not_found")
	}
}

// Install marks orgID as having installed the app (status provisioning).
func (h *Hub) Install(orgID string) {
	h.Lock()
	defer h.Unlock()
	h.Tenants[orgID] = circlexo.Tenant{OrgID: orgID, AppID: h.AppID, Status: "provisioning"}
}

// String describes the hub.
func (h *Hub) String() string { return fmt.Sprintf("circlexotest.Hub(%s)", h.URL) }
