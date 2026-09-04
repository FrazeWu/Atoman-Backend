package music

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAppleArtistCandidatesPreferBestChartPosition(t *testing.T) {
	songs := []appleChartItem{
		{ArtistID: "artist-b", ArtistName: "B"},
		{ArtistID: "artist-a", ArtistName: "A"},
	}
	albums := []appleChartItem{
		{ArtistID: "artist-a", ArtistName: "A album spelling"},
		{ArtistID: "artist-c", ArtistName: "C"},
	}

	got := appleArtistCandidates(songs, albums, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(got))
	}
	if got[0].ExternalID != "artist-b" || got[0].ChartRank != 1 {
		t.Fatalf("unexpected first candidate: %#v", got[0])
	}
	if got[1].ExternalID != "artist-a" || got[1].ChartRank != 1 {
		t.Fatalf("unexpected deduplicated candidate: %#v", got[1])
	}
	if got[2].ExternalID != "artist-c" || got[2].ChartRank != 2 {
		t.Fatalf("unexpected third candidate: %#v", got[2])
	}
}

func TestAppleAlbumType(t *testing.T) {
	tests := []struct {
		name       string
		trackCount int
		want       string
	}{
		{name: "Release - Single", trackCount: 1, want: "single"},
		{name: "Release - EP", trackCount: 5, want: "ep"},
		{name: "Regular Album", trackCount: 10, want: "album"},
	}
	for _, test := range tests {
		if got := appleAlbumType(test.name, test.trackCount); got != test.want {
			t.Fatalf("appleAlbumType(%q, %d) = %q, want %q", test.name, test.trackCount, got, test.want)
		}
	}
}

func TestHipHopConsensusArtistSeedsAreRankedAndUnique(t *testing.T) {
	seeds := HipHopConsensusArtistSeeds()
	if len(seeds) != 100 {
		t.Fatalf("expected 100 consensus artist seeds, got %d", len(seeds))
	}
	seen := make(map[string]struct{}, len(seeds))
	for index, seed := range seeds {
		if seed.Rank != index+1 {
			t.Fatalf("expected rank %d at index %d, got %#v", index+1, index, seed)
		}
		if seed.Name == "" {
			t.Fatalf("seed %d has no artist name", seed.Rank)
		}
		if _, exists := seen[seed.Name]; exists {
			t.Fatalf("duplicate consensus artist seed %q", seed.Name)
		}
		seen[seed.Name] = struct{}{}
	}
}

func TestAppleArtistSearchPrefersExactName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("term") != "Jay-Z" || request.URL.Query().Get("entity") != "musicArtist" || request.URL.Query().Get("country") != "US" {
			t.Fatalf("unexpected Apple artist search query: %s", request.URL.RawQuery)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"artistId":1,"artistName":"Kevin JAY Z","artistViewUrl":"https://music.apple.com/us/artist/kevin-jay-z/1"},{"artistId":2,"artistName":"JAŸ-Z","artistViewUrl":"https://music.apple.com/us/artist/jay-z/2"}]}`))
	}))
	defer server.Close()

	importer := NewAppleCatalogImporter(nil, server.Client(), [16]byte{}, "test")
	importer.searchBaseURL = server.URL
	candidate, err := importer.searchArtist(context.Background(), "us", HipHopConsensusArtistSeed{Rank: 3, Name: "Jay-Z"})
	if err != nil {
		t.Fatalf("search artist: %v", err)
	}
	if candidate.ExternalID != "2" || candidate.Name != "JAŸ-Z" || candidate.ChartRank != 3 {
		t.Fatalf("unexpected resolved artist: %#v", candidate)
	}
}
