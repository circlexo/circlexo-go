// Package oidc is the relying party for "Sign in with CircleXO": the login
// redirect (authorization code with PKCE S256, pushed through PAR when the
// hub offers it), the callback, token refresh, sign-out, and a sealed
// cookie session that keeps the tokens on the browser.
package oidc

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/service"
)

// RP is a relying party. Mount Login and Callback, e.g. at /auth/circlexo/login
// and /auth/circlexo/callback.
type RP struct {
	Config circlexo.Config
	// RedirectURL is the registered callback URL.
	RedirectURL string
	// Scopes requested; default openid profile email org entitlements offline_access.
	Scopes []string
	// Key seals the login state and session cookies; 32 random bytes kept in
	// a secret store. Rotating it signs everyone out.
	Key []byte
	// UsePAR pushes the authorization request (RFC 9126) when the hub offers it.
	UsePAR bool
	// Secure marks cookies Secure; leave on except for http://localhost.
	Secure bool
	// OnLogin runs after a successful callback, e.g. to create or link the
	// product user (Login.IDClaims.Subject is the hub user id). An error
	// answers 500 and no session is saved.
	OnLogin func(w http.ResponseWriter, r *http.Request, l *Login) error
	// Session stores the tokens; nil uses a sealed cookie named "cxo_session".
	Session *CookieSession

	Verifier *circlexo.Verifier
	client   *service.Client
}

// Login is the result of a sign-in.
type Login struct {
	Tokens       Tokens
	IDClaims     *circlexo.Claims
	AccessClaims *circlexo.Claims
	// ReturnTo is the local path the user started from.
	ReturnTo string
}

// Tokens are what the session keeps.
type Tokens struct {
	AccessToken  string    `json:"at"`
	RefreshToken string    `json:"rt,omitempty"`
	IDToken      string    `json:"it,omitempty"`
	Expiry       time.Time `json:"exp"`
}

// New returns a relying party for cfg.
func New(cfg circlexo.Config, redirectURL string, key []byte) (*RP, error) {
	if len(key) != 32 {
		return nil, errors.New("circlexo: the oidc key must be 32 bytes")
	}
	c, err := service.New(cfg)
	if err != nil {
		return nil, err
	}
	rp := &RP{Config: cfg, RedirectURL: redirectURL, Key: key, UsePAR: true, Secure: !strings.HasPrefix(redirectURL, "http://"),
		Verifier: circlexo.NewVerifier(cfg), client: c}
	rp.Session = &CookieSession{Name: "cxo_session", Key: key, Secure: rp.Secure}
	return rp, nil
}

func (rp *RP) scopes() string {
	if len(rp.Scopes) == 0 {
		return "openid profile email org entitlements offline_access"
	}
	return strings.Join(rp.Scopes, " ")
}

