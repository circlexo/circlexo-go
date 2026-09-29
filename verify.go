package circlexo

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken is returned for any token that does not verify. It never
// says why beyond the wrapped reason, which is safe to log.
var ErrInvalidToken = errors.New("circlexo: invalid token")

// Claims are the claims of a hub access token.
type Claims struct {
	Issuer   string   `json:"iss"`
	Subject  string   `json:"sub"` // user id; for a service token, the client id
	Audience []string `json:"-"`
	ClientID string   `json:"client_id"`
	Scope    string   `json:"scope"`
	OrgID    string   `json:"org_id"`
	OrgRole  string   `json:"org_role"`
	// EntVersion is ent_v: the org's entitlement version when the token was
	// issued. Cached entitlements older than it are stale.
	EntVersion int64     `json:"ent_v"`
	AuthTime   time.Time `json:"-"`
	SessionID  string    `json:"sid"`
	ID         string    `json:"jti"`
	AppID      string    `json:"app_id"` // service tokens
	AgentID    string    `json:"agent_id"`
	// Actor is the RFC 8693 act chain of an exchanged token: who acts on the
	// subject's behalf (the MCP gateway, an agent).
	Actor     *Actor         `json:"act"`
	ExpiresAt time.Time      `json:"-"`
	IssuedAt  time.Time      `json:"-"`
	Raw       map[string]any `json:"-"`
}

// Actor is one link of an act chain.
type Actor struct {
	Subject  string `json:"sub"`
	ClientID string `json:"client_id,omitempty"`
	AgentID  string `json:"agent_id,omitempty"`
	Actor    *Actor `json:"act,omitempty"`
}

// Scopes returns the granted scopes.
func (c *Claims) Scopes() []string { return strings.Fields(c.Scope) }

// HasScope reports whether scope was granted.
func (c *Claims) HasScope(scope string) bool {
	for _, s := range c.Scopes() {
		if s == scope {
			return true
		}
	}
	return false
}

// IsService reports whether this is a client_credentials service token.
func (c *Claims) IsService() bool { return c.Subject != "" && c.Subject == c.ClientID }

// Verifier checks hub-signed JWTs against the issuer's JWKS, caching the keys
// and refetching them (at most once a minute) when it sees an unknown kid.
type Verifier struct {
	Issuer string
	// Audience is the aud access tokens must carry: the app id.
	Audience string
	HTTP     *http.Client
	Now      func() time.Time
	// Leeway tolerates clock skew on exp/iat/nbf.
	Leeway time.Duration

	mu      sync.Mutex
	keys    map[string]crypto.PublicKey
	fetched time.Time
}

// NewVerifier verifies access tokens issued for cfg's app.
func NewVerifier(cfg Config) *Verifier {
	return &Verifier{Issuer: cfg.Issuer, Audience: cfg.AppID, HTTP: cfg.HTTP(), Leeway: 30 * time.Second}
}

// VerifyAccessToken checks an access token (typ at+jwt) for the app and
// returns its claims.
func (v *Verifier) VerifyAccessToken(ctx context.Context, raw string) (*Claims, error) {
	return v.verify(ctx, raw, v.Audience, "at+jwt")
}

// VerifyIDToken checks an ID token issued to clientID; nonce, when not
// empty, must match.
func (v *Verifier) VerifyIDToken(ctx context.Context, raw, clientID, nonce string) (*Claims, error) {
	c, err := v.verify(ctx, raw, clientID, "")
	if err != nil {
		return nil, err
	}
	if nonce != "" {
		if got, _ := c.Raw["nonce"].(string); got != nonce {
			return nil, fmt.Errorf("%w: nonce mismatch", ErrInvalidToken)
		}
	}
	return c, nil
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *Verifier) verify(ctx context.Context, raw, aud, typ string) (*Claims, error) {
	if raw == "" {
		return nil, fmt.Errorf("%w: empty", ErrInvalidToken)
	}
	mc := jwt.MapClaims{}
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"RS256", "EdDSA", "ES256"}), jwt.WithIssuer(v.Issuer), jwt.WithAudience(aud),
		jwt.WithExpirationRequired(), jwt.WithLeeway(v.Leeway), jwt.WithTimeFunc(v.now),
	}
	tok, err := jwt.ParseWithClaims(raw, mc, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return v.key(ctx, kid)
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if typ != "" {
		if got, _ := tok.Header["typ"].(string); !strings.EqualFold(got, typ) {
			return nil, fmt.Errorf("%w: typ %q", ErrInvalidToken, got)
		}
	}
	return claimsFrom(mc)
}

