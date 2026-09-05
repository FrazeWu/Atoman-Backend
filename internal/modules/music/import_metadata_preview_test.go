package music

import (
	"context"
	"testing"
)

type fakeAlbumImportMetadataEnricher struct{}

func (fakeAlbumImportMetadataEnricher) Enrich(_ context.Context, input AlbumImportMetadataInput) (AlbumImportMetadataResult, error) {
	return AlbumImportMetadataResult{
		AlbumTitle:           "IGOR",
		MusicBrainzReleaseID: "igor-release",
		SourceURL:            "https://musicbrainz.org/release/igor-release",
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
}
