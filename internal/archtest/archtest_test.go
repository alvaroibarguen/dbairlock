package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// repoRoot walks up from the test's working directory to the go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// goFiles calls fn for every non-test Go source file in the module, skipping
// web/ (node_modules can contain Go files) and hidden directories.
func goFiles(t *testing.T, fn func(rel string, f *ast.File)) {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || rel == "web" || rel == "plans" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		fn(filepath.ToSlash(rel), f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// selectors returns pkgName.Sel references in f where pkgName is the local
// name of an import with the given path.
func selectors(f *ast.File, importPath string) []string {
	var local string
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) != importPath {
			continue
		}
		local = filepath.Base(importPath)
		if imp.Name != nil {
			local = imp.Name.Name
		}
	}
	if local == "" || local == "_" {
		return nil
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
				out = append(out, sel.Sel.Name)
			}
		}
		return true
	})
	return out
}

// TestNoEnvironmentVariables enforces ground rule 7: configuration comes from
// the dashboard and three flags, never from the environment.
func TestNoEnvironmentVariables(t *testing.T) {
	forbidden := []string{"Getenv", "LookupEnv", "Environ", "ExpandEnv"}
	checked := 0
	goFiles(t, func(rel string, f *ast.File) {
		checked++
		for _, name := range selectors(f, "os") {
			if slices.Contains(forbidden, name) {
				t.Errorf("%s: os.%s is forbidden; configuration comes from the dashboard or a flag", rel, name)
			}
		}
		for _, name := range selectors(f, "syscall") {
			if name == "Getenv" || name == "Environ" {
				t.Errorf("%s: syscall.%s is forbidden", rel, name)
			}
		}
	})
	if checked == 0 {
		t.Fatal("no Go files checked")
	}
}

// TestSecretsOpenerHolders enforces import rule 5: only targets, identity and
// notify may hold a secrets.Opener (app wires it in; secrets defines it).
func TestSecretsOpenerHolders(t *testing.T) {
	allowed := []string{"internal/targets/", "internal/identity/", "internal/notify/", "internal/app/", "internal/secrets/"}
	const secretsPkg = "github.com/alvaroibarguen/dbairlock/internal/secrets"
	goFiles(t, func(rel string, f *ast.File) {
		if slices.ContainsFunc(allowed, func(p string) bool { return strings.HasPrefix(rel, p) }) {
			return
		}
		if slices.Contains(selectors(f, secretsPkg), "Opener") {
			t.Errorf("%s: only targets, identity and notify may hold a secrets.Opener", rel)
		}
	})
}
