// Package manifest reads and validates the CircleXO App Contract manifest,
// circlexo.app.yaml (MH-792, design §5). The JSON Schema
// (circlexo.app.v1.schema.json, embedded) checks the shape; Validate adds the
// contract rules a schema cannot express. Apps, the Publisher Console and the
// `circlexo app validate` CLI all use this package, so a manifest means the
// same thing everywhere.
package manifest

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// FileName is the manifest's conventional name at an app's repository root.
const FileName = "circlexo.app.yaml"

// SchemaID is the $id of the v1 schema.
const SchemaID = "https://circlexo.com/schema/circlexo.app.v1.json"

//go:embed circlexo.app.v1.schema.json
var schemaJSON []byte

// Schema returns the v1 JSON Schema, for editors and the console.
func Schema() []byte { return bytes.Clone(schemaJSON) }

// Manifest is a validated circlexo.app.yaml.
type Manifest struct {
	ManifestVersion int           `json:"manifest_version"`
	ID              string        `json:"id"`
	Publisher       string        `json:"publisher"`
	Version         string        `json:"version"`
	Name            Localized     `json:"name"`
	Description     *Localized    `json:"description,omitempty"`
	Category        string        `json:"category"`
	URLs            URLs          `json:"urls"`
	Auth            Auth          `json:"auth"`
	Lifecycle       *Lifecycle    `json:"lifecycle,omitempty"`
	Webhooks        *Webhooks     `json:"webhooks,omitempty"`
	Entitlements    *Entitlements `json:"entitlements,omitempty"`
	MCP             *MCP          `json:"mcp,omitempty"`
	Listing         *Listing      `json:"listing,omitempty"`
}

type Localized struct {
	EN string `json:"en"`
	AR string `json:"ar,omitempty"`
}

type URLs struct {
	Home    string `json:"home"`
	Launch  string `json:"launch"`
	Support string `json:"support,omitempty"`
	Docs    string `json:"docs,omitempty"`
	Privacy string `json:"privacy,omitempty"`
	Terms   string `json:"terms,omitempty"`
}

type Auth struct {
	RedirectURIs           []string `json:"redirect_uris"`
	PostLogoutRedirectURIs []string `json:"post_logout_redirect_uris,omitempty"`
	Scopes                 []string `json:"scopes"`
	ServiceAccount         bool     `json:"service_account,omitempty"`
	TokenSigningAlg        string   `json:"token_signing_alg,omitempty"`
	JWKSURI                string   `json:"jwks_uri,omitempty"`
	JWKS                   *JWKS    `json:"jwks,omitempty"`
	RequirePAR             bool     `json:"require_par,omitempty"`
}

// JWKS is a public key set inline in the manifest, for private_key_jwt.
type JWKS struct {
	Keys []map[string]any `json:"keys"`
}

type Endpoint struct {
	URL string `json:"url"`
}

type Lifecycle struct {
	Provision   *Endpoint `json:"provision,omitempty"`
	Deprovision *Endpoint `json:"deprovision,omitempty"`
	Health      *Endpoint `json:"health,omitempty"`
}

type Webhooks struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

type Entitlements struct {
	Features []string `json:"features,omitempty"`
	Meters   []string `json:"meters,omitempty"`
}

type MCP struct {
	URL       string `json:"url"`
	Transport string `json:"transport"`
	Auth      string `json:"auth"`
	Tools     []Tool `json:"tools"`
}

// Tool effects. Destructive tools need an approval before the MCP gateway runs them.
const (
	EffectRead        = "read"
	EffectWrite       = "write"
	EffectDestructive = "destructive"
)

type Tool struct {
	Name        string `json:"name"`
	Effect      string `json:"effect"`
	Description string `json:"description,omitempty"`
}

type Listing struct {
	DefaultLocale string   `json:"default_locale,omitempty"`
	Locales       []string `json:"locales,omitempty"`
}

// Problem is one thing wrong with a manifest, at a JSON pointer ("/mcp/tools/2/effect").
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	path := p.Path
	if path == "" {
		path = "/"
	}
	return path + ": " + p.Message
}

// Error lists every problem found.
type Error struct{ Problems []Problem }

func (e *Error) Error() string {
	s := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		s[i] = p.String()
	}
	return "invalid manifest:\n  " + strings.Join(s, "\n  ")
}

// Options relax rules for development.
type Options struct {
	// AllowLocal accepts http://localhost and loopback URLs (a manifest for a
	// dev hub). Store listings never allow them.
	AllowLocal bool
}

var (
	compileOnce sync.Once
	compiled    *jsonschema.Schema
	compileErr  error
)

func schema() (*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
		if err != nil {
			compileErr = err
			return
		}
		c := jsonschema.NewCompiler()
		c.AssertFormat()
		if compileErr = c.AddResource(SchemaID, doc); compileErr != nil {
			return
		}
		compiled, compileErr = c.Compile(SchemaID)
	})
	return compiled, compileErr
}

