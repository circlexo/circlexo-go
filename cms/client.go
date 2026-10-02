// Package cms is the client for the Nasaq CMS engine: resolve a host and path
// to an entry, a redirect or an app route, register an app's routes and
// sitemap, read menus and entries, and manage projects and app bindings.
//
// Nasaq is the resolver, not a proxy: an app asks Resolve what lives at the
// request's host and path, and renders it (or redirects to its renderer).
package cms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/circlexo/circlexo-go"
)

// DefaultURL is the CMS when CIRCLEXO_CMS_URL is not set.
const DefaultURL = "https://nasaq.circlexo.com"

var defaultHTTP = &http.Client{Timeout: 15 * time.Second}

// Client calls the CMS app API. The public delivery calls (Resolve, Menu,
// Entries, Entry) work without a token; the token is sent when the source has
// one. Routes, sitemap and projects need the app key or a service token.
type Client struct {
	BaseURL string
	Tokens  circlexo.TokenSource
	HTTP    *http.Client
}

// New returns a client for baseURL; an empty one reads CIRCLEXO_CMS_URL and
// then DefaultURL. A nil tokens means public reads only.
func New(baseURL string, tokens circlexo.TokenSource) *Client {
	if baseURL == "" {
		baseURL = os.Getenv("CIRCLEXO_CMS_URL")
	}
	if baseURL == "" {
		baseURL = DefaultURL
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Tokens: tokens, HTTP: defaultHTTP}
}

// NewClient returns a client for the CMS in CIRCLEXO_CMS_URL, authenticated
// the same way as the hub client: by tokens (service.Source), or cfg.AppKey
// when tokens is nil.
func NewClient(cfg circlexo.Config, tokens circlexo.TokenSource) *Client {
	if tokens == nil && cfg.AppKey != "" {
		tokens = circlexo.StaticToken(cfg.AppKey)
	}
	c := New("", tokens)
	if cfg.HTTPClient != nil {
		c.HTTP = cfg.HTTPClient
	}
	return c
}

// Scope names the project a read is about: by host (public delivery) or by
// project id or slug.
type Scope struct{ Host, Project string }

// ForHost scopes a read to the project served at host.
func ForHost(host string) Scope { return Scope{Host: host} }

// InProject scopes a read to a project id or slug.
func InProject(idOrSlug string) Scope { return Scope{Project: idOrSlug} }

func (s Scope) values(v url.Values) {
	if s.Host != "" {
		v.Set("host", s.Host)
	}
	if s.Project != "" {
		v.Set("site", s.Project)
	}
}

// Resolve answers what lives at host + path. An empty locale is derived from
// the path (a leading /ar) or the site default.
func (c *Client) Resolve(ctx context.Context, host, path, locale string) (*Resolution, error) {
	v := url.Values{"host": {host}, "path": {path}}
	if locale != "" {
		v.Set("locale", locale)
	}
	var r Resolution
	if err := c.call(ctx, http.MethodGet, "/api/cms/v1/resolve?"+v.Encode(), nil, &r, false); err != nil {
		return nil, err
	}
	return &r, nil
}

// RegisterRoutes replaces the routes app serves in project (id, slug or host)
// and returns how many were registered.
func (c *Client) RegisterRoutes(ctx context.Context, project, app string, routes []RouteRegistration) (int, error) {
	var out struct {
		Registered int `json:"registered"`
	}
	in := map[string]any{"site": project, "app": app, "routes": routes}
	err := c.call(ctx, http.MethodPut, "/api/cms/v1/routes", in, &out, true)
	return out.Registered, err
}

// PushSitemap stores the URLs app contributes to project's sitemap and
// returns how many were stored. With replace, the app's earlier entries go.
func (c *Client) PushSitemap(ctx context.Context, project, app string, entries []SitemapEntry, replace bool) (int, error) {
	var out struct {
		Stored int `json:"stored"`
	}
	in := map[string]any{"site": project, "app": app, "replace": replace, "entries": entries}
	err := c.call(ctx, http.MethodPut, "/api/cms/v1/sitemap", in, &out, true)
	return out.Stored, err
}

// Menu returns the menu key of the scoped project.
func (c *Client) Menu(ctx context.Context, s Scope, key, locale string) (*Menu, error) {
	v := url.Values{}
	s.values(v)
	if locale != "" {
		v.Set("locale", locale)
	}
	var m Menu
	if err := c.call(ctx, http.MethodGet, "/api/cms/v1/menus/"+url.PathEscape(key)+"?"+v.Encode(), nil, &m, false); err != nil {
		return nil, err
	}
	return &m, nil
}

