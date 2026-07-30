package blog

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestBlogHTTPOnlyOwnsRouteAssembly(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "http.go", nil, 0)
	if err != nil {
		t.Fatalf("parse http.go: %v", err)
	}

	var functions []string
	for _, declaration := range parsed.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			functions = append(functions, function.Name.Name)
		}
	}
	if len(functions) != 1 || functions[0] != "RegisterRoutes" {
		t.Fatalf("http.go functions = %v, want exactly [RegisterRoutes]", functions)
	}
}

func TestBlogHTTPAnnotationsStayWithTheirHandlers(t *testing.T) {
	channels, err := os.ReadFile("http_channels.go")
	if err != nil {
		t.Fatal(err)
	}
	posts, err := os.ReadFile("http_posts.go")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(channels), "// listPosts godoc") {
		t.Fatal("listPosts annotation must not be attached to a channel helper")
	}
	if !strings.Contains(string(posts), "// listPosts godoc") {
		t.Fatal("http_posts.go must keep the listPosts annotation")
	}
	if strings.Contains(string(posts), "// getDrafts godoc") {
		t.Fatal("getDrafts annotation must not be attached to a post mutation handler")
	}
	drafts, err := os.ReadFile("http_drafts.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(drafts), "// getDrafts godoc") {
		t.Fatal("http_drafts.go must keep the getDrafts annotation")
	}
}
