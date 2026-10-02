package cms_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
	"github.com/circlexo/circlexo-go/cms"
)

var ctx = context.Background()

func seed(t *testing.T) (*circlexotest.CMS, *circlexotest.CMSProject, *cms.Client) {
	f := circlexotest.NewCMS(t)
	p := f.AddProject("acme", "acme.circlexo.com", "www.acme.com")
	p.AddEntry("page", "home", map[string]any{"title": cms.Localized{"en": "Home", "ar": "الرئيسية"}})
	p.AddEntry("page", "about", map[string]any{"title": "About"})
	p.AddEntry("post", "hello", map[string]any{"title": "Hello"})
	p.AddDraft("page", "secret", map[string]any{"title": "Secret"})
	p.AddRedirect("/old", "/about", 301)
	p.AddRedirect("/gone", "", 410)
	p.Bind(cms.Binding{App: "matjar", AppRef: "store-9", Role: "storefront", RenderURL: "https://matjar-acme.circlexo.com"})
	p.Route("matjar", cms.RouteRegistration{Pattern: "/products/{slug}", Name: "Product"})
	p.SetMenu("main", cms.MenuItem{Label: cms.Localized{"en": "Home", "ar": "الرئيسية"}, URL: "/", Children: []cms.MenuItem{{Label: cms.Localized{"en": "Blog"}, URL: "/blog"}}})
	return f, p, f.Client()
}

func TestResolve(t *testing.T) {
	_, _, c := seed(t)

	r, err := c.Resolve(ctx, "acme.circlexo.com", "/", "")
	if err != nil || !r.IsEntry() || r.Entry.Title() != "Home" || r.Locale != "en" || r.Site.Slug != "acme" {
		t.Fatalf("home = %+v, %v", r, err)
	}
	if r.SEO == nil || r.SEO.Title != "Home" || r.SEO.Robots != "index,follow" || len(r.SEO.Alternates) != 3 {
		t.Fatalf("seo = %+v", r.SEO)
	}

	r, _ = c.Resolve(ctx, "acme.circlexo.com", "/ar", "")
	if r.Locale != "ar" || r.Entry.Title() != "الرئيسية" || r.Entry.Localized["title"]["en"] != "Home" {
		t.Fatalf("ar home = %+v", r)
	}

	r, _ = c.Resolve(ctx, "acme.circlexo.com", "/blog/hello", "")
	if !r.IsEntry() || r.Entry.Type != "post" {
		t.Fatalf("post = %+v", r)
	}

	r, _ = c.Resolve(ctx, "www.acme.com", "/old", "")
	if !r.IsRedirect() || r.Redirect.To != "/about" || r.Redirect.Status != 301 {
		t.Fatalf("redirect = %+v", r)
	}

	r, _ = c.Resolve(ctx, "acme.circlexo.com", "/products/shoe", "")
	if !r.IsRoute() || r.Route.App != "matjar" || r.Param("slug") != "shoe" || r.Route.AppRef != "store-9" ||
		r.Route.RenderURL != "https://matjar-acme.circlexo.com" || !r.OwnRoute("matjar") || r.OwnRoute("other") {
		t.Fatalf("route = %+v", r.Route)
	}

	for _, path := range []string{"/nope", "/secret"} {
		r, err = c.Resolve(ctx, "acme.circlexo.com", path, "")
		if err != nil || !r.NotFound() || r.Status != 404 {
			t.Fatalf("%s = %+v, %v", path, r, err)
		}
	}

	if _, err = c.Resolve(ctx, "unknown.example", "/", ""); !circlexo.IsStatus(err, 404) {
		t.Fatalf("unknown host err = %v", err)
	}
}