func claimsFrom(mc jwt.MapClaims) (*Claims, error) {
	c := &Claims{Raw: mc}
	str := func(k string) string { s, _ := mc[k].(string); return s }
	c.Issuer, c.Subject, c.ClientID, c.Scope = str("iss"), str("sub"), str("client_id"), str("scope")
	c.OrgID, c.OrgRole, c.SessionID, c.ID = str("org_id"), str("org_role"), str("sid"), str("jti")
	c.AppID, c.AgentID = str("app_id"), str("agent_id")
	if n, ok := mc["ent_v"].(float64); ok {
		c.EntVersion = int64(n)
	}
	c.Audience, _ = mc.GetAudience()
	unix := func(k string) time.Time {
		if n, ok := mc[k].(float64); ok {
			return time.Unix(int64(n), 0)
		}
		return time.Time{}
	}
	c.AuthTime, c.ExpiresAt, c.IssuedAt = unix("auth_time"), unix("exp"), unix("iat")
	if a, ok := mc["act"].(map[string]any); ok {
		c.Actor = actorFrom(a)
	}
	return c, nil
}

func actorFrom(m map[string]any) *Actor {
	a := &Actor{}
	a.Subject, _ = m["sub"].(string)
	a.ClientID, _ = m["client_id"].(string)
	a.AgentID, _ = m["agent_id"].(string)
	if p, ok := m["act"].(map[string]any); ok {
		a.Actor = actorFrom(p)
	}
	return a
}

func (v *Verifier) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	if !v.fetched.IsZero() && v.now().Sub(v.fetched) < time.Minute {
		return nil, errors.New("unknown key id")
	}
	hc := v.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	d, err := Discover(ctx, hc, v.Issuer)
	if err != nil {
		return nil, err
	}
	keys, err := FetchJWKS(ctx, hc, d.JWKSURI)
	if err != nil {
		return nil, err
	}
	v.keys, v.fetched = keys, v.now()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, errors.New("unknown key id")
}

// JWK is one JSON Web Key (RSA, EC P-256 or OKP Ed25519).
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg,omitempty"`
	Use string `json:"use,omitempty"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
}

// FetchJWKS downloads a key set and returns its usable public keys by kid.
func FetchJWKS(ctx context.Context, hc *http.Client, url string) (map[string]crypto.PublicKey, error) {
	var set struct {
		Keys []JWK `json:"keys"`
	}
	if err := getJSON(ctx, hc, url, "", &set); err != nil {
		return nil, fmt.Errorf("circlexo: jwks: %w", err)
	}
	out := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if pk, err := k.PublicKey(); err == nil && k.Kid != "" {
			out[k.Kid] = pk
		}
	}
	return out, nil
}

// PublicKey decodes the key.
func (k JWK) PublicKey() (crypto.PublicKey, error) {
	b := func(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
	switch k.Kty {
	case "RSA":
		n, err := b(k.N)
		if err != nil {
			return nil, err
		}
		e, err := b(k.E)
		if err != nil {
			return nil, err
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	case "OKP":
		x, err := b(k.X)
		if err != nil || k.Crv != "Ed25519" || len(x) != ed25519.PublicKeySize {
			return nil, errors.New("circlexo: bad OKP key")
		}
		return ed25519.PublicKey(x), nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, errors.New("circlexo: unsupported curve")
		}
		x, err1 := b(k.X)
		y, err2 := b(k.Y)
		if err1 != nil || err2 != nil {
			return nil, errors.New("circlexo: bad EC key")
		}
		return &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}, nil
	}
	return nil, fmt.Errorf("circlexo: unsupported key type %q", k.Kty)
}

// JWKOf returns the public JWK of key (for tests and for publishing a
// private_key_jwt public key).
func JWKOf(kid string, key crypto.PublicKey) (JWK, error) {
	enc := base64.RawURLEncoding.EncodeToString
	switch pk := key.(type) {
	case *rsa.PublicKey:
		return JWK{Kty: "RSA", Kid: kid, Alg: "RS256", Use: "sig", N: enc(pk.N.Bytes()), E: enc(big.NewInt(int64(pk.E)).Bytes())}, nil
	case ed25519.PublicKey:
		return JWK{Kty: "OKP", Kid: kid, Alg: "EdDSA", Use: "sig", Crv: "Ed25519", X: enc(pk)}, nil
	case *ecdsa.PublicKey:
		x, y := make([]byte, 32), make([]byte, 32)
		pk.X.FillBytes(x)
		pk.Y.FillBytes(y)
		return JWK{Kty: "EC", Kid: kid, Alg: "ES256", Use: "sig", Crv: "P-256", X: enc(x), Y: enc(y)}, nil
	}
	return JWK{}, errors.New("circlexo: unsupported key")
}
