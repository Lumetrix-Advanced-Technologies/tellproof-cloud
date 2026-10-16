// Command ossexport assembles the open-source tree (P6-2, D-050): the evidence
// agent, the offline verifier, the packages they import, the verify page, the
// SDKs, the spec and test vectors, and the slice of the concierge that the
// public tests check against, under Apache-2.0. Nothing of the control plane
// (server, store, evaluator, web app, integrations, migrations) goes in.
//
// The package set is not a hand-kept list: it is what `go list -deps` says
// cmd/agent and cmd/verify import (with every build tag the agent has). Test
// files that import anything outside that set are left out and listed, so a
// private package can never be dragged in by a test.
//
//	go run ./tools/ossexport -out dist/oss [-module github.com/org/repo]
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Roots are the open-source programs.
var roots = []string{"./cmd/agent", "./cmd/verify"}

// buildTags are every tag the open-source programs build with.
var buildTags = "bedrock"

// extra are the non-Go paths that go in as they are (directories recursively).
var extra = []string{
	"web/verify",
	"web/brand.css",
	// The static site's other pages, deployed with the verify page (D-019).
	"web/site.css",
	"web/security",
	"web/docs",
	"web/site_test.go",
	"spec",
	"sdk",
	"examples",
	"agent.example.json",
	".dockerignore",
	// The concierge's reference implementations the public tests compare
	// against: ingest (window roots), schema (batch schema) and the Python
	// verifier. The evaluator stays private.
	"concierge/ingest.py",
	"concierge/merkle.py",
	"concierge/schema.py",
	"concierge/verify.py",
	"concierge/tests/test_verify_keys.py",
	"concierge/testdata/otlp-sample.jsonl",
}

// overlay holds the files only the public repository has (licence, README,
// Makefile, CI and release workflows); it is copied over the tree last.
const overlay = "oss"

// skip are names never copied.
var skip = map[string]bool{"__pycache__": true, "node_modules": true, ".DS_Store": true}

// forbidden must not appear anywhere in the exported tree: names that would
// mean control-plane code or configuration slipped in.
var forbidden = []string{
	"internal/store", "internal/server", "internal/webapp", "internal/issue", "internal/evaluate",
	"internal/vanta", "internal/secretbox", "internal/kmssign", "internal/auth", "internal/share",
	"jackc/pgx", "SUPABASE_", "INTEGRATION_KEY", "DATABASE_URL",
}

func main() {
	out := flag.String("out", "dist/oss", "directory to write the tree to (replaced)")
	module := flag.String("module", "", "module path of the public repository (default: this module's)")
	flag.Parse()
	r, err := export(".", *out, *module)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ossexport:", err)
		os.Exit(1)
	}
	fmt.Printf("ossexport: %d packages, %d files to %s\n", len(r.Packages), r.Files, *out)
	for _, p := range r.Packages {
		fmt.Println("  package", p)
	}
	for _, d := range r.Dropped {
		fmt.Println("  left out (imports private packages):", d)
	}
}

// Result says what was exported.
type Result struct {
	Packages []string // directories, relative to the module root
	Dropped  []string // test files left out
	Files    int
}

func modulePath(src string) (string, error) {
	b, err := os.ReadFile(filepath.Join(src, "go.mod"))
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[0] == "module" {
			return f[1], nil
		}
	}
	return "", errors.New("go.mod has no module line")
}

