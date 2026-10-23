package gateway_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanDDDArchitecture(t *testing.T) {
	const modulePrefix = "teamie/services/llm-gateway/internal"

	// 0. Root internal/ directory must contain zero non-test Go files.
	rootEntries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read root internal dir: %v", err)
	}
	for _, entry := range rootEntries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
			t.Errorf("forbidden flat non-test Go file in internal/: %s", entry.Name())
		}
	}

	checkDir := func(relDir string, checkImport func(file, imp string)) {
		entries, err := os.ReadDir(relDir)
		if err != nil {
			t.Fatalf("read dir %s: %v", relDir, err)
		}
		fset := token.NewFileSet()
		var goFiles int
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			goFiles++
			fullPath := filepath.Join(relDir, entry.Name())
			f, err := parser.ParseFile(fset, fullPath, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", fullPath, err)
			}
			for _, imp := range f.Imports {
				impPath := strings.Trim(imp.Path.Value, `"`)
				checkImport(fullPath, impPath)
			}
		}
		if goFiles == 0 {
			t.Fatalf("expected non-test Go files in %s", relDir)
		}
	}

	// 1. domain must only import Go standard library packages.
	checkDir("domain", func(file, imp string) {
		if strings.Contains(imp, ".") || strings.HasPrefix(imp, "teamie/") {
			t.Errorf("domain layer violation in %s: forbidden non-stdlib import %q", file, imp)
		}
	})

	// 2. app must only import Go standard library and internal/domain.
	checkDir("app", func(file, imp string) {
		if imp == modulePrefix+"/domain" {
			return
		}
		if strings.Contains(imp, ".") || strings.HasPrefix(imp, "teamie/") {
			t.Errorf("app layer violation in %s: forbidden import %q (only stdlib and %s/domain allowed)", file, imp, modulePrefix)
		}
	})

	// 3. adapter/* must never import infra/* or the root internal package.
	for _, sub := range []string{"adapter/http", "adapter/rpc"} {
		checkDir(sub, func(file, imp string) {
			if imp == modulePrefix || strings.HasPrefix(imp, modulePrefix+"/infra") {
				t.Errorf("adapter layer violation in %s: forbidden import %q", file, imp)
			}
		})
	}

	// 4. infra/* must never import adapter/* or app, and infra/pg must use pkg/postgres instead of raw pgx.
	for _, sub := range []string{"infra/pg", "infra/kms", "infra/rpcclient", "infra/upstream"} {
		checkDir(sub, func(file, imp string) {
			if imp == modulePrefix || imp == modulePrefix+"/app" || strings.HasPrefix(imp, modulePrefix+"/adapter") {
				t.Errorf("infra layer violation in %s: forbidden import %q", file, imp)
			}
			if sub == "infra/pg" && strings.HasPrefix(imp, "github.com/jackc/pgx") {
				t.Errorf("infra/pg violation in %s: must use teamie/pkg/postgres instead of raw %q", file, imp)
			}
		})
	}
}
