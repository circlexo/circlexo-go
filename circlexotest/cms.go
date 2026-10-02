package circlexotest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/cms"
)

// CMS is an in-memory Nasaq CMS for tests: the public delivery API (resolve,
// entries, menus) and the app API (routes, sitemap, projects, bindings).
// Seed projects and content, point a cms.Client at it, and assert on what the
// app registered. Guard changes made during requests with Lock/Unlock.
type CMS struct {
	*httptest.Server
	sync.Mutex

	// Token is the bearer the app API accepts.
	Token string
	// Projects in creation order.
	Projects []*CMSProject
	// Requests counts the calls served per "METHOD path" (query excluded).
	Requests map[string]int
	// Fail makes every call answer this status (0 = off).
	Fail int

	seq int
}

// CMSProject is a seeded project (site) with its content.
type CMSProject struct {
	cms.Project
	// Hosts that resolve to the project, the first being primary.
	Hosts []string
	// TypePatterns maps an entry type to its route pattern; page and post
	// default to /{slug} and /blog/{slug}.
	TypePatterns map[string]string
	Entries      []*CMSEntry
	Menus        map[string][]cms.MenuItem
	Redirects    []CMSRedirect
	// Routes and Sitemap are what apps registered, by app id.
	Routes  map[string][]cms.RouteRegistration
	Sitemap map[string][]cms.SitemapEntry
}

// CMSEntry is a seeded entry. Field values may be strings or cms.Localized.
type CMSEntry struct {
	ID        string
	Type      string
	Slug      string
	Published bool
	Fields    map[string]any
	UpdatedAt time.Time
}

// CMSRedirect redirects an exact path.
type CMSRedirect struct {
	From, To string
	Status   int // 301, 302 or 410
}

// NewCMS starts a fake CMS. It closes when the test ends.
func NewCMS(t testing.TB) *CMS {
	c := &CMS{Token: "cxa_test_key", Requests: map[string]int{}}
	c.Server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.Close)
	return c
}

// Client returns a cms.Client for the fake authenticated with its token.
func (c *CMS) Client() *cms.Client {
	return cms.New(c.URL, circlexo.StaticToken(c.Token))
}

// AddProject seeds a project (locales en and ar) served at hosts.
func (c *CMS) AddProject(slug string, hosts ...string) *CMSProject {
	c.Lock()
	defer c.Unlock()
	return c.addProject(slug, hosts)
}

func (c *CMS) addProject(slug string, hosts []string) *CMSProject {
	c.seq++
	p := &CMSProject{
		Project: cms.Project{
			ID: "proj-" + strconv.Itoa(c.seq), Slug: slug, Name: cms.Localized{"en": slug},
			DefaultLocale: "en", Locales: []string{"en", "ar"}, Status: "active", IsPrimary: len(c.Projects) == 0,
		},
		Hosts:        hosts,
		TypePatterns: map[string]string{"page": "/{slug}", "post": "/blog/{slug}"},
		Menus:        map[string][]cms.MenuItem{},
		Routes:       map[string][]cms.RouteRegistration{},
		Sitemap:      map[string][]cms.SitemapEntry{},
	}
	c.Projects = append(c.Projects, p)
	return p
}

// AddEntry seeds a published entry. Use "home" as a page slug for the root.
func (p *CMSProject) AddEntry(typ, slug string, fields map[string]any) *CMSEntry {
	e := &CMSEntry{ID: "entry-" + typ + "-" + slug, Type: typ, Slug: slug, Published: true, Fields: fields, UpdatedAt: time.Now().UTC()}
	p.Entries = append(p.Entries, e)
	return e
}

// AddDraft seeds an unpublished entry; it never resolves or lists.
func (p *CMSProject) AddDraft(typ, slug string, fields map[string]any) *CMSEntry {
	e := p.AddEntry(typ, slug, fields)
	e.Published = false
	return e
}

// AddRedirect seeds an exact-path redirect.
func (p *CMSProject) AddRedirect(from, to string, status int) {
	p.Redirects = append(p.Redirects, CMSRedirect{from, to, status})
}

// SetMenu seeds a menu.
func (p *CMSProject) SetMenu(key string, items ...cms.MenuItem) { p.Menus[key] = items }

// Bind seeds an app binding.
func (p *CMSProject) Bind(b cms.Binding) {
	if b.Status == "" {
		b.Status = "active"
	}
	if b.MountPath == "" {
		b.MountPath = "/"
	}
	for i := range p.Bindings {
		if p.Bindings[i].App == b.App {
			p.Bindings[i] = b
			return
		}
	}
	p.Bindings = append(p.Bindings, b)
}

