package music

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestAlbumImportServiceOnlyOwnsCoreSessionAndSharedState(t *testing.T) {
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, "import_service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]bool{
		"buildAlbumImportDTO":                    true,
		"stringValue":                            true,
		"floatValue":                             true,
		"CreateAlbumImportSession":               true,
		"deleteAlbumImportSessionObjectOrRecord": true,
		"updateAlbumImportStatusAndPayload":      true,
		"applyAlbumImportSessionState":           true,
		"albumImportFailureExpiresAt":            true,
		"markAlbumImportFailed":                  true,
		"GetAlbumImportSession":                  true,
		"GetAlbumImportSessionForUser":           true,
		"ListAlbumImportSessionsForUser":         true,
		"loadAlbumImportSession":                 true,
		"normalizeAlbumImportStatus":             true,
		"normalizeAlbumImportInputMode":          true,
		"isAlbumImportInputModeAllowed":          true,
		"isAlbumImportStatusAllowed":             true,
		"isAlbumImportActiveStatus":              true,
		"isAlbumImportMultipartStartStatus":      true,
		"isAlbumImportMultipartPartUploadStatus": true,
		"readAlbumImportPayloadMap":              true,
		"int64Value":                             true,
		"mustMarshalStageNames":                  true,
	}
	actual := make(map[string]bool)
	for _, declaration := range parsed.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			actual[function.Name.Name] = true
		}
	}

	if len(actual) != len(expected) {
		t.Fatalf("import_service.go functions = %v, want exactly %v", actual, expected)
	}
	for name := range expected {
		if !actual[name] {
			t.Fatalf("import_service.go must keep core function %s", name)
		}
	}
}
