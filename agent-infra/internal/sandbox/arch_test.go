package sandbox_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanDDDArchitecture(t *testing.T) {
	const modulePrefix = "teamie/agent-infra/internal/sandbox"
	const gatewayPrefix = "teamie/agent-infra/internal/gateway"

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

	// 2. app must only import Go standard library and sandbox/domain.
	checkDir("app", func(file, imp string) {
		if imp == modulePrefix+"/domain" {
			return
		}
		if strings.Contains(imp, ".") || strings.HasPrefix(imp, "teamie/") {
			t.Errorf("app layer violation in %s: forbidden import %q (only stdlib and %s/domain allowed)", file, imp, modulePrefix)
		}
	})

	// 3. adapter/* must never import infra/* or the root sandbox composition package.
	for _, sub := range []string{"adapter/rpc"} {
		checkDir(sub, func(file, imp string) {
			if imp == modulePrefix || strings.HasPrefix(imp, modulePrefix+"/infra") {
				t.Errorf("adapter layer violation in %s: forbidden import %q", file, imp)
			}
		})
	}

	// 4. infra/* must never import adapter/* or app.
	for _, sub := range []string{"infra/docker", "infra/fs"} {
		checkDir(sub, func(file, imp string) {
			if imp == modulePrefix || imp == modulePrefix+"/app" || strings.HasPrefix(imp, modulePrefix+"/adapter") {
				t.Errorf("infra layer violation in %s: forbidden import %q", file, imp)
			}
		})
	}

	// 5. sandbox (and all subpackages) must never import gateway.
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		for _, imp := range f.Imports {
			impPath := strings.Trim(imp.Path.Value, `"`)
			if impPath == gatewayPrefix || strings.HasPrefix(impPath, gatewayPrefix+"/") {
				t.Errorf("bounded-context isolation violation in %s: sandbox must never import %q", path, impPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk sandbox dir: %v", err)
	}
}
