package control

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func collectImports(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var imports []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", entry.Name(), parseErr)
		}
		for _, imp := range file.Imports {
			imports = append(imports, strings.Trim(imp.Path.Value, `"`))
		}
	}
	return imports
}

func TestControlCleanDDDLayerBoundaries(t *testing.T) {
	// 1. Domain layer must be 100% pure Go standard library (zero external or framework imports).
	for _, imp := range collectImports(t, "domain") {
		if strings.Contains(imp, ".") || strings.HasPrefix(imp, "teamie/") {
			t.Fatalf("domain layer must not import external or non-stdlib package %q", imp)
		}
	}

	// 2. Application layer must depend only on Go stdlib and the control domain layer.
	for _, imp := range collectImports(t, "app") {
		if imp == "teamie/agent-control/internal/control/domain" {
			continue
		}
		if strings.Contains(imp, ".") || strings.HasPrefix(imp, "teamie/") {
			t.Fatalf("app layer must not import infrastructure/transport package %q", imp)
		}
	}

	// 3. Infrastructure layer must never import inbound adapters (http/rpc) or app layer.
	for _, sub := range []string{"infra/pg", "infra/rpcclient"} {
		for _, imp := range collectImports(t, sub) {
			if strings.Contains(imp, "/adapter/") || strings.HasSuffix(imp, "/app") || strings.Contains(imp, "/app/") {
				t.Fatalf("%s must not import adapter or app layer %q", sub, imp)
			}
		}
	}
}
