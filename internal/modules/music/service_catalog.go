package music

import (
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	revisionservice "atoman/internal/service"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) CreateArtist(user authctx.CurrentUser, req CreateArtistRequest) (model.Artist, error) {
	if user.ID == uuid.Nil {
		return model.Artist{}, apperr.Unauthorized("Login required")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return model.Artist{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	memberDraft := strings.TrimSpace(req.DraftContext) == "member"

	sources, sourcesJSON := []model.MusicSource(nil), "[]"
	if !memberDraft {
		var err error
		sources, sourcesJSON, err = normalizeMusicSources(req.Sources, "")
		if err != nil {
			return model.Artist{}, err
		}
	}

	artist := model.Artist{
		Name:           name,
		Disambiguation: strings.TrimSpace(req.Disambiguation),
		DisplayName:    artistDisplayName(name, req.Disambiguation),
		LegalName:      strings.TrimSpace(req.LegalName),
		StageNamesJSON: mustMarshalStageNames(req.StageNames),
		Bio:            strings.TrimSpace(req.Bio),
		ImageURL:       strings.TrimSpace(req.ImageURL),
		Nationality:    strings.TrimSpace(req.Nationality),
		BirthPlace:     strings.TrimSpace(req.BirthPlace),
		BirthYear:      req.BirthYear,
		DeathYear:      req.DeathYear,
		ArtistForm:     normalizeArtistForm(req.ArtistForm),
		EntryStatus:    artistEntryDraft,
		CreatedBy:      &user.ID,
		SourcesJSON:    sourcesJSON,
		Sources:        sources,
	}
	if strings.TrimSpace(req.BirthDate) != "" {
		birthDate, precision, err := parsePartialDate(req.BirthDate, "birth_date")
		if err != nil {
			return model.Artist{}, err
		}
		artist.BirthDate = birthDate
		artist.BirthDatePrecision = precision
		artist.BirthYear = birthDate.Year()
	}
	if strings.TrimSpace(req.ActiveStartDate) != "" {
		activeStartDate, precision, err := parsePartialDate(req.ActiveStartDate, "active_start_date")
		if err != nil {
			return model.Artist{}, err
		}
		artist.ActiveStartDate = *activeStartDate
		artist.ActiveStartDatePrecision = precision
	}
	if strings.TrimSpace(req.ActiveEndDate) != "" {
		activeEndDate, precision, err := parsePartialDate(req.ActiveEndDate, "active_end_date")
		if err != nil {
			return model.Artist{}, err
		}
		artist.ActiveEndDate = *activeEndDate
		artist.ActiveEndDatePrecision = precision
	}
	if !memberDraft {
		if err := validateArtistPublicationFields(artist, req.Members); err != nil {
			return model.Artist{}, err
		}
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := ensureArtistDisplayNameAvailable(tx, artist.Name, artist.Disambiguation, nil); err != nil {
			return err
		}
		if err := validateArtistMemberReferences(tx, user, req.Members); err != nil {
			return err
		}
		if err := tx.Create(&artist).Error; err != nil {
			return err
		}
		if err := replaceArtistMembers(tx, artist.ID, req.Members); err != nil {
			return err
		}
		_, err := revisionservice.NewRevisionService(tx).EnsureInitialRevision("artist", artist.ID, user.ID)
		return err
	})
	return artist, err
}
