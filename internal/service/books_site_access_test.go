package service

import "testing"

func TestDefaultSiteAccessIncludesBooksCapabilities(t *testing.T) {
	matrix := DefaultSiteAccessMatrix()
	books, ok := matrix.Modules["books"]
	if !ok {
		t.Fatal("books module is missing from default site access")
	}
	if books.Enabled == nil || *books.Enabled {
		t.Fatal("books module should be disabled until launch")
	}
	for _, feature := range []string{"books.submit", "books.review", "books.publish_asset"} {
		if !books.Features[feature] {
			t.Fatalf("books feature %q should be enabled by default", feature)
		}
	}
}