const stateCookie = "cxo_oidc_state"

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	Exp      int64  `json:"e"`
}

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Login redirects to the hub's sign-in. Query parameters: return_to (a local
// path), prompt, login_hint.
func (rp *RP) Login(w http.ResponseWriter, r *http.Request) {
	st := loginState{State: random(24), Nonce: random(24), Verifier: random(48), ReturnTo: localPath(r.URL.Query().Get("return_to")), Exp: time.Now().Add(10 * time.Minute).Unix()}
	sealed, err := seal(rp.Key, st)
	if err != nil {
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: sealed, Path: "/", MaxAge: 600, HttpOnly: true, Secure: rp.Secure, SameSite: http.SameSiteLaxMode})
	u, err := rp.AuthURL(r.Context(), st.State, st.Nonce, st.Verifier, r.URL.Query().Get("prompt"), r.URL.Query().Get("login_hint"))
	if err != nil {
		http.Error(w, "sign-in is unavailable", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

// AuthURL builds the authorization URL for state, nonce and a PKCE verifier.
func (rp *RP) AuthURL(ctx context.Context, state, nonce, verifier, prompt, loginHint string) (string, error) {
	d, err := rp.Config.Discover(ctx)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {rp.Config.ClientID}, "redirect_uri": {rp.RedirectURL}, "scope": {rp.scopes()},
		"state": {state}, "nonce": {nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	if prompt != "" {
		q.Set("prompt", prompt)
	}
	if loginHint != "" {
		q.Set("login_hint", loginHint)
	}
	if rp.UsePAR && d.PAREndpoint != "" {
		var par struct {
			RequestURI string `json:"request_uri"`
		}
		if err := rp.client.PostForm(ctx, d.PAREndpoint, q, &par); err != nil {
			return "", err
		}
		q = url.Values{"client_id": {rp.Config.ClientID}, "request_uri": {par.RequestURI}}
	}
	return d.AuthorizationEndpoint + "?" + q.Encode(), nil
}

// Callback finishes the sign-in, saves the session and redirects to the
// page the user came from.
func (rp *RP) Callback(w http.ResponseWriter, r *http.Request) {
	l, err := rp.Exchange(w, r)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errUpstream) {
			status = http.StatusBadGateway
		}
		http.Error(w, "sign-in failed", status)
		return
	}
	if rp.OnLogin != nil {
		if err := rp.OnLogin(w, r, l); err != nil {
			http.Error(w, "sign-in failed", http.StatusInternalServerError)
			return
		}
	}
	if err := rp.Session.Save(w, l.Tokens); err != nil {
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	to := l.ReturnTo
	if to == "" {
		to = "/"
	}
	http.Redirect(w, r, to, http.StatusFound)
}

var errUpstream = errors.New("circlexo: hub token call failed")

// Exchange checks the callback request against the login state and trades
// the code for verified tokens, without saving a session.
func (rp *RP) Exchange(w http.ResponseWriter, r *http.Request) (*Login, error) {
	q := r.URL.Query()
	c, err := r.Cookie(stateCookie)
	if err != nil {
		return nil, errors.New("circlexo: no sign-in in progress")
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: rp.Secure, SameSite: http.SameSiteLaxMode})
	var st loginState
	if err := open(rp.Key, c.Value, &st); err != nil || time.Now().Unix() > st.Exp {
		return nil, errors.New("circlexo: sign-in state expired")
	}
	if subtleNe(q.Get("state"), st.State) {
		return nil, errors.New("circlexo: state mismatch")
	}
	if e := q.Get("error"); e != "" {
		return nil, fmt.Errorf("circlexo: sign-in refused: %s", e)
	}
	if iss := q.Get("iss"); iss != "" && iss != rp.Config.Issuer {
		return nil, errors.New("circlexo: issuer mismatch")
	}
	tok, err := rp.client.Post(r.Context(), url.Values{
		"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {rp.RedirectURL}, "code_verifier": {st.Verifier},
	})
	if err != nil {
		return nil, errors.Join(errUpstream, err)
	}
	idc, err := rp.Verifier.VerifyIDToken(r.Context(), tok.IDToken, rp.Config.ClientID, st.Nonce)
	if err != nil {
		return nil, err
	}
	atc, err := rp.Verifier.VerifyAccessToken(r.Context(), tok.AccessToken)
	if err != nil {
		return nil, err
	}
	if atc.Subject != idc.Subject {
		return nil, errors.New("circlexo: token subjects differ")
	}
	return &Login{Tokens: Tokens{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, IDToken: tok.IDToken, Expiry: tok.Expiry},
		IDClaims: idc, AccessClaims: atc, ReturnTo: st.ReturnTo}, nil
}

func subtleNe(a, b string) bool { return a == "" || !hmac.Equal([]byte(a), []byte(b)) }

// Refresh trades a refresh token for new tokens (the hub rotates it).
func (rp *RP) Refresh(ctx context.Context, t Tokens) (Tokens, error) {
	if t.RefreshToken == "" {
		return Tokens{}, errors.New("circlexo: no refresh token")
	}
	tok, err := rp.client.Post(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}})
	if err != nil {
		return Tokens{}, err
	}
	out := Tokens{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, IDToken: tok.IDToken, Expiry: tok.Expiry}
	if out.RefreshToken == "" {
		out.RefreshToken = t.RefreshToken
	}
	if out.IDToken == "" {
		out.IDToken = t.IDToken
	}
	return out, nil
}

