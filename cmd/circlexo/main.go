// Command circlexo is the CircleXO developer CLI.
//
//	circlexo app validate [--dev] [path ...]   validate circlexo.app.yaml files
//	circlexo app schema                        print the manifest JSON Schema
//
// A path may be a manifest file or a directory holding circlexo.app.yaml; with
// no path the current directory is used. --dev accepts localhost URLs.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/circlexo/circlexo-go/manifest"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage:
  circlexo app validate [--dev] [path ...]
  circlexo app schema
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "app" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[1] {
	case "schema":
		stdout.Write(manifest.Schema())
		return 0
	case "validate":
		fs := flag.NewFlagSet("validate", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dev := fs.Bool("dev", false, "accept localhost URLs (a development manifest)")
		if err := fs.Parse(args[2:]); err != nil {
			return 2
		}
		paths := fs.Args()
		if len(paths) == 0 {
			paths = []string{"."}
		}
		code := 0
		for _, p := range paths {
			if !validate(p, manifest.Options{AllowLocal: *dev}, stdout, stderr) {
				code = 1
			}
		}
		return code
	}
	fmt.Fprint(stderr, usage)
	return 2
}

func validate(path string, o manifest.Options, stdout, stderr io.Writer) bool {
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		path = filepath.Join(path, manifest.FileName)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "✗ %s: %v\n", path, err)
		return false
	}
	m, err := manifest.Parse(data, o)
	var me *manifest.Error
	switch {
	case errors.As(err, &me):
		fmt.Fprintf(stderr, "✗ %s: %d problem(s)\n", path, len(me.Problems))
		for _, p := range me.Problems {
			fmt.Fprintf(stderr, "    %s\n", p)
		}
		return false
	case err != nil:
		fmt.Fprintf(stderr, "✗ %s: %v\n", path, err)
		return false
	}
	tools := 0
	if m.MCP != nil {
		tools = len(m.MCP.Tools)
	}
	fmt.Fprintf(stdout, "✓ %s: %s@%s by %s, %d MCP tools (%d destructive)\n", path, m.ID, m.Version, m.Publisher,
		tools, len(m.Tools(manifest.EffectDestructive)))
	return true
}
