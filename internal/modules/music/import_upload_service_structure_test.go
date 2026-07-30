package music

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestAlbumImportUploadServiceOnlyOwnsValidationAndSharedTypes(t *testing.T) {
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, "import_upload_service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]bool{
		"albumImportUploadLimitsFromEnv": true,
		"positiveInt64Env":               true,
		"detectAlbumImportFileRole":      true,
		"normalizeAlbumImportFiles":      true,
		"isAbsoluteAlbumImportPath":      true,
		"albumImportSourceKey":           true,
	}
	actual := make(map[string]bool)
	for _, declaration := range parsed.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			actual[function.Name.Name] = true
		}
	}

	if len(actual) != len(expected) {
		t.Fatalf("import_upload_service.go functions = %v, want exactly %v", actual, expected)
	}
	for name := range expected {
		if !actual[name] {
			t.Fatalf("import_upload_service.go must keep validation function %s", name)
		}
	}
}
