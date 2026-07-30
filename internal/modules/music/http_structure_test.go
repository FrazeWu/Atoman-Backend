package music

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestMusicHTTPOnlyOwnsRouteAssemblyAndSharedTransportHelpers(t *testing.T) {
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, "http.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]bool{
		"RegisterRoutes":   true,
		"currentMusicUser": true,
		"parseMusicID":     true,
		"bindJSON":         true,
	}
	actual := make(map[string]bool)
	for _, declaration := range parsed.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			actual[function.Name.Name] = true
		}
	}
	if len(actual) != len(expected) {
		t.Fatalf("http.go functions = %v, want exactly %v", actual, expected)
	}
	for name := range expected {
		if !actual[name] {
			t.Fatalf("http.go must keep shared transport function %s", name)
		}
	}
}

func TestMusicHTTPSwaggerAnnotationsStayWithTheirHandlers(t *testing.T) {
	paths, err := filepath.Glob("*http*.go")
	if err != nil {
		t.Fatal(err)
	}
	annotation := regexp.MustCompile(`^// ([A-Za-z0-9_]+) godoc$`)
	seenAnnotations := make(map[string]bool)

	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		files := token.NewFileSet()
		parsed, err := parser.ParseFile(files, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		functionsByLine := make(map[int]*ast.FuncDecl)
		for _, declaration := range parsed.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok {
				functionsByLine[files.Position(function.Pos()).Line] = function
			}
		}
		for _, group := range parsed.Comments {
			match := annotation.FindStringSubmatch(group.List[0].Text)
			if match == nil {
				continue
			}
			seenAnnotations[match[1]] = true
			function := functionsByLine[files.Position(group.End()).Line+1]
			if function == nil || function.Name.Name != match[1] {
				t.Errorf("%s:%d annotation for %s is not attached to its handler", path, files.Position(group.Pos()).Line, match[1])
			}
		}
	}

	for _, name := range []string{
		"recordSongPlay",
		"listListeningHistory",
		"listPlaylistBookmarks",
		"createPlaylistBookmark",
		"deletePlaylistBookmark",
		"listPublicPlaylists",
		"discover",
		"createPlaylist",
		"getPlaylist",
		"listPlaylistSongs",
		"reorderPlaylistSongs",
	} {
		if !seenAnnotations[name] {
			t.Errorf("Swagger annotation for %s was removed", name)
		}
	}
}