// SessionToken returns the session's access token (for middleware.Options.Session).
func (rp *RP) SessionToken(r *http.Request) string {
	t, ok := rp.Session.Load(r)
	if !ok {
		return ""
	}
	return t.AccessToken
}

// Refresher is middleware that refreshes the session's tokens within a
// minute of expiry, before the auth middleware reads them. A failed refresh
// clears the session.
func (rp *RP) Refresher(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := rp.Session.Load(r)
		if ok && time.Until(t.Expiry) < time.Minute {
			nt, err := rp.Refresh(r.Context(), t)
			if err != nil {
				rp.Session.Clear(w)
				r = withoutCookie(r, rp.Session.Name)
			} else if v, err := rp.Session.Encode(nt); err == nil {
				rp.Session.write(w, v)
				r = withCookie(r, rp.Session.Name, v)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Logout clears the session and redirects to the hub's end-session endpoint,
// which returns to postLogoutURL (registered on the client).
func (rp *RP) Logout(postLogoutURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, _ := rp.Session.Load(r)
		rp.Session.Clear(w)
		d, err := rp.Config.Discover(r.Context())
		if err != nil || d.EndSessionEndpoint == "" {
			http.Redirect(w, r, postLogoutURL, http.StatusFound)
			return
		}
		q := url.Values{"client_id": {rp.Config.ClientID}, "post_logout_redirect_uri": {postLogoutURL}}
		if t.IDToken != "" {
			q.Set("id_token_hint", t.IDToken)
		}
		http.Redirect(w, r, d.EndSessionEndpoint+"?"+q.Encode(), http.StatusFound)
	}
}

// localPath keeps return_to on this site.
func localPath(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return ""
	}
	return p
}

// CookieSession keeps Tokens in one AES-GCM sealed, HttpOnly cookie.
type CookieSession struct {
	Name   string
	Key    []byte
	Secure bool
	MaxAge time.Duration // default 30 days (the refresh token's life bounds it)
}

// Save writes t.
func (s *CookieSession) Save(w http.ResponseWriter, t Tokens) error {
	v, err := s.Encode(t)
	if err != nil {
		return err
	}
	s.write(w, v)
	return nil
}

// Encode seals t into a cookie value.
func (s *CookieSession) Encode(t Tokens) (string, error) {
	v, err := seal(s.Key, t)
	if err != nil {
		return "", err
	}
	if len(v) > 4000 {
		return "", errors.New("circlexo: session too large for a cookie")
	}
	return v, nil
}

func (s *CookieSession) write(w http.ResponseWriter, v string) {
	age := s.MaxAge
	if age <= 0 {
		age = 30 * 24 * time.Hour
	}
	http.SetCookie(w, &http.Cookie{Name: s.Name, Value: v, Path: "/", MaxAge: int(age.Seconds()), HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode})
}

// Load reads the tokens.
func (s *CookieSession) Load(r *http.Request) (Tokens, bool) {
	c, err := r.Cookie(s.Name)
	if err != nil {
		return Tokens{}, false
	}
	var t Tokens
	if open(s.Key, c.Value, &t) != nil || t.AccessToken == "" {
		return Tokens{}, false
	}
	return t, true
}

// Clear removes the session.
func (s *CookieSession) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: s.Name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode})
}

func withCookie(r *http.Request, name, value string) *http.Request {
	r = withoutCookie(r, name)
	r.AddCookie(&http.Cookie{Name: name, Value: value})
	return r
}

func withoutCookie(r *http.Request, name string) *http.Request {
	r2 := r.Clone(r.Context())
	r2.Header.Del("Cookie")
	for _, c := range r.Cookies() {
		if c.Name != name {
			r2.AddCookie(c)
		}
	}
	return r2
}

func seal(key []byte, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	gcm, err := aead(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

func open(key []byte, s string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	gcm, err := aead(key)
	if err != nil {
		return err
	}
	if len(raw) < gcm.NonceSize() {
		return errors.New("short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

func aead(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}
