package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// The export holds the agent, the verifier and what they import, and nothing
// of the control plane (D-050). The full build and test of the exported tree
// is `make oss-check`, which CI runs.
func TestExportIsTheOpenSourceTreeOnly(t *testing.T) {
	out := filepath.Join(t.TempDir(), "oss")
	r, err := export(filepath.Join("..", ".."), out, "")
	if err != nil {
		t.Fatal(err)
	}
	pkgs := strings.Join(r.Packages, " ")
	for _, want := range []string{"cmd/agent", "cmd/verify", "internal/jcs", "internal/dsse", "internal/merkle", "internal/pack",
		"internal/evaluate", "internal/policy", "internal/localcheck", "internal/aibom"} {
		if !strings.Contains(" "+pkgs+" ", " "+want+" ") {
			t.Errorf("package %s is missing from %s", want, pkgs)
		}
	}
	for _, private := range []string{"internal/store", "internal/server", "internal/webapp", "internal/issue",
		"internal/vanta", "internal/kmssign", "cmd/server", "migrations", "docs/decisions.md", "docs/architecture.md", "docs/launch", "concierge/evidencepack.py",
		"internal/secretbox", "e2e", "tools/ossexport", "reference", "cmd/agent/connect_test.go", "web/app", "web/embed.go"} {
		if exists(filepath.Join(out, private)) {
			t.Errorf("%s is in the open-source tree", private)
		}
	}
	for _, want := range []string{"LICENSE", "NOTICE", "README.md", "SECURITY.md", "CHANGELOG.md", "Makefile", ".github/workflows/release.yml",
		"web/verify/verify.js", "web/brand.css", "web/security/index.html", "web/docs/agent/index.html", "spec/vectors/manifest.json", "sdk/python/LICENSE", "sdk/js/LICENSE",
		"concierge/verify.py", "concierge/testdata/otlp-sample.jsonl", "tools/demolog/main.go", "packaging/build_wheels.py", "internal/dashboard/static/dashboard.js", "internal/jcs/testdata", "cmd/agent/Dockerfile", "go.mod"} {
		if !exists(filepath.Join(out, want)) {
			t.Errorf("%s is missing", want)
		}
	}
	// Nothing generated locally comes along (bytecode caches, node_modules).
	_ = filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err == nil && (d.Name() == "__pycache__" || d.Name() == "node_modules" || strings.HasSuffix(p, ".pyc")) {
			t.Errorf("%s is in the open-source tree", p)
		}
		return nil
	})
	if len(r.Dropped) != 1 || !strings.HasPrefix(r.Dropped[0], "cmd/agent/connect_test.go") {
		t.Errorf("dropped tests = %v", r.Dropped)
	}
	lic, _ := os.ReadFile(filepath.Join(out, "LICENSE"))
	if !strings.Contains(string(lic), "Apache License") || !strings.Contains(string(lic), "Version 2.0, January 2004") {
		t.Error("LICENSE is not the Apache License 2.0")
	}
	if info, err := os.Stat(filepath.Join(out, "scripts", "release-notes.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("release-notes.sh is not executable: %v", err)
	}
}

func TestExportRewritesTheModulePath(t *testing.T) {
	out := filepath.Join(t.TempDir(), "oss")
	if _, err := export(filepath.Join("..", ".."), out, "example.com/public/witnessmark"); err != nil {
		t.Fatal(err)
	}
	mod, _ := os.ReadFile(filepath.Join(out, "go.mod"))
	src, _ := os.ReadFile(filepath.Join(out, "cmd", "verify", "main.go"))
	if !strings.Contains(string(mod), "module example.com/public/witnessmark\n") || !strings.Contains(string(src), `"example.com/public/witnessmark/internal/pack"`) ||
		strings.Contains(string(src), "lumetrix-advanced-technologies/witnessmark/internal") {
		t.Error("the module path was not rewritten in go.mod and imports")
	}
}

// The leak check refuses a tree that names control-plane code.
func TestCheckCleanRefusesPrivateNames(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "ok.go"), []byte("package x // internal/pack is public"), 0o644)
	if err := checkClean(dir); err != nil {
		t.Fatalf("clean tree refused: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "leak.go"), []byte(`package x; import _ "github.com/jackc/pgx/v5"`), 0o644)
	if err := checkClean(dir); err == nil || !strings.Contains(err.Error(), "leak.go: jackc/pgx") {
		t.Fatalf("leak not refused: %v", err)
	}
}
