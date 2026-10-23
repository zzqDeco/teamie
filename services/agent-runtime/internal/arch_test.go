package internal_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuntimeCleanDDDLayerBoundaries enforces the 4-layer Hexagonal Clean DDD
// import rules across services/agent-runtime/internal/{domain,app,adapter,infra}
// and verifies that no flat non-test .go files remain in internal/.
func TestRuntimeCleanDDDLayerBoundaries(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal dir: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
			t.Errorf("flat non-test .go file %q is forbidden in internal/; move into domain, app, adapter, or infra", entry.Name())
		}
	}

	fset := token.NewFileSet()
	packages := []struct {
		dir     string
		allowed func(imp string) bool
		rule    string
	}{
		{
			dir: "domain",
			allowed: func(imp string) bool {
				return !strings.Contains(imp, ".") && !strings.HasPrefix(imp, "teamie/")
			},
			rule: "domain layer must only import Go standard library packages",
		},
		{
			dir: "app",
			allowed: func(imp string) bool {
				if imp == "teamie/services/agent-runtime/internal/domain" {
					return true
				}
				return !strings.Contains(imp, ".") && !strings.HasPrefix(imp, "teamie/")
			},
			rule: "app layer must only import Go standard library and internal/domain",
		},
		{
			dir: "adapter/rpc",
			allowed: func(imp string) bool {
				return !strings.HasPrefix(imp, "teamie/services/agent-runtime/internal/infra")
			},
			rule: "adapter layer must not import infra layer directly",
		},
		{
			dir: "adapter/http",
			allowed: func(imp string) bool {
				return !strings.HasPrefix(imp, "teamie/services/agent-runtime/internal/infra")
			},
			rule: "adapter layer must not import infra layer directly",
		},
		{
			dir: "adapter/worker",
			allowed: func(imp string) bool {
				return !strings.HasPrefix(imp, "teamie/services/agent-runtime/internal/infra")
			},
			rule: "adapter layer must not import infra layer directly",
		},
		{
			dir: "infra/pg",
			allowed: func(imp string) bool {
				return !strings.HasPrefix(imp, "teamie/services/agent-runtime/internal/adapter") &&
					!strings.HasPrefix(imp, "teamie/services/agent-runtime/internal/app")
			},
			rule: "infra layer must not import adapter or app layers",
		},
		{
			dir: "infra/eino",
			allowed: func(imp string) bool {
				return !strings.HasPrefix(imp, "teamie/services/agent-runtime/internal/adapter") &&
					!strings.HasPrefix(imp, "teamie/services/agent-runtime/internal/app")
			},
			rule: "infra layer must not import adapter or app layers",
		},
		{
			dir: "infra/rpcclient",
			allowed: func(imp string) bool {
				return !strings.HasPrefix(imp, "teamie/services/agent-runtime/internal/adapter") &&
					!strings.HasPrefix(imp, "teamie/services/agent-runtime/internal/app")
			},
			rule: "infra layer must not import adapter or app layers",
		},
	}

	for _, pkg := range packages {
		pkgEntries, err := os.ReadDir(pkg.dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", pkg.dir, err)
		}
		for _, entry := range pkgEntries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			fullPath := filepath.Join(pkg.dir, entry.Name())
			file, err := parser.ParseFile(fset, fullPath, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", fullPath, err)
			}
			for _, spec := range file.Imports {
				imp := strings.Trim(spec.Path.Value, `"`)
				if !pkg.allowed(imp) {
					t.Errorf("%s imports forbidden package %q (%s)", fullPath, imp, pkg.rule)
				}
			}
		}
	}
}
