package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestAdminHandlerOnlyOwnsRouteAssembly(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "admin_handler.go", nil, 0)
	if err != nil {
		t.Fatalf("parse admin_handler.go: %v", err)
	}

	var functions []string
	for _, declaration := range parsed.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			functions = append(functions, function.Name.Name)
		}
	}
	if len(functions) != 1 || functions[0] != "SetupAdminRoutes" {
		t.Fatalf("admin_handler.go functions = %v, want exactly [SetupAdminRoutes]", functions)
	}
}
