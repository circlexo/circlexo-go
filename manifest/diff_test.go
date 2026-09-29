package manifest_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/circlexo/circlexo-go/manifest"
)

func TestCompareVersions(t *testing.T) {
	order := []string{"0.1.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.2.0", "1.10.0", "2.0.0"}
	for i := range order {
		for j := range order {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := manifest.CompareVersions(order[i], order[j]); got != want {
				t.Errorf("%s vs %s = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	if manifest.CompareVersions("1.0.0+build.5", "1.0.0") != 0 {
		t.Error("build metadata counts")
	}
}

func TestWidening(t *testing.T) {
	prev, err := manifest.Parse([]byte(good), manifest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if w := manifest.Widening(prev, prev); len(w) != 0 {
		t.Fatalf("same = %v", w)
	}
	next, err := manifest.Parse([]byte(strings.NewReplacer(
		"scopes: [openid, org]", "scopes: [openid, org, offline_access]",
		"effect: read }", "effect: destructive }",
	).Replace(good)+"webhooks: { url: https://sample.example/hook, events: [org.*] }\n"), manifest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(manifest.Widening(prev, next))
	if got != "[destructive_tool:read_things scope:offline_access webhook:https://sample.example/hook webhook_event:org.*]" {
		t.Fatalf("widening = %s", got)
	}
	// Narrowing is not widening.
	if w := manifest.Widening(next, prev); len(w) != 0 {
		t.Fatalf("narrowing = %v", w)
	}
	if w := manifest.Widening(nil, prev); len(w) == 0 {
		t.Fatal("first version asks for nothing")
	}
}
