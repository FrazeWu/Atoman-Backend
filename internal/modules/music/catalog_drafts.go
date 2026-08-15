package music

import (
	"encoding/json"
	"net/url"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/partialdate"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	artistEntryDraft = "draft"
	artistEntryOpen  = "open"
)

func normalizeMusicSources(sources []Source, legacy string) ([]model.MusicSource, string, error) {
	if len(sources) == 0 && strings.TrimSpace(legacy) != "" {
		value := strings.TrimSpace(legacy)
		if isHTTPMusicSource(value) {
			sources = []Source{{URL: value}}
		} else {
			sources = []Source{{Title: value}}
		}
	}
	normalized := make([]model.MusicSource, 0, len(sources))
	for _, source := range sources {
		title := strings.TrimSpace(source.Title)
		url := strings.TrimSpace(source.URL)
		if title == "" && url == "" {
			continue
		}
		sourceType := "text"
		if url != "" {
			sourceType = "url"
		}
		normalized = append(normalized, model.MusicSource{Type: sourceType, Title: title, URL: url})
	}
	if len(normalized) == 0 {
		return nil, "", apperr.BadRequest("validation.invalid_request", "at least one source is required")
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, "", err
	}
	return normalized, string(encoded), nil
}

func isHTTPMusicSource(value string) bool {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func artistDisplayName(name, disambiguation string) string {
	displayName := strings.TrimSpace(name)
	if value := strings.TrimSpace(disambiguation); value != "" {
		displayName += "（" + value + "）"
	}
	return displayName
}

func canUseArtistDraft(artist model.Artist, user authctx.CurrentUser) bool {
	return artist.EntryStatus != artistEntryDraft || (artist.CreatedBy != nil && *artist.CreatedBy == user.ID)
}

func validateArtistPublicationFields(artist model.Artist, members []ArtistMemberPayload) error {
	if normalizeArtistForm(artist.ArtistForm) == "group" {
		if (artist.ActiveStartDate.IsZero() && artist.ActiveStartDatePrecision != partialdate.Unknown) || len(members) < 2 {
			return apperr.BadRequest("validation.invalid_request", "group artists require a start date and at least two members")
		}
		return nil
	}
	if strings.TrimSpace(artist.ImageURL) == "" || strings.TrimSpace(artist.LegalName) == "" || strings.TrimSpace(artist.Nationality) == "" || (artist.BirthDate == nil && artist.BirthDatePrecision != partialdate.Unknown) {
		return apperr.BadRequest("validation.invalid_request", "artist portrait, legal name, nationality and birth date are required")
	}
	return nil
}

func validateArtistMemberReferences(tx *gorm.DB, user authctx.CurrentUser, members []ArtistMemberPayload) error {
	for _, member := range members {
		memberID, err := uuid.Parse(strings.TrimSpace(member.ArtistID))
		if err != nil {
			return apperr.BadRequest("validation.invalid_request", "members.artist_id must be a valid UUID")
		}
		var artist model.Artist
		if err := tx.First(&artist, "id = ?", memberID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.artist_not_found", "Artist member not found")
			}
			return err
		}
		if !canUseArtistDraft(artist, user) {
			return apperr.NotFound("music.artist_not_found", "Artist member not found")
		}
	}
	return nil
}
