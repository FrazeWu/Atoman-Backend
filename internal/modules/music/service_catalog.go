package music

import (
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
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
		Name:        name,
		Bio:         strings.TrimSpace(req.Bio),
		ImageURL:    strings.TrimSpace(req.ImageURL),
		Nationality: strings.TrimSpace(req.Nationality),
		BirthYear:   req.BirthYear,
		DeathYear:   req.DeathYear,
		ArtistForm:  "person",
		EntryStatus: "open",
	}
	if strings.TrimSpace(req.BirthDate) != "" {
		birthDate, err := parseMusicDate(req.BirthDate, "birth_date")
		if err != nil {
			return model.Artist{}, err
		}
		artist.BirthDate = &birthDate
	}
	return s.repo.CreateArtist(artist)
}

func parseMusicDate(raw string, field string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, apperr.BadRequest("validation.invalid_request", field+" must use YYYY-MM-DD")
	}
	return parsed, nil
}
