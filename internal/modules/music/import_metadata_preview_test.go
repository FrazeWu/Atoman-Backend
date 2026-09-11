package music

import (
	"context"
	"testing"
)

type fakeAlbumImportMetadataEnricher struct{}

func (fakeAlbumImportMetadataEnricher) Enrich(_ context.Context, input AlbumImportMetadataInput) (AlbumImportMetadataResult, error) {
	return AlbumImportMetadataResult{
		AlbumTitle:           "IGOR",
		ReleaseDate:          "2019-05-17",
		AlbumType:            "album",
		CoverURL:             "https://cover.example/igor.jpg",
		MusicBrainzReleaseID: "igor-release",
		MetadataSource:       "musicbrainz",
		SourceURL:            "https://musicbrainz.org/release/igor-release",
		MatchStatus:          "matched",
		MatchConfidence:      0.95,
		MetadataSources: []AlbumImportMetadataSourceResult{
			{Provider: "discogs", Status: "unmatched", Error: "no safe release"},
			{Provider: "musicbrainz", Status: "matched", Selected: true, SelectedTitle: "IGOR", SourceURL: "https://musicbrainz.org/release/igor-release", ExternalID: "igor-release", MatchConfidence: 0.95},
		},
		Tracks: []AlbumImportDTOTrack{
			{Title: "IGOR'S THEME", TrackNumber: 1},
			{Title: "EARFQUAKE", TrackNumber: 2},
		},
	}, nil
}

func TestPreviewAlbumImportMetadataReturnsMatchedTrackOrder(t *testing.T) {
	service := NewService(nil).WithAlbumImportMetadataEnricher(fakeAlbumImportMetadataEnricher{})

	preview, err := service.PreviewAlbumImportMetadata(context.Background(), AlbumImportMetadataPreviewInput{
		AlbumTitle:  "IGOR",
		Artist:      "Tyler, The Creator",
		TrackTitles: []string{"EARFQUAKE", "IGOR'S THEME"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Matched || preview.SourceURL == "" {
		t.Fatalf("expected MusicBrainz match, got %#v", preview)
	}
	if len(preview.Tracks) != 2 || preview.Tracks[0].Title != "IGOR'S THEME" || preview.Tracks[1].Title != "EARFQUAKE" {
		t.Fatalf("expected matched order, got %#v", preview.Tracks)
	}
	if preview.AlbumTitle != "IGOR" || preview.ReleaseDate != "2019-05-17" || preview.CoverURL == "" || preview.AlbumType != "album" {
		t.Fatalf("expected album metadata, got %#v", preview)
	}
	if len(preview.MetadataSources) != 2 || !preview.MetadataSources[1].Selected {
		t.Fatalf("expected source decisions, got %#v", preview.MetadataSources)
	}
}
