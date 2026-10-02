package cms

import (
	"bytes"
	"encoding/json"
	"time"
)

// Localized is a localized value, {"en":"..","ar":".."}. The CMS may answer a
// plain string when a locale was requested; that decodes under the "" key.
type Localized map[string]string

// UnmarshalJSON accepts an object or a plain string.
func (l *Localized) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*l = Localized{"": s}
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*l = m
	return nil
}

// Get returns the value for locale, falling back to the plain value, then en.
func (l Localized) Get(locale string) string {
	if v, ok := l[locale]; ok && v != "" {
		return v
	}
	if v, ok := l[""]; ok && v != "" {
		return v
	}
	return l["en"]
}

// Resolution kinds.
const (
	KindEntry    = "entry"
	KindRedirect = "redirect"
	KindRoute    = "route"
	KindNotFound = "not_found"
)

// Resolution is the answer to "what is at host + path".
type Resolution struct {
	Kind     string    `json:"kind"`
	Status   int       `json:"status"`
	Site     Site      `json:"site"`
	Locale   string    `json:"locale"`
	Path     string    `json:"path"`
	Entry    *Entry    `json:"entry,omitempty"`
	Redirect *Redirect `json:"redirect,omitempty"`
	Route    *Route    `json:"route,omitempty"`
	SEO      *SEO      `json:"seo,omitempty"`
}

// IsRedirect reports whether the path redirects (to Redirect.To).
func (r *Resolution) IsRedirect() bool {
	return r != nil && r.Kind == KindRedirect && r.Redirect != nil
}

// IsRoute reports whether the path belongs to a registered app route.
func (r *Resolution) IsRoute() bool { return r != nil && r.Kind == KindRoute && r.Route != nil }

// IsEntry reports whether the path is a published CMS entry.
func (r *Resolution) IsEntry() bool { return r != nil && r.Kind == KindEntry && r.Entry != nil }

// NotFound reports whether nothing lives at the path.
func (r *Resolution) NotFound() bool { return r == nil || r.Kind == KindNotFound }

// OwnRoute reports whether r is a route registered by app.
func (r *Resolution) OwnRoute(app string) bool { return r.IsRoute() && r.Route.App == app }

// Param returns a route parameter, such as "slug" for /products/{slug}.
func (r *Resolution) Param(name string) string {
	if r == nil || r.Route == nil {
		return ""
	}
	return r.Route.Params[name]
}

// Site is the project a host belongs to.
type Site struct {
	ID            string    `json:"id"`
	Slug          string    `json:"slug"`
	Name          Localized `json:"name"`
	DefaultLocale string    `json:"default_locale"`
	Locales       []string  `json:"locales"`
	Host          string    `json:"host"`
}

// Redirect is where a path moves to. Status is 301, 302 or 410.
type Redirect struct {
	To     string `json:"to"`
	Status int    `json:"status"`
}

