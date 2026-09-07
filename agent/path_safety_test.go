package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Run this check before the suite on a disposable runner. It rejects known
// production-path entry points in test source, including function references.
// It is a regression guard, not whole-program filesystem sandboxing.
func TestNoProductionPathCallsInTests(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(wd)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("cannot locate repository root: %v", err)
	}
	blocked := map[string]map[string]bool{
		"Zeus/agent": {
			"New": true, "LoadCredentials": true, "SaveCredentials": true,
			"ClearCredentials": true, "tokenFilePath": true,
		},
		"Zeus/storage": {"DataDir": true},
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		aliases := make(map[string]string)
		for _, spec := range f.Imports {
			pkg, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if blocked[pkg] == nil {
				continue
			}
			alias := pkg[strings.LastIndex(pkg, "/")+1:]
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			if alias == "." {
				t.Errorf("%s: dot import of filesystem-owning package is prohibited", fset.Position(spec.Pos()))
			}
			aliases[alias] = pkg
		}
		localPkg := "Zeus/" + f.Name.Name
		ast.Inspect(f, func(node ast.Node) bool {
			switch expr := node.(type) {
			case *ast.SelectorExpr:
				if ident, ok := expr.X.(*ast.Ident); ok {
					if blocked[aliases[ident.Name]][expr.Sel.Name] {
						t.Errorf("%s: production-path entry point %s.%s; inject t.TempDir instead", fset.Position(expr.Pos()), ident.Name, expr.Sel.Name)
					}
				}
			case *ast.CallExpr:
				if ident, ok := expr.Fun.(*ast.Ident); ok && blocked[localPkg][ident.Name] {
					t.Errorf("%s: production-path call %s; inject t.TempDir instead", fset.Position(expr.Pos()), ident.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
