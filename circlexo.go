// Package circlexo connects a product to the CircleXO hub: configuration,
// discovery, access-token verification against the hub's JWKS, and a client
// for the hub's app APIs (entitlements, usage, tenants, org directory).
//
// The sub-packages build on it:
//
//	oidc          relying-party sign-in (PKCE, PAR) and sign-out
//	middleware    net/http middleware that puts a Principal in the context
//	entitlements  cached entitlements with the fail-closed grace policy
//	usage         buffered, idempotent usage reporting
//	webhooks      Standard Webhooks verification and typed hub events
//	service       service tokens (client_credentials, private_key_jwt) and token exchange
//	mcp           verifying gateway-exchanged tokens on a product MCP endpoint
//	manifest      circlexo.app.yaml types and validation
package circlexo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Config is how a product finds and authenticates to the hub.
type Config struct {
	// Issuer is the hub's OIDC issuer, e.g. https://accounts.circlexo.com.
	Issuer string
	// APIURL is the hub API base; empty means Issuer.
	APIURL string
	// AppID is the product's app id; access tokens for it carry it as aud.
	AppID string
	// ClientID defaults to "cxo_app_" + AppID.
	ClientID string
	// ClientSecret authenticates the client (client_secret_basic). Leave it
	// empty when using PrivateKeyPEM (private_key_jwt).
	ClientSecret string
	// PrivateKeyPEM and KeyID sign private_key_jwt client assertions.
	PrivateKeyPEM string
	KeyID         string
	// AppKey is a cxa_ app API key, an alternative to service tokens for the
	// hub's app APIs.
	AppKey string
	// HTTPClient is used for every hub call; nil means a client with a 15s timeout.
	HTTPClient *http.Client
}

// FromEnv reads the configuration from CIRCLEXO_ISSUER, CIRCLEXO_API_URL,
// CIRCLEXO_APP_ID, CIRCLEXO_CLIENT_ID, CIRCLEXO_CLIENT_SECRET,
// CIRCLEXO_PRIVATE_KEY (PEM, or a path to a PEM file), CIRCLEXO_KEY_ID and
// CIRCLEXO_APP_KEY. Issuer and app id are required.
func FromEnv() (Config, error) {
	c := Config{
		Issuer:       os.Getenv("CIRCLEXO_ISSUER"),
		APIURL:       os.Getenv("CIRCLEXO_API_URL"),
		AppID:        os.Getenv("CIRCLEXO_APP_ID"),
		ClientID:     os.Getenv("CIRCLEXO_CLIENT_ID"),
		ClientSecret: os.Getenv("CIRCLEXO_CLIENT_SECRET"),
		KeyID:        os.Getenv("CIRCLEXO_KEY_ID"),
		AppKey:       os.Getenv("CIRCLEXO_APP_KEY"),
	}
	if k := os.Getenv("CIRCLEXO_PRIVATE_KEY"); k != "" {
		if !strings.Contains(k, "-----BEGIN") {
			b, err := os.ReadFile(k)
			if err != nil {
				return Config{}, fmt.Errorf("circlexo: CIRCLEXO_PRIVATE_KEY: %w", err)
			}
			k = string(b)
		}
		c.PrivateKeyPEM = k
	}
	return c, c.Validate()
}

// Validate checks the required fields and fills the defaults.
func (c *Config) Validate() error {
	c.Issuer = strings.TrimRight(c.Issuer, "/")
	if c.Issuer == "" {
		return errors.New("circlexo: issuer is required")
	}
	if c.AppID == "" {
		return errors.New("circlexo: app id is required")
	}
	if c.ClientID == "" {
		c.ClientID = "cxo_app_" + c.AppID
	}
	c.APIURL = strings.TrimRight(c.APIURL, "/")
	if c.APIURL == "" {
		c.APIURL = c.Issuer
	}
	return nil
}

// HTTP returns the configured HTTP client.
func (c Config) HTTP() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return defaultHTTP
}

var defaultHTTP = &http.Client{Timeout: 15 * time.Second}

// Discovery is the subset of the hub's OpenID configuration the SDK uses.
type Discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	RevocationEndpoint    string   `json:"revocation_endpoint"`
	EndSessionEndpoint    string   `json:"end_session_endpoint"`
	PAREndpoint           string   `json:"pushed_authorization_request_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
}

var (
	discMu    sync.Mutex
	discCache = map[string]discEntry{}
)

type discEntry struct {
	d  Discovery
	at time.Time
}

// Discover fetches (and caches for an hour) the issuer's OpenID configuration.
func Discover(ctx context.Context, hc *http.Client, issuer string) (Discovery, error) {
	issuer = strings.TrimRight(issuer, "/")
	discMu.Lock()
	e, ok := discCache[issuer]
	discMu.Unlock()
	if ok && time.Since(e.at) < time.Hour {
		return e.d, nil
	}
	var d Discovery
	if err := getJSON(ctx, hc, issuer+"/.well-known/openid-configuration", "", &d); err != nil {
		return Discovery{}, fmt.Errorf("circlexo: discovery: %w", err)
	}
	if d.Issuer != issuer {
		return Discovery{}, fmt.Errorf("circlexo: discovery issuer %q does not match %q", d.Issuer, issuer)
	}
	discMu.Lock()
	discCache[issuer] = discEntry{d, time.Now()}
	discMu.Unlock()
	return d, nil
}

// Discover fetches the configured issuer's OpenID configuration.
func (c Config) Discover(ctx context.Context) (Discovery, error) {
	return Discover(ctx, c.HTTP(), c.Issuer)
}

// APIError is a non-2xx answer from the hub. Code is the hub's machine
// readable error code when it sent one (e.g. not_installed).
type APIError struct {
	Status int
	Code   string
	Detail string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("circlexo: hub answered %d %s: %s", e.Status, e.Code, e.Detail)
	}
	return fmt.Sprintf("circlexo: hub answered %d: %s", e.Status, e.Detail)
}

// IsStatus reports whether err is an APIError with the given status.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

func getJSON(ctx context.Context, hc *http.Client, url, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return do(hc, req, out)
}

func do(hc *http.Client, req *http.Request, out any) error {
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return decodeError(res)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func decodeError(res *http.Response) error {
	var body struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Code   string `json:"code"`
		Msg    string `json:"message"`
		Error  string `json:"error"`
		Desc   string `json:"error_description"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(nil, res.Body, 64<<10)).Decode(&body)
	e := &APIError{Status: res.StatusCode, Code: body.Code, Detail: body.Detail}
	if e.Code == "" {
		e.Code = body.Error
	}
	if e.Detail == "" {
		e.Detail = body.Msg
	}
	if e.Detail == "" {
		e.Detail = body.Desc
	}
	if e.Detail == "" {
		e.Detail = body.Title
	}
	if e.Detail == "" {
		e.Detail = http.StatusText(res.StatusCode)
	}
	return e
}
