// Package service gets hub tokens for the product itself: service tokens by
// client_credentials (authenticated by client secret or private_key_jwt),
// and RFC 8693 token exchange.
package service

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/circlexo/circlexo-go"
)

// Token types for Exchange.
const (
	AccessTokenType = "urn:ietf:params:oauth:token-type:access_token"
	// AgentKeyType exchanges a CircleXO agent key for an access token.
	AgentKeyType = "urn:circlexo:params:oauth:token-type:agent_key"

	grantExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	assertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
)

// Token is a token endpoint answer.
type Token struct {
	AccessToken     string    `json:"access_token"`
	TokenType       string    `json:"token_type"`
	ExpiresIn       int       `json:"expires_in"`
	Scope           string    `json:"scope"`
	IssuedTokenType string    `json:"issued_token_type,omitempty"`
	RefreshToken    string    `json:"refresh_token,omitempty"`
	IDToken         string    `json:"id_token,omitempty"`
	Expiry          time.Time `json:"-"`
}

// OAuthError is an error answer from the token endpoint.
type OAuthError struct {
	Status      int
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("circlexo: token endpoint: %s: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("circlexo: token endpoint: %s (%d)", e.Code, e.Status)
}

// Client authenticates the product to the hub's token endpoint.
type Client struct {
	cfg    circlexo.Config
	signer crypto.Signer
	method jwt.SigningMethod
	Now    func() time.Time
}

// New returns a client for cfg. With cfg.PrivateKeyPEM set it authenticates
// by private_key_jwt (RSA → RS256, P-256 → ES256, Ed25519 → EdDSA),
// otherwise by client_secret_basic.
func New(cfg circlexo.Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg}
	if cfg.PrivateKeyPEM != "" {
		k, m, err := parseKey(cfg.PrivateKeyPEM)
		if err != nil {
			return nil, err
		}
		c.signer, c.method = k, m
	} else if cfg.ClientSecret == "" {
		return nil, errors.New("circlexo: a client secret or a private key is required")
	}
	return c, nil
}

func parseKey(p string) (crypto.Signer, jwt.SigningMethod, error) {
	block, _ := pem.Decode([]byte(p))
	if block == nil {
		return nil, nil, errors.New("circlexo: private key is not PEM")
	}
	var key any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("circlexo: private key: %w", err)
	}
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return k, jwt.SigningMethodRS256, nil
	case *ecdsa.PrivateKey:
		return k, jwt.SigningMethodES256, nil
	case ed25519.PrivateKey:
		return k, jwt.SigningMethodEdDSA, nil
	}
	return nil, nil, errors.New("circlexo: unsupported private key type")
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// assertion is a private_key_jwt client assertion for the token endpoint.
func (c *Client) assertion(aud string) (string, error) {
	now := c.now()
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", err
	}
	t := jwt.NewWithClaims(c.method, jwt.MapClaims{
		"iss": c.cfg.ClientID, "sub": c.cfg.ClientID, "aud": aud, "jti": base64.RawURLEncoding.EncodeToString(jti),
		"iat": now.Unix(), "exp": now.Add(2 * time.Minute).Unix(),
	})
	if c.cfg.KeyID != "" {
		t.Header["kid"] = c.cfg.KeyID
	}
	return t.SignedString(c.signer)
}

// Post sends a token request with client authentication and decodes the answer.
func (c *Client) Post(ctx context.Context, form url.Values) (Token, error) {
	d, err := c.cfg.Discover(ctx)
	if err != nil {
		return Token{}, err
	}
	return c.PostTo(ctx, d.TokenEndpoint, form)
}

// PostTo is Post to a known endpoint (the token or PAR endpoint).
func (c *Client) PostTo(ctx context.Context, endpoint string, form url.Values) (Token, error) {
	var tok Token
	err := c.PostForm(ctx, endpoint, form, &tok)
	if err == nil && tok.ExpiresIn > 0 {
		tok.Expiry = c.now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	return tok, err
}

// PostForm posts an authenticated form to endpoint and decodes JSON into out.
func (c *Client) PostForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	f := url.Values{}
	for k, v := range form {
		f[k] = v
	}
	var basic bool
	if c.signer != nil {
		a, err := c.assertion(c.cfg.Issuer + "/oauth2/token")
		if err != nil {
			return err
		}
		f.Set("client_id", c.cfg.ClientID)
		f.Set("client_assertion_type", assertionType)
		f.Set("client_assertion", a)
	} else {
		basic = true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(f.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth(url.QueryEscape(c.cfg.ClientID), url.QueryEscape(c.cfg.ClientSecret))
	}
	res, err := c.cfg.HTTP().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		e := &OAuthError{Status: res.StatusCode}
		_ = json.NewDecoder(res.Body).Decode(e)
		if e.Code == "" {
			e.Code = http.StatusText(res.StatusCode)
		}
		return e
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// ServiceToken gets a client_credentials token for scopes (empty: every
// service scope the app is allowed).
func (c *Client) ServiceToken(ctx context.Context, scopes ...string) (Token, error) {
	f := url.Values{"grant_type": {"client_credentials"}}
	if len(scopes) > 0 {
		f.Set("scope", strings.Join(scopes, " "))
	}
	return c.Post(ctx, f)
}

// Exchange trades subjectToken (of subjectTokenType: AccessTokenType or
// AgentKeyType) for an access token for the audience app, with the product
// as the actor. scopes may narrow it.
func (c *Client) Exchange(ctx context.Context, subjectToken, subjectTokenType, audience string, scopes ...string) (Token, error) {
	if subjectTokenType == "" {
		subjectTokenType = AccessTokenType
	}
	f := url.Values{
		"grant_type": {grantExchange}, "subject_token": {subjectToken}, "subject_token_type": {subjectTokenType},
		"audience": {audience}, "requested_token_type": {AccessTokenType},
	}
	if len(scopes) > 0 {
		f.Set("scope", strings.Join(scopes, " "))
	}
	return c.Post(ctx, f)
}

// Source is a circlexo.TokenSource of cached service tokens, renewed a
// minute before they expire. It is safe for concurrent use.
type Source struct {
	Client *Client
	Scopes []string

	mu  sync.Mutex
	tok Token
}

// NewSource returns a token source for the scopes.
func NewSource(c *Client, scopes ...string) *Source { return &Source{Client: c, Scopes: scopes} }

// Token returns a valid service token.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tok.AccessToken != "" && s.Client.now().Add(time.Minute).Before(s.tok.Expiry) {
		return s.tok.AccessToken, nil
	}
	t, err := s.Client.ServiceToken(ctx, s.Scopes...)
	if err != nil {
		return "", err
	}
	s.tok = t
	return t.AccessToken, nil
}
