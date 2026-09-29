package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
	"github.com/circlexo/circlexo-go/manifest"
	"github.com/circlexo/circlexo-go/mcp"
)

func TestMiddlewareAndPolicy(t *testing.T) {
	h := circlexotest.New(t, "demo")
	meta := "https://demo.example/.well-known/oauth-protected-resource"
	var caller *mcp.Caller
	srv := mcp.Middleware(mcp.Options{Verifier: circlexo.NewVerifier(h.Config()), ResourceMetadataURL: meta})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { caller = mcp.FromContext(r.Context()) }))

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `resource_metadata="`+meta+`"`) {
		t.Fatalf("anonymous = %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}

	// A gateway-exchanged token for an agent acting for the user.
	tok := h.AccessToken(map[string]any{"scope": "org mcp:read mcp:write", "agent_id": "a1",
		"act": map[string]any{"sub": "agent:a1", "agent_id": "a1", "act": map[string]any{"sub": "cxo_mcp_gateway"}}})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 200 || caller == nil || caller.UserID != "user-1" || caller.AgentID != "a1" || strings.Join(caller.Actors(), ">") != "agent:a1>cxo_mcp_gateway" {
		t.Fatalf("caller = %d %+v", w.Code, caller)
	}

	m := &manifest.Manifest{MCP: &manifest.MCP{Tools: []manifest.Tool{
		{Name: "list_things", Effect: manifest.EffectRead}, {Name: "add_thing", Effect: manifest.EffectWrite}, {Name: "drop_all", Effect: manifest.EffectDestructive},
	}}}
	p := mcp.PolicyFromManifest(m, map[string]string{"read": "mcp:read", "write": "mcp:write", "destructive": "mcp:destructive"})
	ctx := t.Context()
	if got := strings.Join(p.Visible(ctx, caller, []string{"list_things", "add_thing", "drop_all", "unknown"}), ","); got != "list_things,add_thing" {
		t.Fatalf("visible = %s", got)
	}
	readOnly := true
	p.ReadOnly = func(context.Context, *mcp.Caller) bool { return readOnly }
	if p.Allowed(ctx, caller, "add_thing") || !p.Allowed(ctx, caller, "list_things") {
		t.Fatal("read-only not applied")
	}

	w = httptest.NewRecorder()
	mcp.ProtectedResource("https://demo.example/mcp", h.URL, []string{"mcp:read"}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	var doc struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
	}
	if json.Unmarshal(w.Body.Bytes(), &doc) != nil || doc.Resource != "https://demo.example/mcp" || doc.Servers[0] != h.URL {
		t.Fatalf("metadata = %s", w.Body)
	}
}
