package reference

import "testing"

func TestSearchPatternsEscapeLikeWildcards(t *testing.T) {
	if got, want := searchContainsPattern(`A_b%\\c`), `%a\_b\%\\\\c%`; got != want {
		t.Fatalf("searchContainsPattern() = %q, want %q", got, want)
	}
	if got, want := searchPrefixPattern(`A_b%\\c`), `a\_b\%\\\\c%`; got != want {
		t.Fatalf("searchPrefixPattern() = %q, want %q", got, want)
	}
}

func TestEscapedSearchTermNormalizesWhitespaceAndCase(t *testing.T) {
	if got, want := escapedSearchTerm("  YeS  "), "yes"; got != want {
		t.Fatalf("escapedSearchTerm() = %q, want %q", got, want)
	}
}
