package music

import (
	"context"
	"strconv"
	"strings"
)

type AlbumImportMetadataPreviewInput struct {
	AlbumTitle  string   `json:"albumTitle"`
	Artist      string   `json:"artist"`
	TrackTitles []string `json:"trackTitles"`
}

type AlbumImportMetadataPreviewDTO struct {
	Matched   bool                  `json:"matched"`
	SourceURL string                `json:"sourceUrl"`
	Tracks    []AlbumImportDTOTrack `json:"tracks"`
}

func (s *Service) PreviewAlbumImportMetadata(ctx context.Context, input AlbumImportMetadataPreviewInput) (AlbumImportMetadataPreviewDTO, error) {
	tracks := make([]AlbumImportMetadataTrack, 0, len(input.TrackTitles))
	for index, title := range input.TrackTitles {
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		tracks = append(tracks, AlbumImportMetadataTrack{
			Title:       title,
			TrackNumber: index + 1,
			Origin:      "local_preview:" + strconv.Itoa(index+1),
		})
	}
	if s == nil || s.albumImportMetadataEnricher == nil || strings.TrimSpace(input.AlbumTitle) == "" || len(tracks) == 0 {
		return AlbumImportMetadataPreviewDTO{Tracks: []AlbumImportDTOTrack{}}, nil
	}
	result, err := s.albumImportMetadataEnricher.Enrich(ctx, AlbumImportMetadataInput{
		AlbumTitle: strings.TrimSpace(input.AlbumTitle),
		Artist:     strings.TrimSpace(input.Artist),
		Tracks:     tracks,
		SkipLyrics: true,
	})
	if err != nil || result.MusicBrainzReleaseID == "" {
		return AlbumImportMetadataPreviewDTO{Tracks: []AlbumImportDTOTrack{}}, nil
	}
	return AlbumImportMetadataPreviewDTO{Matched: true, SourceURL: result.SourceURL, Tracks: result.Tracks}, nil
}