// Route registers routes for app directly, as RegisterRoutes would.
func (p *CMSProject) Route(app string, routes ...cms.RouteRegistration) { p.Routes[app] = routes }

func (c *CMS) serve(w http.ResponseWriter, r *http.Request) {
	c.Lock()
	defer c.Unlock()
	c.Requests[r.Method+" "+r.URL.Path]++
	if c.Fail != 0 {
		cmsErr(w, c.Fail, "failure injected", "injected")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/cms/v1")
	if path == r.URL.Path {
		cmsErr(w, 404, "not found", "not_found")
		return
	}
	q := r.URL.Query()
	switch {
	case path == "/resolve" && r.Method == http.MethodGet:
		c.resolve(w, q)
		return
	case path == "/entries" && r.Method == http.MethodGet:
		c.entries(w, q)
		return
	case strings.HasPrefix(path, "/entries/") && r.Method == http.MethodGet:
		c.entry(w, q, strings.TrimPrefix(path, "/entries/"))
		return
	case strings.HasPrefix(path, "/menus/") && r.Method == http.MethodGet:
		c.menu(w, q, strings.TrimPrefix(path, "/menus/"))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+c.Token {
		cmsErr(w, 401, "unauthorized", "unauthorized")
		return
	}
	switch {
	case path == "/routes" && r.Method == http.MethodPut:
		var in struct {
			Site, App string
			Routes    []cms.RouteRegistration
		}
		if !cmsBody(w, r, &in) {
			return
		}
		p := c.find(in.Site)
		if p == nil {
			cmsErr(w, 404, "unknown project", "project_not_found")
			return
		}
		p.Routes[in.App] = in.Routes
		cmsJSON(w, 200, map[string]int{"registered": len(in.Routes)})
	case path == "/sitemap" && r.Method == http.MethodPut:
		var in struct {
			Site, App string
			Replace   bool
			Entries   []cms.SitemapEntry
		}
		if !cmsBody(w, r, &in) {
			return
		}
		p := c.find(in.Site)
		if p == nil {
			cmsErr(w, 404, "unknown project", "project_not_found")
			return
		}
		if in.Replace {
			p.Sitemap[in.App] = nil
		}
		p.Sitemap[in.App] = append(p.Sitemap[in.App], in.Entries...)
		cmsJSON(w, 200, map[string]int{"stored": len(in.Entries)})
	case path == "/projects" && r.Method == http.MethodGet:
		out := []cms.Project{}
		for _, p := range c.Projects {
			sp := p.Project
			sp.Domains, sp.Theme, sp.Bindings = nil, nil, nil
			out = append(out, sp)
		}
		cmsJSON(w, 200, map[string]any{"items": out})
	case path == "/projects" && r.Method == http.MethodPost:
		var in cms.NewProject
		if !cmsBody(w, r, &in) {
			return
		}
		if in.Name == "" {
			cmsErr(w, 400, "name is required", "invalid")
			return
		}
		slug := in.Slug
		if slug == "" {
			slug = strings.ToLower(strings.ReplaceAll(in.Name, " ", "-"))
		}
		p := c.addProject(slug, []string{slug + "-org.circlexo.com"})
		p.Name = cms.Localized{"en": in.Name}
		if in.DefaultLocale != "" {
			p.DefaultLocale = in.DefaultLocale
		}
		cmsJSON(w, 201, p.view())
	case strings.HasPrefix(path, "/projects/"):
		c.project(w, r, strings.Split(strings.TrimPrefix(path, "/projects/"), "/"))
	default:
		cmsErr(w, 404, "not found", "not_found")
	}
}

func (c *CMS) project(w http.ResponseWriter, r *http.Request, seg []string) {
	p := c.find(seg[0])
	if p == nil {
		cmsErr(w, 404, "unknown project", "project_not_found")
		return
	}
	sub := ""
	if len(seg) > 1 {
		sub = seg[1]
	}
	switch {
	case sub == "" && r.Method == http.MethodGet:
		cmsJSON(w, 200, p.view())
	case sub == "bind" && r.Method == http.MethodPost:
		var b cms.Binding
		if !cmsBody(w, r, &b) {
			return
		}
		if b.App == "" || b.AppRef == "" {
			cmsErr(w, 400, "app and app_ref are required", "invalid")
			return
		}
		p.Bind(b)
		for _, x := range p.Bindings {
			if x.App == b.App {
				cmsJSON(w, 200, x)
			}
		}
	case sub == "domains" && r.Method == http.MethodGet:
		cmsJSON(w, 200, map[string]any{"items": p.domains()})
	case sub == "theme" && r.Method == http.MethodGet:
		th := p.Theme
		if th == nil {
			th = map[string]any{}
		}
		cmsJSON(w, 200, th)
	default:
		cmsErr(w, 404, "not found", "not_found")
	}
}

func (p *CMSProject) domains() []cms.Domain {
	out := []cms.Domain{}
	now := time.Now().UTC()
	for i, h := range p.Hosts {
		kind := "custom"
		if strings.HasSuffix(h, ".circlexo.com") {
			kind = "system"
		}
		out = append(out, cms.Domain{Host: h, Kind: kind, IsPrimary: i == 0, VerifiedAt: &now})
	}
	return out
}

func (p *CMSProject) view() cms.Project {
	v := p.Project
	v.Domains = p.domains()
	if v.Theme == nil {
		v.Theme = map[string]any{}
	}
	return v
}

// find looks a project up by id, slug or host.
func (c *CMS) find(key string) *CMSProject {
	key = strings.ToLower(key)
	for _, p := range c.Projects {
		if strings.ToLower(p.ID) == key || strings.ToLower(p.Slug) == key {
			return p
		}
		for _, h := range p.Hosts {
			if strings.ToLower(h) == key {
				return p
			}
		}
	}
	return nil
}

func (c *CMS) scoped(q url.Values) *CMSProject {
	if h := q.Get("host"); h != "" {
		return c.find(h)
	}
	return c.find(q.Get("site"))
}

func (c *CMS) resolve(w http.ResponseWriter, q url.Values) {
	p := c.scoped(q)
	if p == nil {
		cmsErr(w, 404, "unknown host", "host_not_found")
		return
	}
	host := strings.ToLower(q.Get("host"))
	if host == "" {
		host = p.Hosts[0]
	}
	path := q.Get("path")
	if path == "" {
		path = "/"
	}
	locale := q.Get("locale")
	rest := path
	for _, l := range p.Locales {
		if path == "/"+l || strings.HasPrefix(path, "/"+l+"/") {
			if locale == "" {
				locale = l
			}
			rest = strings.TrimPrefix(path, "/"+l)
			if rest == "" {
				rest = "/"
			}
			break
		}
	}
	if locale == "" {
		locale = p.DefaultLocale
	}
	res := cms.Resolution{
		Status: 200, Locale: locale, Path: path,
		Site: cms.Site{ID: p.ID, Slug: p.Slug, Name: p.Name, DefaultLocale: p.DefaultLocale, Locales: p.Locales, Host: host},
	}
	finish := func() { cmsJSON(w, 200, res) }

	for _, rd := range p.Redirects {
		if rd.From == rest || rd.From == path {
			res.Kind, res.Status = cms.KindRedirect, rd.Status
			res.Redirect = &cms.Redirect{To: rd.To, Status: rd.Status}
			finish()
			return
		}
	}
	for _, e := range p.Entries {
		if !e.Published {
			continue
		}
		pat := p.TypePatterns[e.Type]
		if pat == "" {
			continue
		}
		want := strings.ReplaceAll(pat, "{slug}", e.Slug)
		if e.Type == "page" && e.Slug == "home" {
			want = "/"
		}
		if want == rest {
			res.Kind = cms.KindEntry
			ce := e.view(locale)
			res.Entry = &ce
			res.SEO = p.seo(host, path, locale, e.title(locale), e.desc(locale))
			finish()
			return
		}
	}
	for app, routes := range p.Routes {
		for _, rt := range routes {
			params, ok := matchPattern(rt.Pattern, rest)
			if !ok {
				continue
			}
			res.Kind = cms.KindRoute
			route := &cms.Route{App: app, Pattern: rt.Pattern, Name: rt.Name, Locales: rt.Locales, SEO: rt.SEO, Params: params}
			for _, b := range p.Bindings {
				if b.App == app {
					route.AppRef, route.Role, route.MountPath, route.RenderURL = b.AppRef, b.Role, b.MountPath, b.RenderURL
				}
			}
			res.Route = route
			res.SEO = p.seo(host, path, locale, rt.Name, "")
			finish()
			return
		}
	}
	res.Kind, res.Status = cms.KindNotFound, 404
	finish()
}

func (p *CMSProject) seo(host, path, locale, title, desc string) *cms.SEO {
	s := &cms.SEO{
		Title: title, Description: desc, Canonical: "https://" + host + path, Robots: "index,follow",
		OG: cms.OpenGraph{Title: title, Description: desc, Type: "website", Locale: locale, SiteName: p.Name.Get(locale)},
	}
	base := path
	for _, l := range p.Locales {
		if base == "/"+l || strings.HasPrefix(base, "/"+l+"/") {
			base = strings.TrimPrefix(base, "/"+l)
			if base == "" {
				base = "/"
			}
		}
	}
	for _, l := range p.Locales {
		href := "https://" + host + base
		if l != p.DefaultLocale {
			href = "https://" + host + "/" + l + strings.TrimSuffix(base, "/")
		}
		s.Alternates = append(s.Alternates, cms.Alternate{Locale: l, Href: href})
	}
	s.Alternates = append(s.Alternates, cms.Alternate{Locale: "x-default", Href: "https://" + host + base})
	return s
}

func (e *CMSEntry) val(key, locale string) any {
	switch v := e.Fields[key].(type) {
	case cms.Localized:
		return v.Get(locale)
	case map[string]string:
		return cms.Localized(v).Get(locale)
	default:
		return v
	}
}

func (e *CMSEntry) title(locale string) string { s, _ := e.val("title", locale).(string); return s }
func (e *CMSEntry) desc(locale string) string {
	s, _ := e.val("description", locale).(string)
	return s
}

func (e *CMSEntry) view(locale string) cms.Entry {
	now := e.UpdatedAt
	out := cms.Entry{ID: e.ID, Type: e.Type, Slug: e.Slug, Status: "published", PublishedAt: &now, UpdatedAt: &now,
		Fields: map[string]any{}, Localized: map[string]cms.Localized{}}
	for k := range e.Fields {
		out.Fields[k] = e.val(k, locale)
		switch v := e.Fields[k].(type) {
		case cms.Localized:
			out.Localized[k] = v
		case map[string]string:
			out.Localized[k] = cms.Localized(v)
		}
	}
	return out
}

func (c *CMS) entries(w http.ResponseWriter, q url.Values) {
	p := c.scoped(q)
	if p == nil {
		cmsErr(w, 404, "unknown project", "project_not_found")
		return
	}
	locale := q.Get("locale")
	if locale == "" {
		locale = p.DefaultLocale
	}
	var all []cms.Entry
	for _, e := range p.Entries {
		if e.Published && (q.Get("type") == "" || q.Get("type") == e.Type) {
			all = append(all, e.view(locale))
		}
	}
	total := len(all)
	off, _ := strconv.Atoi(q.Get("offset"))
	lim, _ := strconv.Atoi(q.Get("limit"))
	if off > len(all) {
		off = len(all)
	}
	all = all[off:]
	if lim > 0 && lim < len(all) {
		all = all[:lim]
	}
	if all == nil {
		all = []cms.Entry{}
	}
	cmsJSON(w, 200, map[string]any{"items": all, "total": total})
}

func (c *CMS) entry(w http.ResponseWriter, q url.Values, rest string) {
	p := c.scoped(q)
	typ, slug, _ := strings.Cut(rest, "/")
	if p != nil {
		locale := q.Get("locale")
		if locale == "" {
			locale = p.DefaultLocale
		}
		for _, e := range p.Entries {
			if e.Published && e.Type == typ && e.Slug == slug {
				cmsJSON(w, 200, e.view(locale))
				return
			}
		}
	}
	cmsErr(w, 404, "entry not found", "entry_not_found")
}

func (c *CMS) menu(w http.ResponseWriter, q url.Values, key string) {
	p := c.scoped(q)
	if p != nil {
		if items, ok := p.Menus[key]; ok {
			locale := q.Get("locale")
			cmsJSON(w, 200, map[string]any{"key": key, "items": flattenMenu(items, locale)})
			return
		}
	}
	cmsErr(w, 404, "menu not found", "menu_not_found")
}

// flattenMenu keeps the {en,ar} labels, or one plain string for a locale.
func flattenMenu(items []cms.MenuItem, locale string) []map[string]any {
	out := []map[string]any{}
	for _, it := range items {
		m := map[string]any{"label": any(it.Label), "url": it.URL}
		if locale != "" {
			m["label"] = it.Label.Get(locale)
		}
		if len(it.Children) > 0 {
			m["children"] = flattenMenu(it.Children, locale)
		}
		out = append(out, m)
	}
	return out
}

// matchPattern matches /products/{slug} against a path.
func matchPattern(pattern, path string) (map[string]string, bool) {
	ps := strings.Split(strings.Trim(pattern, "/"), "/")
	xs := strings.Split(strings.Trim(path, "/"), "/")
	if len(ps) != len(xs) {
		return nil, false
	}
	params := map[string]string{}
	for i, s := range ps {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			if xs[i] == "" {
				return nil, false
			}
			params[s[1:len(s)-1]] = xs[i]
		} else if s != xs[i] {
			return nil, false
		}
	}
	return params, true
}

func cmsBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		cmsErr(w, 400, "invalid body", "invalid")
		return false
	}
	return true
}

func cmsJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func cmsErr(w http.ResponseWriter, status int, msg, code string) {
	cmsJSON(w, status, map[string]string{"error": msg, "errorAr": msg, "code": code})
}
