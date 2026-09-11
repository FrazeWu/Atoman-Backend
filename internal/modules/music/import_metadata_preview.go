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
	Matched         bool                              `json:"matched"`
	AlbumTitle      string                            `json:"albumTitle,omitempty"`
	ReleaseDate     string                            `json:"releaseDate,omitempty"`
	CoverURL        string                            `json:"coverUrl,omitempty"`
	AlbumType       string                            `json:"albumType,omitempty"`
	SourceURL       string                            `json:"sourceUrl"`
	MetadataSource  string                            `json:"metadataSource,omitempty"`
	ExternalID      string                            `json:"externalId,omitempty"`
	MatchStatus     string                            `json:"matchStatus,omitempty"`
	MatchConfidence float64                           `json:"matchConfidence,omitempty"`
	MetadataError   string                            `json:"metadataError,omitempty"`
	MetadataSources []AlbumImportMetadataSourceResult `json:"sources,omitempty"`
	Tracks          []AlbumImportDTOTrack             `json:"tracks"`
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
	if err != nil {
		return AlbumImportMetadataPreviewDTO{Tracks: baseMetadataTracks(tracks), MetadataError: err.Error()}, nil
	}
	return AlbumImportMetadataPreviewDTO{
		Matched: result.MetadataSource != "", AlbumTitle: result.AlbumTitle,
		ReleaseDate: result.ReleaseDate, CoverURL: result.CoverURL, AlbumType: result.AlbumType,
		SourceURL: result.SourceURL, MetadataSource: result.MetadataSource,
		ExternalID: result.ExternalID, MatchStatus: result.MatchStatus,
		MatchConfidence: result.MatchConfidence, MetadataError: result.MetadataError,
		MetadataSources: result.MetadataSources, Tracks: result.Tracks,
	}, nil
}
