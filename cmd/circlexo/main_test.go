package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateCLI(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"app", "validate", "../../manifest/testdata/mahaam", "../../manifest/testdata/zekra"}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if strings.Count(out.String(), "✓") != 2 {
		t.Fatalf("out = %s", out.String())
	}
	bad := filepath.Join(t.TempDir(), "circlexo.app.yaml")
	os.WriteFile(bad, []byte("manifest_version: 1\nid: x\n"), 0o600)
	out.Reset()
	errb.Reset()
	if code := run([]string{"app", "validate", filepath.Dir(bad)}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "/id:") {
		t.Fatalf("bad: code %d %s", code, errb.String())
	}
	if code := run([]string{"app", "schema"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "circlexo.app.v1.json") {
		t.Fatal("schema")
	}
	if code := run(nil, &out, &errb); code != 2 {
		t.Fatal("usage")
	}
}
