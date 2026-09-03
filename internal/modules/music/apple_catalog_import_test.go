package music

import "testing"

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
