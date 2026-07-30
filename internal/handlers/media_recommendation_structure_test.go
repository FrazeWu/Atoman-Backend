package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestMediaRecommendationHelpersHaveNeutralOwnership(t *testing.T) {
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, "media_recommendation_handler.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	expectedFunctions := map[string]bool{
		"parseRecommendationMode":  true,
		"clampRecommendation":      true,
		"recommendationScoreLabel": true,
	}
	actualFunctions := make(map[string]bool)
	actualTypes := make(map[string]bool)
	for _, declaration := range parsed.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			actualFunctions[typed.Name.Name] = true
		case *ast.GenDecl:
			for _, spec := range typed.Specs {
				if typeSpec, ok := spec.(*ast.TypeSpec); ok {
					actualTypes[typeSpec.Name.Name] = true
				}
			}
		}
	}

	if len(actualFunctions) != len(expectedFunctions) {
		t.Fatalf("shared functions = %v, want exactly %v", actualFunctions, expectedFunctions)
	}
	for name := range expectedFunctions {
		if !actualFunctions[name] {
			t.Fatalf("shared recommendation function %s is missing", name)
		}
	}
	if len(actualTypes) != 1 || !actualTypes["recommendationItemDTO"] {
		t.Fatalf("shared types = %v, want recommendationItemDTO only", actualTypes)
	}
}
