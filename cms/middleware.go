package cms

import (
	"context"
	"net"
	"net/http"
	"strings"
)

type ctxKey int

const resolutionKey ctxKey = 0

// FromContext returns the Resolution HostMiddleware stored, or nil.
func FromContext(ctx context.Context) *Resolution {
	r, _ := ctx.Value(resolutionKey).(*Resolution)
	return r
}

// WithResolution returns ctx carrying r.
func WithResolution(ctx context.Context, r *Resolution) context.Context {
	return context.WithValue(ctx, resolutionKey, r)
}

// Params returns the matched route's parameters (route.params), or nil.
func Params(ctx context.Context) map[string]string {
	if r := FromContext(ctx); r != nil && r.Route != nil {
		return r.Route.Params
	}
	return nil
}

// AppRef returns the app's own tenant id for the project (route.app_ref).
func AppRef(ctx context.Context) string {
	if r := FromContext(ctx); r != nil && r.Route != nil {
		return r.Route.AppRef
	}
	return ""
}

// Options tunes HostMiddleware.
type Options struct {
	// Redirects makes the middleware answer redirect resolutions itself
	// (301/302, or 410 Gone) instead of passing them to the handler.
	Redirects bool
	// OnError handles a failed Resolve. Nil means the request continues
	// without a Resolution (the handler sees FromContext == nil).
	OnError func(w http.ResponseWriter, r *http.Request, err error)
}

// HostMiddleware resolves each request's host and path and puts the
// Resolution in the request context (FromContext, Params, AppRef). app is the
// calling app's id; fn, when non-nil, may enrich the context from the
// resolution, for example by loading the tenant for AppRef(ctx), and returns
// the context to continue with (nil keeps it). The host is X-Forwarded-Host
// when present, else Host, without the port.
func HostMiddleware(rs Resolver, app string, fn func(ctx context.Context, res *Resolution) context.Context, opts ...Options) func(http.Handler) http.Handler {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			res, err := rs.Resolve(r.Context(), RequestHost(r), r.URL.Path, "")
			if err != nil {
				if o.OnError != nil {
					o.OnError(w, r, err)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if o.Redirects && res.IsRedirect() {
				if res.Redirect.Status == http.StatusGone {
					http.Error(w, "gone", http.StatusGone)
					return
				}
				code := res.Redirect.Status
				if code < 300 || code > 399 {
					code = http.StatusMovedPermanently
				}
				http.Redirect(w, r, res.Redirect.To, code)
				return
			}
			ctx := WithResolution(r.Context(), res)
			if fn != nil {
				if c := fn(ctx, res); c != nil {
					ctx = c
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestHost is the host a request was addressed to, without the port.
func RequestHost(r *http.Request) string {
	h := r.Header.Get("X-Forwarded-Host")
	if i := strings.IndexByte(h, ','); i >= 0 {
		h = h[:i]
	}
	h = strings.TrimSpace(h)
	if h == "" {
		h = r.Host
	}
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.ToLower(h)
}
