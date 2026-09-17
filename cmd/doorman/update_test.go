package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Keep the checker reachable only from the process wrapper, never from the
// daemon, protocol handlers or packages on the call path.
func TestUpdateCheckerImportFence(t *testing.T) {
	for _, root := range []string{".", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			for _, imp := range file.Imports {
				name, _ := strconv.Unquote(imp.Path.Value)
				if name != "callmemaybe/internal/updatecheck" {
					continue
				}
				if path != "main.go" {
					t.Errorf("%s imports the operator-only update checker", path)
					continue
				}
				for _, decl := range file.Decls {
					fn, ok := decl.(*ast.FuncDecl)
					if !ok || fn.Name.Name == "main" {
						continue
					}
					ast.Inspect(fn, func(n ast.Node) bool {
						id, ok := n.(*ast.Ident)
						if ok && id.Name == "updatecheck" {
							t.Errorf("%s reaches updatecheck outside main", fn.Name.Name)
						}
						return true
					})
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