// Route is a registered app route that matched. RenderURL, AppRef, Role and
// MountPath come from the project's binding of the app.
type Route struct {
	App       string            `json:"app,omitempty"`
	Pattern   string            `json:"pattern"`
	Name      string            `json:"name,omitempty"`
	Locales   []string          `json:"locales,omitempty"`
	SEO       map[string]any    `json:"seo,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	AppRef    string            `json:"app_ref,omitempty"`
	Role      string            `json:"role,omitempty"`
	MountPath string            `json:"mount_path,omitempty"`
	RenderURL string            `json:"render_url,omitempty"`
}

// Entry is a content entry. Fields holds the values in the requested locale;
// Localized holds the localized ones in every locale.
type Entry struct {
	ID          string               `json:"id"`
	Type        string               `json:"type"`
	Slug        string               `json:"slug"`
	Status      string               `json:"status,omitempty"`
	PublishedAt *time.Time           `json:"published_at,omitempty"`
	UpdatedAt   *time.Time           `json:"updated_at,omitempty"`
	Fields      map[string]any       `json:"fields"`
	Localized   map[string]Localized `json:"localized,omitempty"`
	SEO         *SEO                 `json:"seo,omitempty"`
}

// Title returns fields.title when it is a string.
func (e Entry) Title() string {
	s, _ := e.Fields["title"].(string)
	return s
}

// SEO is what to put in the page head.
type SEO struct {
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Keywords    string           `json:"keywords,omitempty"`
	Canonical   string           `json:"canonical,omitempty"`
	Robots      string           `json:"robots,omitempty"`
	OG          OpenGraph        `json:"og"`
	Alternates  []Alternate      `json:"alternates,omitempty"`
	JSONLD      []map[string]any `json:"jsonld,omitempty"`
}

// OpenGraph is the og: tag set.
type OpenGraph struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image,omitempty"`
	Type        string `json:"type,omitempty"`
	Locale      string `json:"locale,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
}

// Alternate is one hreflang link.
type Alternate struct {
	Locale string `json:"locale"`
	Href   string `json:"href"`
}

// Menu is a navigation tree.
type Menu struct {
	Key   string     `json:"key"`
	Items []MenuItem `json:"items"`
}

// MenuItem is one navigation link.
type MenuItem struct {
	Label    Localized  `json:"label"`
	URL      string     `json:"url"`
	Children []MenuItem `json:"children,omitempty"`
}

// EntryList is a page of published entries.
type EntryList struct {
	Items []Entry `json:"items"`
	Total int     `json:"total"`
}

// RouteRegistration is one route an app serves, relative to the project host.
type RouteRegistration struct {
	Pattern string         `json:"pattern"`
	Name    string         `json:"name,omitempty"`
	Locales []string       `json:"locales,omitempty"`
	SEO     map[string]any `json:"seo,omitempty"`
}

// SitemapEntry is one URL an app contributes to the project's sitemap.
type SitemapEntry struct {
	Path       string            `json:"path"`
	Lastmod    *time.Time        `json:"lastmod,omitempty"`
	Changefreq string            `json:"changefreq,omitempty"`
	Priority   float64           `json:"priority,omitempty"`
	Title      Localized         `json:"title,omitempty"`
	Alternates map[string]string `json:"alternates,omitempty"`
}

// Project is a CMS site: domains, theme and bound apps. Domains, Theme and
// Bindings are filled by GetProject.
type Project struct {
	ID            string         `json:"id"`
	Slug          string         `json:"slug"`
	Name          Localized      `json:"name"`
	IsPrimary     bool           `json:"is_primary,omitempty"`
	DefaultLocale string         `json:"default_locale,omitempty"`
	Locales       []string       `json:"locales,omitempty"`
	Status        string         `json:"status,omitempty"`
	Domains       []Domain       `json:"domains,omitempty"`
	Theme         map[string]any `json:"theme,omitempty"`
	Bindings      []Binding      `json:"bindings,omitempty"`
	CreatedAt     *time.Time     `json:"created_at,omitempty"`
}

// Domain is a host that serves a project. Kind is system or custom.
type Domain struct {
	Host        string     `json:"host"`
	Kind        string     `json:"kind"`
	IsPrimary   bool       `json:"is_primary"`
	VerifyToken string     `json:"verify_token,omitempty"`
	VerifiedAt  *time.Time `json:"verified_at,omitempty"`
}

// Binding binds an app to a project.
type Binding struct {
	App       string     `json:"app"`
	AppRef    string     `json:"app_ref"`
	Role      string     `json:"role,omitempty"`
	MountPath string     `json:"mount_path,omitempty"`
	RenderURL string     `json:"render_url,omitempty"`
	Status    string     `json:"status,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

// NewProject is the body of CreateProject.
type NewProject struct {
	Name          string `json:"name"`
	Slug          string `json:"slug,omitempty"`
	DefaultLocale string `json:"default_locale,omitempty"`
}
