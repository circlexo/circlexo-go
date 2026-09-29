package manifest_test

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/circlexo/circlexo-go/manifest"
)

var testdata = os.DirFS("testdata")

func TestFirstPartyManifestsValidate(t *testing.T) {
	files, err := fs.Glob(testdata, "*/circlexo.app.yaml")
	if err != nil || len(files) < 2 {
		t.Fatalf("manifests: %v %v", files, err)
	}
	for _, f := range files {
		data, _ := fs.ReadFile(testdata, f)
		m, err := manifest.Parse(data, manifest.Options{})
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if dir := strings.Split(f, "/")[0]; m.ID != dir || m.Publisher != "3x1" {
			t.Fatalf("%s: id %q publisher %q", f, m.ID, m.Publisher)
		}
		if len(m.Tools(manifest.EffectDestructive)) == 0 || m.Name.AR == "" {
			t.Fatalf("%s: %+v", f, m)
		}
	}
}

const good = `
manifest_version: 1
id: sample
publisher: acme
version: 0.1.0
name: { en: Sample }
category: productivity
urls: { home: https://sample.example, launch: https://sample.example/sso }
auth:
  redirect_uris: [https://sample.example/cb]
  scopes: [openid, org]
mcp:
  url: https://sample.example/mcp
  transport: streamable-http
  auth: token-exchange
  tools:
    - { name: read_things, effect: read }
    - { name: drop_things, effect: destructive }
`

func problems(t *testing.T, src string, o manifest.Options) []string {
	t.Helper()
	_, err := manifest.Parse([]byte(src), o)
	var me *manifest.Error
	if !errors.As(err, &me) {
		t.Fatalf("want *manifest.Error, got %v", err)
	}
	out := make([]string, len(me.Problems))
	for i, p := range me.Problems {
		out[i] = p.String()
	}
	return out
}

func has(ps []string, path, text string) bool {
	for _, p := range ps {
		if strings.HasPrefix(p, path+": ") && strings.Contains(p, text) {
			return true
		}
	}
	return false
}

func TestGoodSample(t *testing.T) {
	m, err := manifest.Parse([]byte(good), manifest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Tools(manifest.EffectDestructive); len(got) != 1 || got[0] != "drop_things" {
		t.Fatalf("destructive = %v", got)
	}
	// JSON is YAML too.
	if _, err := manifest.Parse([]byte(`{"manifest_version":1,"id":"j","publisher":"acme","version":"1.0.0","name":{"en":"J"},"category":"ai",
		"urls":{"home":"https://j.example","launch":"https://j.example"},"auth":{"redirect_uris":["https://j.example/cb"],"scopes":["openid"]}}`),
		manifest.Options{}); err == nil || !strings.Contains(err.Error(), "/id") {
		t.Fatalf("one-letter id: %v", err)
	}
}

func TestSchemaProblems(t *testing.T) {
	src := strings.NewReplacer(
		"version: 0.1.0", "version: v1",
		"effect: destructive", "effect: nuke",
		"scopes: [openid, org]", "scopes: [org]",
	).Replace(good) + "client_secret: shh\n"
	ps := problems(t, src, manifest.Options{})
	for _, want := range [][2]string{
		{"/version", "semantic version"},
		{"/mcp/tools/1/effect", "must be one of"},
		{"/auth/scopes", "openid"},
		{"", "client_secret"},
	} {
		if !has(ps, want[0], want[1]) && !(want[0] == "" && strings.Contains(strings.Join(ps, "\n"), want[1])) {
			t.Errorf("missing %s %q in:\n%s", want[0], want[1], strings.Join(ps, "\n"))
		}
	}
	if _, err := manifest.Parse([]byte("id: [unclosed"), manifest.Options{}); err == nil || !strings.Contains(err.Error(), "YAML") {
		t.Fatalf("bad yaml: %v", err)
	}
	if ps := problems(t, "", manifest.Options{}); len(ps) != 1 {
		t.Fatalf("empty: %v", ps)
	}
}

func TestContractRules(t *testing.T) {
	src := strings.NewReplacer(
		"launch: https://sample.example/sso", "launch: http://sample.example/sso",
		"redirect_uris: [https://sample.example/cb]", "redirect_uris: [\"https://sample.example/cb?token=abc\", \"https://u:p@sample.example/cb\"]",
		"name: read_things", "name: drop_things",
		"auth: token-exchange", "auth: none",
	).Replace(good) + `lifecycle: { provision: { url: https://sample.example/p } }
entitlements: { features: [seats], meters: [seats] }
listing: { default_locale: en, locales: [en, ar] }
`
	ps := problems(t, src, manifest.Options{})
	for _, want := range [][2]string{
		{"/urls/launch", "must use https"},
		{"/auth/redirect_uris/0", "secret in its query"},
		{"/auth/redirect_uris/1", "credentials"},
		{"/mcp/tools/1/name", "already declared"},
		{"/mcp/auth", "needs auth"},
		{"/lifecycle", "go together"},
		{"/entitlements/features/0", "both a feature and a meter"},
		{"/name/ar", "Arabic name"},
	} {
		if !has(ps, want[0], want[1]) {
			t.Errorf("missing %s %q in:\n%s", want[0], want[1], strings.Join(ps, "\n"))
		}
	}
}

func TestLocalURLs(t *testing.T) {
	src := strings.ReplaceAll(good, "https://sample.example", "http://localhost:3000")
	if ps := problems(t, src, manifest.Options{}); !has(ps, "/urls/home", "development manifest") {
		t.Fatalf("local in prod: %v", ps)
	}
	if _, err := manifest.Parse([]byte(src), manifest.Options{AllowLocal: true}); err != nil {
		t.Fatalf("dev: %v", err)
	}
	if ps := problems(t, strings.ReplaceAll(good, "https://sample.example", "http://127.0.0.1"), manifest.Options{}); len(ps) == 0 {
		t.Fatal("loopback allowed")
	}
}
