package handlers

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

func TestAdminFeedHandlersAreSplitByResponsibility(t *testing.T) {
	expected := map[string][]string{
		"admin_feed_source_handler.go": {
			"normalizeExternalRSSURL",
			"requireFeedSourceOwner",
			"feedSourceImpact",
			"GetAdminFeedSourceImpact",
			"GetAdminFeedSourceDiagnostics",
			"AdminListFeedSources",
			"AdminUpdateFeedSourceRow",
			"AdminDeleteFeedSourceRow",
			"CreateAdminFeedSource",
			"UpdateAdminFeedSource",
			"SyncAdminFeedSource",
		},
		"admin_feed_fulltext_settings_handler.go": {
			"defaultAdminFeedFullTextSettings",
			"loadAdminFeedFullTextSettings",
			"saveAdminFeedFullTextSettings",
			"GetAdminFeedFullTextSettings",
			"UpdateAdminFeedFullTextSettings",
			"UpdateAdminFeedFullTextSourceSettings",
		},
		"admin_feed_fulltext_handler.go": {
			"parseAdminListParams",
			"adminFullTextBlogSourceQuery",
			"adminFullTextBlogItemQuery",
			"adminFeedFullTextHealthStatus",
			"GetAdminFeedFullTextHealth",
			"GetAdminFeedFullTextSources",
			"GetAdminFeedFullTextItems",
			"RetryAdminFeedFullTextItem",
		},
	}

	for filename, expectedFunctions := range expected {
		parsed, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}

		actual := make(map[string]bool)
		for _, declaration := range parsed.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok {
				actual[function.Name.Name] = true
			}
		}
		if len(actual) != len(expectedFunctions) {
			t.Fatalf("%s functions = %v, want exactly %v", filename, actual, expectedFunctions)
		}
		for _, name := range expectedFunctions {
			if !actual[name] {
				t.Fatalf("%s is missing %s", filename, name)
			}
		}
	}

	if _, err := os.Stat("admin_feed_handler.go"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("admin_feed_handler.go should be removed after responsibilities are split, stat error = %v", err)
	}
}
