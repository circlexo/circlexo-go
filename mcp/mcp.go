// Package mcp secures a product's MCP endpoint for the CircleXO MCP gateway
// and direct clients: it verifies the access token (usually exchanged by the
// gateway, RFC 8693, with the gateway or an agent as actor), serves the OAuth
// protected resource metadata (RFC 9728) that points clients at the hub, and
// decides per tool whether the caller's scopes allow it.
package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/manifest"
)

// Caller is who is calling a tool.
type Caller struct {
	UserID  string
	OrgID   string
	OrgRole string
	Scopes  []string
	// AgentID is set when an agent calls on the user's behalf.
	AgentID string
	// Via is the act chain (the gateway, then any agent). Log it with the
	// user in the product's audit trail.
	Via    *circlexo.Actor
	Claims *circlexo.Claims
}

// HasScope reports whether the caller was granted scope.
func (c *Caller) HasScope(scope string) bool {
	for _, s := range c.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Actors lists the act chain's subjects, outermost first.
func (c *Caller) Actors() []string {
	var out []string
	for a := c.Via; a != nil; a = a.Actor {
		out = append(out, a.Subject)
	}
	return out
}

type ctxKey struct{}

// FromContext returns the Caller put in ctx by Middleware, or nil.
func FromContext(ctx context.Context) *Caller { c, _ := ctx.Value(ctxKey{}).(*Caller); return c }

// NewContext returns ctx carrying c.
func NewContext(ctx context.Context, c *Caller) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// Options configures Middleware.
type Options struct {
	Verifier *circlexo.Verifier
	// ResourceMetadataURL is where ProtectedResource is served, for the
	// WWW-Authenticate challenge (e.g. https://app.example/.well-known/oauth-protected-resource).
	ResourceMetadataURL string
}

// Middleware requires a valid hub access token for the app and puts the
// Caller in the context. Unauthenticated requests get a 401 whose challenge
// names the resource metadata, so MCP clients can discover the hub.
func Middleware(o Options) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			tok := ""
			if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
				tok = strings.TrimSpace(h[7:])
			}
			c, err := o.Verifier.VerifyAccessToken(r.Context(), tok)
			if err != nil {
				ch := `Bearer realm="circlexo"`
				if tok != "" {
					ch += `, error="invalid_token"`
				}
				if o.ResourceMetadataURL != "" {
					ch += `, resource_metadata="` + o.ResourceMetadataURL + `"`
				}
				w.Header().Set("WWW-Authenticate", ch)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			caller := &Caller{UserID: c.Subject, OrgID: c.OrgID, OrgRole: c.OrgRole, Scopes: c.Scopes(), AgentID: c.AgentID, Via: c.Actor, Claims: c}
			next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), caller)))
		})
	}
}

// ProtectedResource serves RFC 9728 metadata for the MCP endpoint at
// resource, naming the hub issuer as its authorization server.
func ProtectedResource(resource, issuer string, scopes []string) http.Handler {
	body, _ := json.Marshal(map[string]any{
		"resource": resource, "authorization_servers": []string{issuer}, "scopes_supported": scopes,
		"bearer_methods_supported": []string{"header"},
	})
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	})
}

// Policy decides which tools a caller may use. Tools maps a tool name to
// its effect (read, write, destructive); EffectScopes maps an effect to the
// scope it needs. An effect with no scope is open to any verified caller
// (the gateway already filters the catalogue by the org's grants).
type Policy struct {
	Tools        map[string]string
	EffectScopes map[string]string
	// ReadOnly, if set, is asked per call; while it returns true (e.g.
	// entitlements are in their read-only grace) only read tools run.
	ReadOnly func(ctx context.Context, c *Caller) bool
}

// PolicyFromManifest takes the tools and their effects from the app
// manifest.
func PolicyFromManifest(m *manifest.Manifest, effectScopes map[string]string) *Policy {
	p := &Policy{Tools: map[string]string{}, EffectScopes: effectScopes}
	if m != nil && m.MCP != nil {
		for _, t := range m.MCP.Tools {
			p.Tools[t.Name] = t.Effect
		}
	}
	return p
}

// Allowed reports whether c may call tool. Unknown tools are refused.
func (p *Policy) Allowed(ctx context.Context, c *Caller, tool string) bool {
	if c == nil {
		return false
	}
	effect, ok := p.Tools[tool]
	if !ok {
		return false
	}
	if effect != manifest.EffectRead && p.ReadOnly != nil && p.ReadOnly(ctx, c) {
		return false
	}
	if s := p.EffectScopes[effect]; s != "" && !c.HasScope(s) {
		return false
	}
	return true
}

// Visible filters tool names to those c may call, for tools/list.
func (p *Policy) Visible(ctx context.Context, c *Caller, tools []string) []string {
	var out []string
	for _, t := range tools {
		if p.Allowed(ctx, c, t) {
			out = append(out, t)
		}
	}
	return out
}