// ListOpts filters Entries. Limit 0 means the CMS default.
type ListOpts struct {
	Scope
	Type   string
	Locale string
	Limit  int
	Offset int
}

// Entries lists published entries.
func (c *Client) Entries(ctx context.Context, o ListOpts) (*EntryList, error) {
	v := url.Values{}
	o.Scope.values(v)
	if o.Type != "" {
		v.Set("type", o.Type)
	}
	if o.Locale != "" {
		v.Set("locale", o.Locale)
	}
	if o.Limit > 0 {
		v.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		v.Set("offset", strconv.Itoa(o.Offset))
	}
	var raw json.RawMessage
	if err := c.call(ctx, http.MethodGet, "/api/cms/v1/entries?"+v.Encode(), nil, &raw, false); err != nil {
		return nil, err
	}
	var l EntryList
	if err := decodeList(raw, &l.Items, &l.Total); err != nil {
		return nil, err
	}
	return &l, nil
}

// Entry returns one published entry by type and slug.
func (c *Client) Entry(ctx context.Context, s Scope, typ, slug, locale string) (*Entry, error) {
	v := url.Values{}
	s.values(v)
	if locale != "" {
		v.Set("locale", locale)
	}
	var raw json.RawMessage
	p := "/api/cms/v1/entries/" + url.PathEscape(typ) + "/" + url.PathEscape(slug) + "?" + v.Encode()
	if err := c.call(ctx, http.MethodGet, p, nil, &raw, false); err != nil {
		return nil, err
	}
	var wrapped struct {
		Entry *Entry `json:"entry"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Entry != nil {
		return wrapped.Entry, nil
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// ListProjects lists the org's projects.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var raw json.RawMessage
	if err := c.call(ctx, http.MethodGet, "/api/cms/v1/projects", nil, &raw, true); err != nil {
		return nil, err
	}
	var out []Project
	err := decodeList(raw, &out, nil)
	return out, err
}

// CreateProject creates a project.
func (c *Client) CreateProject(ctx context.Context, p NewProject) (*Project, error) {
	var out Project
	if err := c.call(ctx, http.MethodPost, "/api/cms/v1/projects", p, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetProject returns a project with its domains, theme and bindings.
func (c *Client) GetProject(ctx context.Context, id string) (*Project, error) {
	var out Project
	if err := c.call(ctx, http.MethodGet, "/api/cms/v1/projects/"+url.PathEscape(id), nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// BindApp binds an app to a project (or updates the binding).
func (c *Client) BindApp(ctx context.Context, projectID string, b Binding) (*Binding, error) {
	var out Binding
	if err := c.call(ctx, http.MethodPost, "/api/cms/v1/projects/"+url.PathEscape(projectID)+"/bind", b, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// Domains lists the hosts that serve a project.
func (c *Client) Domains(ctx context.Context, projectID string) ([]Domain, error) {
	var raw json.RawMessage
	if err := c.call(ctx, http.MethodGet, "/api/cms/v1/projects/"+url.PathEscape(projectID)+"/domains", nil, &raw, true); err != nil {
		return nil, err
	}
	var out []Domain
	err := decodeList(raw, &out, nil)
	return out, err
}

// Theme returns a project's theme settings.
func (c *Client) Theme(ctx context.Context, projectID string) (map[string]any, error) {
	var out map[string]any
	err := c.call(ctx, http.MethodGet, "/api/cms/v1/projects/"+url.PathEscape(projectID)+"/theme", nil, &out, true)
	return out, err
}

// decodeList reads either a bare array or an object with items (and total).
func decodeList[T any](raw json.RawMessage, items *[]T, total *int) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, items); err != nil {
			return err
		}
		if total != nil {
			*total = len(*items)
		}
		return nil
	}
	var o struct {
		Items []T  `json:"items"`
		Total *int `json:"total"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return err
	}
	*items = o.Items
	if total != nil {
		*total = len(o.Items)
		if o.Total != nil {
			*total = *o.Total
		}
	}
	return nil
}

// call sends the request. Authenticated calls fail without a token; public
// ones send it when there is one.
func (c *Client) call(ctx context.Context, method, path string, in, out any, needAuth bool) error {
	var tok string
	if c.Tokens != nil {
		t, err := c.Tokens.Token(ctx)
		if err != nil && needAuth {
			return err
		}
		tok = t
	} else if needAuth {
		return errors.New("cms: no token source")
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
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(nil, res.Body, 64<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = http.StatusText(res.StatusCode)
		}
		return &circlexo.APIError{Status: res.StatusCode, Code: e.Code, Detail: e.Error}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("cms: decode %s: %w", path, err)
	}
	return nil
}
