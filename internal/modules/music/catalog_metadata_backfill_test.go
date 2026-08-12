package music

import (
	"testing"
	"time"

	"atoman/internal/model"
)

func TestExistingSongSourceTitleRemovesArtistPrefix(t *testing.T) {
	song := model.Song{Title: "Wrong", SourceFileName: "Jean Grae - Block Party.mp3"}
	if got := existingSongSourceTitle(song, "Jean Grae"); got != "Block Party" {
		t.Fatalf("title = %q", got)
	}
}

func TestExistingSongSourceTitleUsesStoredTitleWithoutSourceFileName(t *testing.T) {
	song := model.Song{Title: "Street Lights"}
	if got := existingSongSourceTitle(song, "Ye"); got != "Street Lights" {
		t.Fatalf("title = %q", got)
	}
}

func TestExistingSongSourceTitleRemovesTrackAndArtistPrefix(t *testing.T) {
	song := model.Song{Title: "早", SourceFileName: "01 - 万能青年旅店 - 早.mp3"}
	if got := existingSongSourceTitle(song, "万能青年旅店"); got != "早" {
		t.Fatalf("title = %q", got)
	}
}

func TestExistingSongSourceTitleRemovesArtistSuffix(t *testing.T) {
	song := model.Song{Title: "如此-杰子", SourceFileName: "如此-杰子.wav"}
	if got := existingSongSourceTitle(song, "杰子"); got != "如此" {
		t.Fatalf("title = %q", got)
	}
}

func TestMusicArtistSearchNamesIncludesLegalNameAndAliases(t *testing.T) {
	artist := model.Artist{Name: "Ye", LegalName: "Kanye Omari West", Aliases: []model.ArtistAlias{{Alias: "Kanye West"}, {Alias: "Ye"}}}
	got := musicArtistSearchNames(artist)
	if len(got) != 3 || got[0] != "Ye" || got[1] != "Kanye Omari West" || got[2] != "Kanye West" {
		t.Fatalf("names = %#v", got)
	}
}

func TestStrippedCatalogSongTitleUsesLastSeparator(t *testing.T) {
	if got := strippedCatalogSongTitle("Kanye West Feat. Jay-Z - Monster"); got != "Monster" {
		t.Fatalf("title = %q", got)
	}
}

func TestAppendMusicBrainzSourcePreservesAndDeduplicates(t *testing.T) {
	sources := []model.MusicSource{{Type: "url", URL: "https://example.com"}}
	got := appendMusicBrainzSource(sources, "https://musicbrainz.org/release/id")
	got = appendMusicBrainzSource(got, "https://musicbrainz.org/release/id")
	if len(got) != 2 || got[0].URL != "https://example.com" || got[1].Title != "MusicBrainz" {
		t.Fatalf("sources = %#v", got)
	}
}

func TestParseBackfillReleaseDate(t *testing.T) {
	date, precision, ok := parseBackfillReleaseDate("2008-11")
	if !ok || precision != "month" || date != time.Date(2008, 11, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("date = %v/%s/%v", date, precision, ok)
	}
}