// Parse reads a manifest (YAML or JSON) and validates it. The error is an
// *Error listing every problem when the manifest is readable but invalid.
func Parse(data []byte, o Options) (*Manifest, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("manifest is not valid YAML: %w", err)
	}
	if doc == nil {
		return nil, &Error{Problems: []Problem{{Message: "the manifest is empty"}}}
	}
	// Round-trip through JSON so the schema sees JSON types (yaml gives ints and maps of any).
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	sch, err := schema()
	if err != nil {
		return nil, fmt.Errorf("manifest schema: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		var ve *jsonschema.ValidationError
		if !errors.As(err, &ve) {
			return nil, err
		}
		return nil, &Error{Problems: schemaProblems(ve)}
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if ps := m.check(o); len(ps) > 0 {
		return nil, &Error{Problems: ps}
	}
	return &m, nil
}

func schemaProblems(ve *jsonschema.ValidationError) []Problem {
	var out []Problem
	seen := map[string]bool{}
	var walk func(u jsonschema.OutputUnit)
	walk = func(u jsonschema.OutputUnit) {
		// The items tried against "contains" are not problems themselves.
		if strings.Contains(u.KeywordLocation, "/contains/") {
			return
		}
		if u.Error != nil && len(u.Errors) == 0 {
			p := Problem{Path: u.InstanceLocation, Message: u.Error.String()}
			switch {
			case strings.HasSuffix(u.KeywordLocation, "/contains"):
				p.Message = "must include openid"
			case u.InstanceLocation == "/version" && strings.HasSuffix(u.KeywordLocation, "/pattern"):
				p.Message = "must be a semantic version, like 1.2.0"
			}
			if k := p.String(); !seen[k] {
				seen[k] = true
				out = append(out, p)
			}
		}
		for _, c := range u.Errors {
			walk(c)
		}
	}
	walk(*ve.BasicOutput())
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// secretParams are query parameters that would put a credential in a URL.
var secretParams = []string{"token", "access_token", "secret", "client_secret", "key", "api_key", "apikey", "password", "sig", "signature"}

// check applies the contract rules the schema cannot express.
func (m *Manifest) check(o Options) []Problem {
	var ps []Problem
	add := func(path, format string, args ...any) {
		ps = append(ps, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
	}
	checkURL := func(path, raw string) {
		if raw == "" {
			return
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			add(path, "not an absolute URL")
			return
		}
		if u.User != nil {
			add(path, "must not contain credentials")
		}
		for k := range u.Query() {
			for _, s := range secretParams {
				if strings.EqualFold(k, s) {
					add(path, "must not carry a secret in its query (%s)", k)
				}
			}
		}
		if u.Fragment != "" {
			add(path, "must not have a fragment")
		}
		local := isLocal(u.Hostname())
		switch {
		case u.Scheme == "https" && (!local || o.AllowLocal):
		case u.Scheme == "http" && local && o.AllowLocal:
		case local:
			add(path, "localhost URLs are only allowed in a development manifest")
		default:
			add(path, "must use https")
		}
	}

	checkURL("/urls/home", m.URLs.Home)
	checkURL("/urls/launch", m.URLs.Launch)
	checkURL("/urls/support", m.URLs.Support)
	checkURL("/urls/docs", m.URLs.Docs)
	checkURL("/urls/privacy", m.URLs.Privacy)
	checkURL("/urls/terms", m.URLs.Terms)
	for i, u := range m.Auth.RedirectURIs {
		checkURL(fmt.Sprintf("/auth/redirect_uris/%d", i), u)
	}
	checkURL("/auth/jwks_uri", m.Auth.JWKSURI)
	if m.Auth.JWKSURI != "" && m.Auth.JWKS != nil {
		add("/auth/jwks", "set jwks_uri or jwks, not both")
	}
	for i, u := range m.Auth.PostLogoutRedirectURIs {
		checkURL(fmt.Sprintf("/auth/post_logout_redirect_uris/%d", i), u)
	}
	if l := m.Lifecycle; l != nil {
		for name, e := range map[string]*Endpoint{"provision": l.Provision, "deprovision": l.Deprovision, "health": l.Health} {
			if e != nil {
				checkURL("/lifecycle/"+name+"/url", e.URL)
			}
		}
		if (l.Provision == nil) != (l.Deprovision == nil) {
			add("/lifecycle", "provision and deprovision go together")
		}
	}
	if w := m.Webhooks; w != nil {
		checkURL("/webhooks/url", w.URL)
	}
	if e := m.Entitlements; e != nil {
		meters := map[string]bool{}
		for _, k := range e.Meters {
			meters[k] = true
		}
		for i, k := range e.Features {
			if meters[k] {
				add(fmt.Sprintf("/entitlements/features/%d", i), "%q is both a feature and a meter", k)
			}
		}
	}
	if c := m.MCP; c != nil {
		checkURL("/mcp/url", c.URL)
		seen := map[string]int{}
		for i, t := range c.Tools {
			if j, dup := seen[t.Name]; dup {
				add(fmt.Sprintf("/mcp/tools/%d/name", i), "tool %q is already declared at /mcp/tools/%d", t.Name, j)
			}
			seen[t.Name] = i
		}
		if c.Auth == "none" && m.hasEffect(EffectWrite, EffectDestructive) {
			add("/mcp/auth", "an MCP server with write or destructive tools needs auth")
		}
	}
	if l := m.Listing; l != nil {
		if l.DefaultLocale != "" && len(l.Locales) > 0 && !contains(l.Locales, l.DefaultLocale) {
			add("/listing/default_locale", "%q is not one of the listing locales", l.DefaultLocale)
		}
		if contains(l.Locales, "ar") && m.Name.AR == "" {
			add("/name/ar", "the listing is in Arabic, so the app needs an Arabic name")
		}
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Path < ps[j].Path })
	return ps
}

func (m *Manifest) hasEffect(effects ...string) bool {
	if m.MCP == nil {
		return false
	}
	for _, t := range m.MCP.Tools {
		if contains(effects, t.Effect) {
			return true
		}
	}
	return false
}

// Tools returns the declared MCP tools with the given effect.
func (m *Manifest) Tools(effect string) []string {
	var out []string
	if m.MCP != nil {
		for _, t := range m.MCP.Tools {
			if t.Effect == effect {
				out = append(out, t.Name)
			}
		}
	}
	return out
}

func isLocal(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
