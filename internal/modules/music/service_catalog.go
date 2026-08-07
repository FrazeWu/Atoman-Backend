package music

import (
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

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

	artist := model.Artist{
		Name:           name,
		LegalName:      strings.TrimSpace(req.LegalName),
		StageNamesJSON: mustMarshalStageNames(req.StageNames),
		Bio:            strings.TrimSpace(req.Bio),
		ImageURL:       strings.TrimSpace(req.ImageURL),
		Nationality:    strings.TrimSpace(req.Nationality),
		BirthPlace:     strings.TrimSpace(req.BirthPlace),
		BirthYear:      req.BirthYear,
		DeathYear:      req.DeathYear,
		ArtistForm:     normalizeArtistForm(req.ArtistForm),
		EntryStatus:    "open",
	}
	if strings.TrimSpace(req.BirthDate) != "" {
		birthDate, err := parseMusicDate(req.BirthDate, "birth_date")
		if err != nil {
			return model.Artist{}, err
		}
		artist.BirthDate = &birthDate
		artist.BirthYear = birthDate.Year()
	}
	if strings.TrimSpace(req.ActiveStartDate) != "" {
		activeStartDate, err := parseMusicDate(req.ActiveStartDate, "active_start_date")
		if err != nil {
			return model.Artist{}, err
		}
		artist.ActiveStartDate = activeStartDate
	}
	if strings.TrimSpace(req.ActiveEndDate) != "" {
		activeEndDate, err := parseMusicDate(req.ActiveEndDate, "active_end_date")
		if err != nil {
			return model.Artist{}, err
		}
		artist.ActiveEndDate = activeEndDate
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&artist).Error; err != nil {
			return err
		}
		return replaceArtistMembers(tx, artist.ID, req.Members)
	})
	return artist, err
}

func parseMusicDate(raw string, field string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, apperr.BadRequest("validation.invalid_request", field+" must use YYYY-MM-DD")
	}
	return parsed, nil
}