// packages lists the module's packages the roots depend on, as directories.
func packages(src, mod string) ([]string, error) {
	args := append([]string{"list", "-deps", "-tags", buildTags, "-f", "{{.ImportPath}}"}, roots...)
	cmd := exec.Command("go", args...)
	cmd.Dir = src
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	var dirs []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == mod {
			dirs = append(dirs, ".")
		} else if rel, ok := strings.CutPrefix(line, mod+"/"); ok {
			dirs = append(dirs, rel)
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

func export(src, out, newModule string) (*Result, error) {
	mod, err := modulePath(src)
	if err != nil {
		return nil, err
	}
	if newModule == "" {
		newModule = mod
	}
	dirs, err := packages(src, mod)
	if err != nil {
		return nil, err
	}
	public := map[string]bool{}
	for _, d := range dirs {
		public[mod+"/"+d] = true
	}
	if err := os.RemoveAll(out); err != nil {
		return nil, err
	}
	res := &Result{Packages: dirs}
	c := &copier{src: src, out: out, mod: mod, newMod: newModule, res: res}

	for _, d := range dirs {
		entries, err := os.ReadDir(filepath.Join(src, d))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			name := e.Name()
			rel := filepath.Join(d, name)
			switch {
			case skip[name]:
			case e.IsDir() && name == "testdata":
				if err := c.tree(rel); err != nil {
					return nil, err
				}
			case e.IsDir():
				// Sub-packages are copied only if they are in the set themselves.
			case strings.HasSuffix(name, "_test.go"):
				private, err := privateImports(filepath.Join(src, rel), mod, public)
				if err != nil {
					return nil, err
				}
				if len(private) > 0 {
					res.Dropped = append(res.Dropped, rel+" ("+strings.Join(private, ", ")+")")
					continue
				}
				if err := c.file(rel); err != nil {
					return nil, err
				}
			default:
				if err := c.file(rel); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, p := range append(extra, "go.mod", "go.sum") {
		if err := c.tree(p); err != nil {
			return nil, err
		}
	}
	if err := c.overlay(filepath.Join(src, overlay)); err != nil {
		return nil, err
	}
	// The published SDK packages carry the licence too.
	for _, d := range []string{"sdk/python", "sdk/js"} {
		if err := copyFile(filepath.Join(src, overlay, "LICENSE"), filepath.Join(out, d, "LICENSE")); err != nil {
			return nil, err
		}
	}
	if err := checkClean(out); err != nil {
		return nil, err
	}
	return res, nil
}

// privateImports lists the module packages a Go file imports that are not public.
func privateImports(path, mod string, public map[string]bool) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		if (p == mod || strings.HasPrefix(p, mod+"/")) && !public[p] {
			out = append(out, strings.TrimPrefix(p, mod+"/"))
		}
	}
	return out, nil
}

type copier struct {
	src, out, mod, newMod string
	res                   *Result
}

// tree copies a file or a directory.
func (c *copier) tree(rel string) error {
	root := filepath.Join(c.src, rel)
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skip[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		r, _ := filepath.Rel(c.src, p)
		return c.file(r)
	})
}

// file copies one file, rewriting the module path in Go sources and go.mod.
func (c *copier) file(rel string) error {
	b, err := os.ReadFile(filepath.Join(c.src, rel))
	if err != nil {
		return err
	}
	if c.newMod != c.mod && (strings.HasSuffix(rel, ".go") || rel == "go.mod") {
		b = bytes.ReplaceAll(b, []byte(c.mod), []byte(c.newMod))
	}
	dst := filepath.Join(c.out, rel)
	if _, err := os.Stat(dst); err == nil {
		return nil // already copied (a package inside an extra tree)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	info, err := os.Stat(filepath.Join(c.src, rel))
	if err != nil {
		return err
	}
	c.res.Files++
	return os.WriteFile(dst, b, info.Mode().Perm()|0o200)
}

// overlay copies the public repository's own files, replacing any of the same name.
func (c *copier) overlay(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		r, _ := filepath.Rel(dir, p)
		c.res.Files++
		return copyFile(p, filepath.Join(c.out, r))
	})
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	info, err := in.Stat()
	if err != nil {
		return err
	}
	o, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm()|0o200)
	if err != nil {
		return err
	}
	if _, err := io.Copy(o, in); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}

// checkClean fails if any exported file names control-plane code or config.
// go.sum is checked after `go mod tidy` by the Makefile, not here: before
// tidying it still lists the private module's dependencies.
func checkClean(out string) error {
	var bad []string
	err := filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "go.sum" || d.Name() == "go.mod" {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, f := range forbidden {
			if bytes.Contains(b, []byte(f)) {
				r, _ := filepath.Rel(out, p)
				bad = append(bad, r+": "+f)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("the open-source tree mentions private code or configuration:\n  %s", strings.Join(bad, "\n  "))
	}
	return nil
}
