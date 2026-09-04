package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AppleSongPreview struct {
	PreviewURL         string `json:"preview_url,omitempty"`
	StoreURL           string `json:"store_url"`
	Attribution        string `json:"attribution"`
	MaxDurationSeconds int    `json:"max_duration_seconds"`
}

type applePreviewLookupResponse struct {
	Results []struct {
		TrackID    int64  `json:"trackId"`
		PreviewURL string `json:"previewUrl"`
	} `json:"results"`
}

func (s *Service) AppleSongPreview(ctx context.Context, songID uuid.UUID) (AppleSongPreview, error) {
	var link model.MusicCatalogLink
	err := s.db.Where("provider = ? AND entity_type = ? AND entity_id = ?", appleCatalogProvider, "song", songID).First(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return AppleSongPreview{}, apperr.NotFound("music.apple_preview_not_found", "Apple Music preview not found")
	}
	if err != nil {
		return AppleSongPreview{}, err
	}

	endpoint, err := url.Parse(s.applePreviewBaseURL)
	if err != nil {
		return AppleSongPreview{}, fmt.Errorf("parse Apple lookup URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("id", link.ExternalID)
	query.Set("country", link.Storefront)
	query.Set("entity", "song")
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return AppleSongPreview{}, fmt.Errorf("create Apple lookup request: %w", err)
	}
	response, err := s.applePreviewHTTPClient.Do(request)
	if err != nil {
		return AppleSongPreview{}, fmt.Errorf("lookup Apple song preview: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return AppleSongPreview{}, fmt.Errorf("lookup Apple song preview: unexpected status %d", response.StatusCode)
	}

	var lookup applePreviewLookupResponse
	if err := json.NewDecoder(response.Body).Decode(&lookup); err != nil {
		return AppleSongPreview{}, fmt.Errorf("decode Apple song preview: %w", err)
	}
	result := AppleSongPreview{
		StoreURL: link.URL, Attribution: "试听由 iTunes 提供", MaxDurationSeconds: 30,
	}
	for _, item := range lookup.Results {
		if strconv.FormatInt(item.TrackID, 10) == link.ExternalID {
			result.PreviewURL = strings.TrimSpace(item.PreviewURL)
			break
		}
	}
	return result, nil
}