func TestRegisterRoutesAndSitemap(t *testing.T) {
	f, p, c := seed(t)

	n, err := c.RegisterRoutes(ctx, "acme", "mizan", []cms.RouteRegistration{
		{Pattern: "/pricing", Name: "Pricing", Locales: []string{"en", "ar"}, SEO: map[string]any{"changefreq": "daily"}},
		{Pattern: "/plans/{id}"},
	})
	if err != nil || n != 2 || len(p.Routes["mizan"]) != 2 || p.Routes["mizan"][0].SEO["changefreq"] != "daily" {
		t.Fatalf("routes n=%d err=%v got=%+v", n, err, p.Routes["mizan"])
	}
	if r, _ := c.Resolve(ctx, "acme.circlexo.com", "/plans/7", ""); !r.IsRoute() || r.Param("id") != "7" {
		t.Fatalf("registered route did not resolve: %+v", r)
	}

	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	e := []cms.SitemapEntry{{Path: "/products/x", Lastmod: &now, Changefreq: "weekly", Priority: 0.7,
		Title: cms.Localized{"en": "X"}, Alternates: map[string]string{"ar": "/ar/products/x"}}}
	if n, err = c.PushSitemap(ctx, "acme.circlexo.com", "matjar", e, true); err != nil || n != 1 {
		t.Fatalf("sitemap n=%d err=%v", n, err)
	}
	if n, err = c.PushSitemap(ctx, "acme", "matjar", e, true); err != nil || len(p.Sitemap["matjar"]) != 1 {
		t.Fatalf("replace kept old entries: %d %v", len(p.Sitemap["matjar"]), err)
	}
	if _, err = c.PushSitemap(ctx, "acme", "matjar", e, false); err != nil || len(p.Sitemap["matjar"]) != 2 {
		t.Fatalf("append: %d %v", len(p.Sitemap["matjar"]), err)
	}
	if p.Sitemap["matjar"][0].Priority != 0.7 || p.Sitemap["matjar"][0].Alternates["ar"] != "/ar/products/x" {
		t.Fatalf("entry = %+v", p.Sitemap["matjar"][0])
	}

	if _, err = c.RegisterRoutes(ctx, "nope", "matjar", nil); !circlexo.IsStatus(err, 404) {
		t.Fatalf("unknown project err = %v", err)
	}

	// Without the token the app API refuses.
	anon := cms.New(f.URL, nil)
	if _, err = anon.RegisterRoutes(ctx, "acme", "matjar", nil); err == nil {
		t.Fatal("anonymous RegisterRoutes succeeded")
	}
	bad := cms.New(f.URL, circlexo.StaticToken("wrong"))
	if _, err = bad.ListProjects(ctx); !circlexo.IsStatus(err, 401) {
		t.Fatalf("bad token err = %v", err)
	}
	// ...but public reads need none.
	if r, err := anon.Resolve(ctx, "acme.circlexo.com", "/about", ""); err != nil || !r.IsEntry() {
		t.Fatalf("anonymous resolve = %+v, %v", r, err)
	}
}

func TestMenuAndEntries(t *testing.T) {
	_, _, c := seed(t)

	m, err := c.Menu(ctx, cms.ForHost("acme.circlexo.com"), "main", "")
	if err != nil || m.Key != "main" || len(m.Items) != 1 || m.Items[0].Label["ar"] != "الرئيسية" || m.Items[0].Children[0].URL != "/blog" {
		t.Fatalf("menu = %+v, %v", m, err)
	}
	m, err = c.Menu(ctx, cms.InProject("acme"), "main", "ar")
	if err != nil || m.Items[0].Label.Get("ar") != "الرئيسية" {
		t.Fatalf("menu ar = %+v, %v", m, err)
	}
	if _, err = c.Menu(ctx, cms.InProject("acme"), "footer", ""); !circlexo.IsStatus(err, 404) {
		t.Fatalf("missing menu err = %v", err)
	}

	l, err := c.Entries(ctx, cms.ListOpts{Scope: cms.ForHost("acme.circlexo.com")})
	if err != nil || l.Total != 3 || len(l.Items) != 3 {
		t.Fatalf("entries = %+v, %v", l, err)
	}
	l, _ = c.Entries(ctx, cms.ListOpts{Scope: cms.InProject("acme"), Type: "page", Limit: 1, Offset: 1, Locale: "en"})
	if l.Total != 2 || len(l.Items) != 1 || l.Items[0].Slug != "about" {
		t.Fatalf("paged = %+v", l)
	}

	e, err := c.Entry(ctx, cms.ForHost("acme.circlexo.com"), "page", "home", "ar")
	if err != nil || e.Title() != "الرئيسية" {
		t.Fatalf("entry = %+v, %v", e, err)
	}
	for _, slug := range []string{"missing", "secret"} {
		if _, err = c.Entry(ctx, cms.InProject("acme"), "page", slug, ""); !circlexo.IsStatus(err, 404) {
			t.Fatalf("%s err = %v", slug, err)
		}
	}
}

