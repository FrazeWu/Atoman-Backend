package music

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AlbumImportMetadataPreviewInput struct {
	AlbumTitle  string                            `json:"albumTitle"`
	Artist      string                            `json:"artist"`
	TrackTitles []string                          `json:"trackTitles"`
	Tracks      []AlbumImportMetadataPreviewTrack `json:"tracks,omitempty"`
}

type AlbumImportMetadataPreviewTrack struct {
	Title         string `json:"title"`
	FileID        string `json:"fileId,omitempty"`
	AudioKey      string `json:"audioKey,omitempty"`
	AudioURL      string `json:"audioUrl,omitempty"`
	Origin        string `json:"origin,omitempty"`
	DiscNumber    int    `json:"discNumber,omitempty"`
	TrackNumber   int    `json:"trackNumber,omitempty"`
	OriginalTitle string `json:"originalTitle,omitempty"`
	OriginalDisc  int    `json:"originalDiscNumber,omitempty"`
	OriginalTrack int    `json:"originalTrackNumber,omitempty"`
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

// MatchAlbumImportMetadata performs the one authoritative external lookup for
// an import session and persists its result before audio processing completes.
func (s *Service) MatchAlbumImportMetadata(ctx context.Context, user authctx.CurrentUser, sessionID uuid.UUID, input AlbumImportMetadataPreviewInput) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	if s == nil || s.db == nil {
		return model.AlbumImportSession{}, errors.New("music import database is required")
	}
	var session model.AlbumImportSession
	if err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", sessionID, user.ID).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.AlbumImportSession{}, apperr.NotFound("music.import_not_found", "Import session not found")
		}
		return model.AlbumImportSession{}, err
	}
	payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	locked, _ := payload["metadata_match_locked"].(bool)
	if locked {
		return loadAlbumImportSession(s.db, sessionID, &user.ID)
	}
	preview, err := s.PreviewAlbumImportMetadata(ctx, input)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	result := AlbumImportMetadataResult{
		AlbumTitle: preview.AlbumTitle, ReleaseDate: preview.ReleaseDate, CoverURL: preview.CoverURL,
		AlbumType: preview.AlbumType, SourceURL: preview.SourceURL, MetadataSource: preview.MetadataSource,
		ExternalID: preview.ExternalID, MatchStatus: preview.MatchStatus, MatchConfidence: preview.MatchConfidence,
		MetadataError: preview.MetadataError, Genres: preview.Genres, Styles: preview.Styles, Labels: preview.Labels,
		Country: preview.Country, Formats: preview.Formats, MissingArtists: preview.MissingArtists,
		MetadataSources: preview.MetadataSources, Tracks: preview.Tracks,
	}
	if result.MatchStatus == "" {
		result.MatchStatus = model.MusicMatchUnmatched
	}
	if err := s.persistAlbumImportMetadataMatch(ctx, &session, result); err != nil {
		return model.AlbumImportSession{}, err
	}
	return loadAlbumImportSession(s.db, sessionID, &user.ID)
}

func (s *Service) persistAlbumImportMetadataMatch(ctx context.Context, session *model.AlbumImportSession, result AlbumImportMetadataResult) error {
	if err := (&MediaImportProcessor{db: s.db}).persistDerivedMetadataResult(ctx, session, nil, result); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var latest model.AlbumImportSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&latest, "id = ?", session.ID).Error; err != nil {
			return err
		}
		payload, err := readAlbumImportPayloadMap(latest.PayloadJSON)
		if err != nil {
			return err
		}
		payload["metadata_match_started"] = true
		payload["metadata_match_locked"] = true
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		return tx.Model(&latest).Update("payload_json", string(encoded)).Error
	})
}

func (s *Service) PreviewAlbumImportMetadata(ctx context.Context, input AlbumImportMetadataPreviewInput) (AlbumImportMetadataPreviewDTO, error) {
	artist := strings.TrimSpace(input.Artist)
	if artist == "" {
		artist = inferCommonAlbumImportArtist(input.TrackTitles)
	}
	tracks := make([]AlbumImportMetadataTrack, 0, len(input.Tracks)+len(input.TrackTitles))
	if len(input.Tracks) > 0 {
		for index, previewTrack := range input.Tracks {
			title := strings.TrimSpace(previewTrack.Title)
			if title == "" {
				continue
			}
			discNumber := previewTrack.DiscNumber
			if discNumber <= 0 {
				discNumber = 1
			}
			trackNumber := previewTrack.TrackNumber
			if trackNumber <= 0 {
				trackNumber = index + 1
			}
			tracks = append(tracks, AlbumImportMetadataTrack{
				Title: normalizeAlbumImportTrackTitle(previewTrack.Title, artist), OriginalTitle: firstNonEmptyMusicValue(previewTrack.OriginalTitle, previewTrack.Title),
				FileID: previewTrack.FileID, AudioKey: previewTrack.AudioKey, AudioURL: previewTrack.AudioURL,
				Origin: previewTrack.Origin, DiscNumber: discNumber, TrackNumber: trackNumber,
				OriginalDisc: previewTrack.OriginalDisc, OriginalTrack: previewTrack.OriginalTrack,
			})
		}
	} else {
		for index, title := range input.TrackTitles {
			title = strings.TrimSpace(title)
			if title == "" {
				continue
			}
			tracks = append(tracks, AlbumImportMetadataTrack{
				Title: normalizeAlbumImportTrackTitle(title, artist), OriginalTitle: title, TrackNumber: index + 1, Origin: "local_preview:" + strconv.Itoa(index+1),
			})
		}
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
		Matched: result.MatchStatus == model.MusicMatchMatched || result.MatchStatus == model.MusicMatchManual, AlbumTitle: result.AlbumTitle,
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
	pattern := regexp.MustCompile(`^\s*(.+?)\s*(?:-|–|—)\s*(.+?)\s*$`)
	prefixes := make([]string, 0, len(titles))
	for _, title := range titles {
		match := pattern.FindStringSubmatch(strings.TrimSpace(title))
		if len(match) != 3 || strings.TrimSpace(match[1]) == "" || strings.TrimSpace(match[2]) == "" {
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
