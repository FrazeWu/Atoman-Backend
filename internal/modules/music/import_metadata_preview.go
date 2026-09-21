package music

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
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
	Genres          []string                          `json:"genres,omitempty"`
	Styles          []string                          `json:"styles,omitempty"`
	Labels          []string                          `json:"labels,omitempty"`
	Country         string                            `json:"country,omitempty"`
	Formats         []string                          `json:"formats,omitempty"`
	MissingArtists  []string                          `json:"missingArtists,omitempty"`
	MetadataSources []AlbumImportMetadataSourceResult `json:"sources,omitempty"`
	Tracks          []AlbumImportDTOTrack             `json:"tracks"`
}

func (s *Service) PreviewAlbumImportMetadata(ctx context.Context, input AlbumImportMetadataPreviewInput) (AlbumImportMetadataPreviewDTO, error) {
	artist := strings.TrimSpace(input.Artist)
	if artist == "" {
		artist = inferCommonAlbumImportArtist(input.TrackTitles)
	}
	tracks := make([]AlbumImportMetadataTrack, 0, len(input.TrackTitles))
	for index, title := range input.TrackTitles {
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		tracks = append(tracks, AlbumImportMetadataTrack{
			Title:         normalizeAlbumImportTrackTitle(title, artist),
			OriginalTitle: title,
			TrackNumber:   index + 1,
			Origin:        "local_preview:" + strconv.Itoa(index+1),
		})
	}
	if s == nil || s.albumImportMetadataEnricher == nil || strings.TrimSpace(input.AlbumTitle) == "" || len(tracks) == 0 {
		return AlbumImportMetadataPreviewDTO{Tracks: baseMetadataTracks(tracks)}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	metadataCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	result, err := s.albumImportMetadataEnricher.Enrich(metadataCtx, AlbumImportMetadataInput{
		AlbumTitle: strings.TrimSpace(input.AlbumTitle),
		Artist:     artist,
		Tracks:     tracks,
		SkipLyrics: true,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(metadataCtx.Err(), context.DeadlineExceeded) {
			return AlbumImportMetadataPreviewDTO{
				Tracks:        baseMetadataTracks(tracks),
				MetadataError: "外部元数据匹配超时，请继续填写专辑信息后稍后重试",
			}, nil
		}
		return AlbumImportMetadataPreviewDTO{Tracks: baseMetadataTracks(tracks), MetadataError: err.Error()}, nil
	}
	return AlbumImportMetadataPreviewDTO{
		Matched: result.MetadataSource != "", AlbumTitle: result.AlbumTitle,
		ReleaseDate: result.ReleaseDate, CoverURL: result.CoverURL, AlbumType: result.AlbumType,
		SourceURL: result.SourceURL, MetadataSource: result.MetadataSource,
		ExternalID: result.ExternalID, MatchStatus: result.MatchStatus,
		MatchConfidence: result.MatchConfidence, MetadataError: result.MetadataError,
		Genres: result.Genres, Styles: result.Styles, Labels: result.Labels, Country: result.Country, Formats: result.Formats,
		MissingArtists:  result.MissingArtists,
		MetadataSources: result.MetadataSources, Tracks: result.Tracks,
	}, nil
}

func inferCommonAlbumImportArtist(titles []string) string {
	pattern := regexp.MustCompile(`^\s*(.+?)\s+(?:-|–|—)\s+.+$`)
	prefixes := make([]string, 0, len(titles))
	for _, title := range titles {
		match := pattern.FindStringSubmatch(strings.TrimSpace(title))
		if len(match) != 2 || strings.TrimSpace(match[1]) == "" {
			return ""
		}
		prefixes = append(prefixes, strings.TrimSpace(match[1]))
	}
	if len(prefixes) < 2 {
		return ""
	}
	key := compactMusicText(prefixes[0])
	for _, prefix := range prefixes[1:] {
		if compactMusicText(prefix) != key {
			return ""
		}
	}
	return prefixes[0]
}
