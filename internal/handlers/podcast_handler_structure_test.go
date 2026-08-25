package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"testing"
)

func topLevelFunctionNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	functions := make(map[string]bool)
	for _, declaration := range parsed.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			functions[function.Name.Name] = true
		}
	}
	return functions
}

func TestPodcastHandlerOnlyOwnsRouteAssembly(t *testing.T) {
	functions := topLevelFunctionNames(t, "podcast_handler.go")
	if len(functions) != 1 || !functions["SetupPodcastRoutes"] {
		t.Fatalf("podcast_handler.go functions = %v, want SetupPodcastRoutes only", functions)
	}
}

func TestMediaListHelpersHaveNeutralOwnership(t *testing.T) {
	functions := topLevelFunctionNames(t, "media_list_handler.go")
	if len(functions) != 1 || !functions["boundedListLimit"] {
		t.Fatalf("media_list_handler.go functions = %v, want boundedListLimit only", functions)
	}
}

func TestPodcastSwaggerAnnotationsStayWithTheirHandlers(t *testing.T) {
	assertSwaggerAnnotations(t, "podcast*_handler.go", []string{
		"RecordPodcastPlayback",
		"GetPodcastEpisodes",
		"GetShowEpisodes",
		"GetPodcastEpisode",
		"CreatePodcastEpisode",
		"UpdatePodcastEpisode",
		"DeletePodcastEpisode",
		"UploadPodcastAudio",
		"UploadPodcastCover",
	})
}

func assertSwaggerAnnotations(t *testing.T, pattern string, expected []string) {
	t.Helper()
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	annotation := regexp.MustCompile(`^// ([A-Za-z0-9_]+) godoc$`)
	seen := make(map[string]bool)

	for _, path := range paths {
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
			for _, comment := range group.List {
				match := annotation.FindStringSubmatch(comment.Text)
				if match == nil {
					continue
				}
				seen[match[1]] = true
				function := functionsByLine[files.Position(group.End()).Line+1]
				if function == nil || function.Name.Name != match[1] {
					t.Errorf("%s:%d annotation for %s is not attached to its handler", path, files.Position(group.Pos()).Line, match[1])
				}
			}
		}
	}

	for _, name := range expected {
		if !seen[name] {
			t.Errorf("Swagger annotation for %s was removed", name)
		}
	}
}
