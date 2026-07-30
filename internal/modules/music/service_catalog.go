package music

import (
	"errors"
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

func (s *Service) UpdateArtist(user authctx.CurrentUser, artistID uuid.UUID, req UpdateArtistRequest) (model.Artist, error) {
	if user.ID == uuid.Nil {
		return model.Artist{}, apperr.Unauthorized("Login required")
	}
	artist, err := s.repo.GetArtist(artistID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Artist{}, apperr.NotFound("music.artist_not_found", "Artist not found")
		}
		return model.Artist{}, err
	}

	updates := map[string]any{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return model.Artist{}, apperr.BadRequest("validation.invalid_request", "name is required")
		}
		updates["name"] = name
	}
	if req.Bio != nil {
		updates["bio"] = strings.TrimSpace(*req.Bio)
	}
	if req.ImageURL != nil {
		updates["image_url"] = strings.TrimSpace(*req.ImageURL)
	}
	if req.Nationality != nil {
		updates["nationality"] = strings.TrimSpace(*req.Nationality)
	}
	if req.BirthDate != nil {
		if strings.TrimSpace(*req.BirthDate) == "" {
			updates["birth_date"] = nil
		} else {
			birthDate, err := parseMusicDate(*req.BirthDate, "birth_date")
			if err != nil {
				return model.Artist{}, err
			}
			updates["birth_date"] = birthDate
		}
	}
	if req.BirthYear != nil {
		updates["birth_year"] = *req.BirthYear
	}
	if req.DeathYear != nil {
		updates["death_year"] = *req.DeathYear
	}
	if len(updates) == 0 {
		return artist, nil
	}

	if err := s.repo.UpdateArtist(&artist, updates); err != nil {
		return model.Artist{}, err
	}
	return s.repo.GetArtist(artistID)
}

func parseMusicDate(raw string, field string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, apperr.BadRequest("validation.invalid_request", field+" must use YYYY-MM-DD")
	}
	return parsed, nil
}
