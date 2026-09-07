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
	Matched         bool                  `json:"matched"`
	SourceURL       string                `json:"sourceUrl"`
	MetadataSource  string                `json:"metadataSource,omitempty"`
	ExternalID      string                `json:"externalId,omitempty"`
	MatchStatus     string                `json:"matchStatus,omitempty"`
	MatchConfidence float64               `json:"matchConfidence,omitempty"`
	Tracks          []AlbumImportDTOTrack `json:"tracks"`
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
		return AlbumImportMetadataPreviewDTO{Tracks: baseMetadataTracks(tracks)}, nil
	}
	result, err := s.albumImportMetadataEnricher.Enrich(ctx, AlbumImportMetadataInput{
		AlbumTitle: strings.TrimSpace(input.AlbumTitle),
		Artist:     strings.TrimSpace(input.Artist),
		Tracks:     tracks,
		SkipLyrics: true,
	})
	if err != nil || result.MetadataSource == "" {
		return AlbumImportMetadataPreviewDTO{Tracks: baseMetadataTracks(tracks)}, nil
	}
	return AlbumImportMetadataPreviewDTO{
		Matched: true, SourceURL: result.SourceURL, MetadataSource: result.MetadataSource,
		ExternalID: result.ExternalID, MatchStatus: result.MatchStatus,
		MatchConfidence: result.MatchConfidence, Tracks: result.Tracks,
	}, nil
}
