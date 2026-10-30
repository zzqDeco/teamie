package marketplace

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

func TestMarketplaceCleanDDDLayerBoundaries(t *testing.T) {
	// 0. Root internal/ directory must contain zero flat non-test .go business files.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
			t.Fatalf("flat root business file %q is forbidden in internal/; move into domain, app, adapter, or infra", entry.Name())
		}
	}

	// 1. Domain layer must be 100% pure Go standard library (zero external or framework imports).
	for _, imp := range collectImports(t, "domain") {
		if strings.Contains(imp, ".") || strings.HasPrefix(imp, "teamie/") {
			t.Fatalf("domain layer must not import external or non-stdlib package %q", imp)
		}
	}

	// 2. Application layer must depend only on Go stdlib and the marketplace domain layer.
	for _, imp := range collectImports(t, "app") {
		if imp == "teamie/services/marketplace/internal/domain" {
			continue
		}
		if strings.Contains(imp, ".") || strings.HasPrefix(imp, "teamie/") {
			t.Fatalf("app layer must not import infrastructure/transport package %q", imp)
		}
	}

	// 3. Inbound HTTP adapter must never import infra packages.
	for _, imp := range collectImports(t, "adapter/http") {
		if strings.Contains(imp, "/infra/") {
			t.Fatalf("adapter/http must not import infra package %q", imp)
		}
	}

	// 4. Infrastructure layer must never import inbound adapters or app.
	for _, sub := range []string{"infra/pg", "infra/flags", "infra/rpcclient", "infra/probe"} {
		for _, imp := range collectImports(t, sub) {
			if strings.Contains(imp, "/adapter/") || strings.Contains(imp, "/app") {
				t.Fatalf("%s must not import adapter or app layer %q", sub, imp)
			}
		}
	}
}