func TestProjects(t *testing.T) {
	f, _, c := seed(t)

	list, err := c.ListProjects(ctx)
	if err != nil || len(list) != 1 || list[0].Slug != "acme" || len(list[0].Domains) != 0 {
		t.Fatalf("list = %+v, %v", list, err)
	}

	np, err := c.CreateProject(ctx, cms.NewProject{Name: "Cafe Nour", Slug: "nour", DefaultLocale: "ar"})
	if err != nil || np.Slug != "nour" || np.DefaultLocale != "ar" || np.Name.Get("en") != "Cafe Nour" || len(np.Domains) != 1 {
		t.Fatalf("create = %+v, %v", np, err)
	}
	if _, err = c.CreateProject(ctx, cms.NewProject{}); !circlexo.IsStatus(err, 400) {
		t.Fatalf("empty name err = %v", err)
	}

	b, err := c.BindApp(ctx, np.ID, cms.Binding{App: "seatfor", AppRef: "t-1", Role: "booking", RenderURL: "https://seatfor.example"})
	if err != nil || b.App != "seatfor" || b.MountPath != "/" || b.Status != "active" {
		t.Fatalf("bind = %+v, %v", b, err)
	}
	if _, err = c.BindApp(ctx, np.ID, cms.Binding{App: "seatfor"}); !circlexo.IsStatus(err, 400) {
		t.Fatalf("bind without app_ref err = %v", err)
	}

	got, err := c.GetProject(ctx, np.ID)
	if err != nil || len(got.Bindings) != 1 || got.Bindings[0].AppRef != "t-1" || len(got.Domains) != 1 || got.Domains[0].Kind != "system" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err = c.GetProject(ctx, "missing"); !circlexo.IsStatus(err, 404) {
		t.Fatalf("get missing err = %v", err)
	}

	f.Lock()
	f.Projects[1].Theme = map[string]any{"primary": "#123456"}
	f.Unlock()
	th, err := c.Theme(ctx, np.ID)
	if err != nil || th["primary"] != "#123456" {
		t.Fatalf("theme = %+v, %v", th, err)
	}
	ds, err := c.Domains(ctx, "acme")
	if err != nil || len(ds) != 2 || !ds[0].IsPrimary || ds[0].Kind != "system" || ds[1].Kind != "custom" {
		t.Fatalf("domains = %+v, %v", ds, err)
	}
}

func TestErrors(t *testing.T) {
	f, _, c := seed(t)
	f.Lock()
	f.Fail = 503
	f.Unlock()
	_, err := c.Resolve(ctx, "acme.circlexo.com", "/", "")
	var ae *circlexo.APIError
	if !circlexo.IsStatus(err, 503) {
		t.Fatalf("err = %v", err)
	}
	_ = ae
}

func TestNewURL(t *testing.T) {
	t.Setenv("CIRCLEXO_CMS_URL", "")
	if c := cms.New("", nil); c.BaseURL != cms.DefaultURL {
		t.Fatalf("default = %q", c.BaseURL)
	}
	t.Setenv("CIRCLEXO_CMS_URL", "https://cms.example/")
	if c := cms.New("", nil); c.BaseURL != "https://cms.example" {
		t.Fatalf("env = %q", c.BaseURL)
	}
	c := cms.NewClient(circlexo.Config{AppKey: "cxa_x"}, nil)
	if tok, _ := c.Tokens.Token(ctx); tok != "cxa_x" {
		t.Fatalf("token = %q", tok)
	}
}

type counting struct {
	n    atomic.Int32
	next cms.Resolver
}

func (c *counting) Resolve(ctx context.Context, h, p, l string) (*cms.Resolution, error) {
	c.n.Add(1)
	return c.next.Resolve(ctx, h, p, l)
}

func TestCached(t *testing.T) {
	_, _, c := seed(t)
	cnt := &counting{next: c}
	cache := cms.Cached(cnt, time.Minute)

	for i := 0; i < 3; i++ {
		if r, err := cache.Resolve(ctx, "acme.circlexo.com", "/about", ""); err != nil || !r.IsEntry() {
			t.Fatalf("%+v %v", r, err)
		}
	}
	if cnt.n.Load() != 1 {
		t.Fatalf("calls = %d, want 1", cnt.n.Load())
	}
	// A different path, locale or host is a different key.
	cache.Resolve(ctx, "acme.circlexo.com", "/", "")
	cache.Resolve(ctx, "acme.circlexo.com", "/about", "ar")
	cache.Resolve(ctx, "www.acme.com", "/about", "")
	if cnt.n.Load() != 4 {
		t.Fatalf("calls = %d, want 4", cnt.n.Load())
	}
	// Errors are not cached.
	for i := 0; i < 2; i++ {
		if _, err := cache.Resolve(ctx, "unknown.example", "/", ""); err == nil {
			t.Fatal("want error")
		}
	}
	if cnt.n.Load() != 6 {
		t.Fatalf("calls = %d, want 6", cnt.n.Load())
	}
	cache.Purge()
	cache.Resolve(ctx, "acme.circlexo.com", "/about", "")
	if cnt.n.Load() != 7 {
		t.Fatalf("after purge calls = %d, want 7", cnt.n.Load())
	}
	// Expiry.
	short := cms.Cached(cnt, 20*time.Millisecond)
	short.Resolve(ctx, "acme.circlexo.com", "/about", "")
	time.Sleep(40 * time.Millisecond)
	short.Resolve(ctx, "acme.circlexo.com", "/about", "")
	if cnt.n.Load() != 9 {
		t.Fatalf("after expiry calls = %d, want 9", cnt.n.Load())
	}
	// ttl 0 disables.
	off := cms.Cached(cnt, 0)
	off.Resolve(ctx, "acme.circlexo.com", "/about", "")
	off.Resolve(ctx, "acme.circlexo.com", "/about", "")
	if cnt.n.Load() != 11 {
		t.Fatalf("disabled calls = %d, want 11", cnt.n.Load())
	}
}

