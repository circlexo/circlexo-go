package circlexo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// TokenSource supplies the bearer for the hub's app APIs: a cxa_ app key
// (StaticToken) or a service token (service.Source).
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a fixed bearer, such as a cxa_ app key.
type StaticToken string

// Token returns t.
func (t StaticToken) Token(context.Context) (string, error) {
	if t == "" {
		return "", errors.New("circlexo: no app key")
	}
	return string(t), nil
}

// Client calls the hub's app APIs. Each call needs the scope named on it when
// the token source issues service tokens; an app key may call all of them.
type Client struct {
	BaseURL string
	Tokens  TokenSource
	HTTP    *http.Client
}

// NewClient returns a client for cfg authenticated by tokens. A nil tokens
// uses cfg.AppKey.
func NewClient(cfg Config, tokens TokenSource) *Client {
	if tokens == nil {
		tokens = StaticToken(cfg.AppKey)
	}
	return &Client{BaseURL: cfg.APIURL, Tokens: tokens, HTTP: cfg.HTTP()}
}

// Entitlements is what an org may use in the app right now.
type Entitlements struct {
	OrgID        string        `json:"org_id"`
	AppID        string        `json:"app_id"`
	Active       bool          `json:"active"` // the org may use the app at all
	Plan         string        `json:"plan"`
	Status       string        `json:"status"`
	PeriodStart  time.Time     `json:"period_start"`
	PeriodEnd    time.Time     `json:"period_end"`
	PaygEnabled  bool          `json:"payg_enabled"`
	BalanceMinor int64         `json:"balance_minor"`
	Currency     string        `json:"currency"`
	Features     []Entitlement `json:"features"`
	// Version is the ent_v claim it matches; a token with a higher ent_v means
	// these are out of date.
	Version int64 `json:"version"`
}

// Entitlement is one feature's state. Limit is nil when unlimited.
type Entitlement struct {
	Key            string `json:"key"`
	Kind           string `json:"kind"`
	Enabled        bool   `json:"enabled"`
	Limit          *int64 `json:"limit"`
	IncludedQty    int64  `json:"included_qty"`
	UsedInPeriod   int64  `json:"used_in_period"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
}

// Feature returns the named feature, if the hub listed it.
func (e Entitlements) Feature(key string) (Entitlement, bool) {
	for _, f := range e.Features {
		if f.Key == key {
			return f, true
		}
	}
	return Entitlement{}, false
}

// Entitlements fetches orgID's entitlements (scope billing.entitlements).
func (c *Client) Entitlements(ctx context.Context, orgID string) (Entitlements, error) {
	var e Entitlements
	err := c.call(ctx, http.MethodGet, "/api/billing/entitlements?org_id="+url.QueryEscape(orgID), nil, &e)
	return e, err
}

// UsageReport is one metered use of a feature.
type UsageReport struct {
	OrgID   string `json:"org_id"`
	Feature string `json:"feature"`
	Qty     int64  `json:"qty"`
	// IdempotencyKey makes retries safe: the hub charges a key once.
	IdempotencyKey string `json:"idempotency_key"`
}

// UsageResult is what the hub charged for a report.
type UsageResult struct {
	Qty            int64 `json:"qty"`
	BillableQty    int64 `json:"billable_qty"`
	UnitPriceMinor int64 `json:"unit_price_minor"`
	AmountMinor    int64 `json:"amount_minor"`
	BalanceMinor   int64 `json:"balance_minor"`
	UsedInPeriod   int64 `json:"used_in_period"`
	IncludedQty    int64 `json:"included_qty"`
	LowBalance     bool  `json:"low_balance"`
	Replayed       bool  `json:"replayed"`
}

// ReportUsage records usage (scope billing.usage). A 402 APIError means the
// org is over its limit or out of balance.
func (c *Client) ReportUsage(ctx context.Context, r UsageReport) (UsageResult, error) {
	var out UsageResult
	err := c.call(ctx, http.MethodPost, "/api/billing/usage", r, &out)
	return out, err
}

// Tenant links a hub org to the product's own tenant.
type Tenant struct {
	OrgID             string    `json:"org_id"`
	AppID             string    `json:"app_id"`
	ProductTenantID   string    `json:"product_tenant_id"`
	ProductTenantSlug string    `json:"product_tenant_slug"`
	Status            string    `json:"status"` // provisioning, active, suspended
	InstalledBy       string    `json:"installed_by,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ConfirmTenant tells the hub which product tenant serves orgID, once the
// product has provisioned it (scope tenants.write). It activates the install.
func (c *Client) ConfirmTenant(ctx context.Context, orgID, productTenantID, slug string) (Tenant, error) {
	var t Tenant
	err := c.call(ctx, http.MethodPost, "/api/apps/tenants", map[string]string{"org_id": orgID, "product_tenant_id": productTenantID, "slug": slug}, &t)
	return t, err
}

// TenantByOrg returns the product tenant for a hub org (scope tenants.read).
// A 404 APIError means the org has not installed the app.
func (c *Client) TenantByOrg(ctx context.Context, orgID string) (Tenant, error) {
	var t Tenant
	err := c.call(ctx, http.MethodGet, "/api/apps/tenants?org_id="+url.QueryEscape(orgID), nil, &t)
	return t, err
}

// TenantByProductID returns the hub org for a product tenant (scope tenants.read).
func (c *Client) TenantByProductID(ctx context.Context, productTenantID string) (Tenant, error) {
	var t Tenant
	err := c.call(ctx, http.MethodGet, "/api/apps/tenants?product_tenant_id="+url.QueryEscape(productTenantID), nil, &t)
	return t, err
}

// Org is an org that installed the app, with its active members.
type Org struct {
	ID            string   `json:"id"`
	Slug          string   `json:"slug"`
	Name          string   `json:"name"`
	DefaultLocale string   `json:"default_locale"`
	Status        string   `json:"status"`
	Tenant        Tenant   `json:"tenant"`
	Members       []Member `json:"members"`
}

// Member is an org member; ProductUserID is the product's own id for them
// when their accounts are linked. Role is owner, admin, billing or member.
type Member struct {
	UserID        string `json:"user_id"`
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	Locale        string `json:"locale"`
	Role          string `json:"role"`
	ProductUserID string `json:"product_user_id,omitempty"`
}

// Org returns an installed org and its members (scope orgs.read).
func (c *Client) Org(ctx context.Context, orgID string) (Org, error) {
	var o Org
	err := c.call(ctx, http.MethodGet, "/api/apps/orgs/"+url.PathEscape(orgID), nil, &o)
	return o, err
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	tok, err := c.Tokens.Token(ctx)
	if err != nil {
		return err
	}
	var body *bytes.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	return do(hc, req, out)
}
