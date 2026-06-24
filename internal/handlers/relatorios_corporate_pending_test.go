package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCorporatePendingTotalsIsAbsentFromProduction(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	handlersDir := filepath.Dir(testFile)
	entries, err := os.ReadDir(handlersDir)
	if err != nil {
		t.Fatalf("read handlers directory: %v", err)
	}

	fset := token.NewFileSet()
	var references []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(handlersDir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if !ok || identifier.Name != "queryCorporatePendingTotals" {
				return true
			}
			references = append(references, fset.Position(identifier.Pos()).String())
			return true
		})
	}

	if len(references) != 0 {
		t.Fatalf("queryCorporatePendingTotals must remain absent from production: %v", references)
	}
}