func TestHostMiddleware(t *testing.T) {
	_, _, c := seed(t)

	var seen *cms.Resolution
	var params map[string]string
	var ref string
	h := cms.HostMiddleware(c, "matjar", func(ctx context.Context, res *cms.Resolution) context.Context {
		return context.WithValue(ctx, ctxKey{}, "tenant-"+res.Route.AppRef)
	}, cms.Options{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, params, ref = cms.FromContext(r.Context()), cms.Params(r.Context()), cms.AppRef(r.Context())
		if r.Context().Value(ctxKey{}) != "tenant-store-9" {
			t.Errorf("fn context lost: %v", r.Context().Value(ctxKey{}))
		}
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest("GET", "http://internal/products/shoe", nil)
	req.Host = "acme.circlexo.com:443"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusTeapot || !seen.IsRoute() || params["slug"] != "shoe" || ref != "store-9" {
		t.Fatalf("code=%d seen=%+v params=%v ref=%q", w.Code, seen, params, ref)
	}

	// X-Forwarded-Host wins over Host.
	req = httptest.NewRequest("GET", "http://internal/products/hat", nil)
	req.Header.Set("X-Forwarded-Host", "ACME.circlexo.com, proxy")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if params["slug"] != "hat" {
		t.Fatalf("params = %v", params)
	}
}

type ctxKey struct{}

func TestHostMiddlewareRedirects(t *testing.T) {
	_, _, c := seed(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res := cms.FromContext(r.Context())
		if res == nil || !res.IsRedirect() {
			t.Errorf("handler got %+v", res)
		}
		w.WriteHeader(http.StatusAccepted)
	})

	req := func(path string) *http.Request {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "acme.circlexo.com"
		return r
	}

	// Off by default: the handler decides.
	w := httptest.NewRecorder()
	cms.HostMiddleware(c, "matjar", nil)(next).ServeHTTP(w, req("/old"))
	if w.Code != http.StatusAccepted {
		t.Fatalf("code = %d", w.Code)
	}

	on := cms.HostMiddleware(c, "matjar", nil, cms.Options{Redirects: true})(next)
	w = httptest.NewRecorder()
	on.ServeHTTP(w, req("/old"))
	if w.Code != 301 || w.Header().Get("Location") != "/about" {
		t.Fatalf("redirect = %d %q", w.Code, w.Header().Get("Location"))
	}
	w = httptest.NewRecorder()
	on.ServeHTTP(w, req("/gone"))
	if w.Code != http.StatusGone {
		t.Fatalf("gone = %d", w.Code)
	}
}

func TestHostMiddlewareErrors(t *testing.T) {
	_, _, c := seed(t)
	var got *cms.Resolution = &cms.Resolution{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = cms.FromContext(r.Context()) })
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "unknown.example"

	// Fails open by default.
	cms.HostMiddleware(c, "matjar", nil)(next).ServeHTTP(httptest.NewRecorder(), r)
	if got != nil {
		t.Fatalf("resolution = %+v, want nil", got)
	}
	// OnError can fail closed.
	w := httptest.NewRecorder()
	called := false
	cms.HostMiddleware(c, "matjar", nil, cms.Options{OnError: func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "no site", http.StatusBadGateway)
	}})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway || called {
		t.Fatalf("code=%d called=%v", w.Code, called)
	}
	// Works with the cache as the resolver.
	cms.HostMiddleware(cms.Cached(c, time.Minute), "matjar", nil)(next).ServeHTTP(httptest.NewRecorder(), r)
}

func TestLocalized(t *testing.T) {
	var l cms.Localized
	if err := l.UnmarshalJSON([]byte(`"plain"`)); err != nil || l.Get("ar") != "plain" {
		t.Fatalf("%v %v", l, err)
	}
	l = cms.Localized{"en": "E", "ar": "A"}
	if l.Get("ar") != "A" || l.Get("fr") != "E" {
		t.Fatalf("get = %q %q", l.Get("ar"), l.Get("fr"))
	}
	var nilRes *cms.Resolution
	if !nilRes.NotFound() || nilRes.IsRoute() || nilRes.IsRedirect() || nilRes.Param("x") != "" {
		t.Fatal("nil resolution helpers")
	}
}
