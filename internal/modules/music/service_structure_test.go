package music

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestMusicServiceOnlyOwnsCompositionAndSharedPagination(t *testing.T) {
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, "service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]bool{
		"NewService":                       true,
		"NewServiceWithS3":                 true,
		"WithAlbumLinkSuggestionProvider":  true,
		"normalizeMusicRecommendationPage": true,
	}
	actual := make(map[string]bool)
	for _, declaration := range parsed.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			actual[function.Name.Name] = true
		}
	}

	if len(actual) != len(expected) {
		t.Fatalf("service.go functions = %v, want exactly %v", actual, expected)
	}
	for name := range expected {
		if !actual[name] {
			t.Fatalf("service.go must keep core function %s", name)
		}
	}
}
