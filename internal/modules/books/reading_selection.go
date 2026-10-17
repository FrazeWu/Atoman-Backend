package books

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"golang.org/x/text/language"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SetBookReadingSelectionInput struct {
	AssetID    uuid.UUID `json:"asset_id"`
	RevisionID uuid.UUID `json:"revision_id"`
}

type BookReadingRecommendation struct {
	WorkID     uuid.UUID                   `json:"work_id"`
	Selection  *model.BookReadingSelection `json:"selection"`
	Asset      *model.BookAsset            `json:"asset,omitempty"`
	RevisionID *uuid.UUID                  `json:"revision_id,omitempty"`
	Available  bool                        `json:"available"`
	Source     string                      `json:"source"`
	Reason     string                      `json:"reason,omitempty"`
}

func (s *Service) SetBookReadingSelection(ctx context.Context, user authctx.CurrentUser, workID uuid.UUID, input SetBookReadingSelectionInput) (model.BookReadingSelection, error) {
	if user.ID == uuid.Nil {
		return model.BookReadingSelection{}, apperr.Unauthorized("Login required")
	}
	var result model.BookReadingSelection
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		// GetBookWork holds shared locks through this transaction, including redirects.
		work, err := service.GetBookWork(ctx, workID)
		if err != nil {
			return err
		}
		canonicalID := uuid.MustParse(work.ID)
		revision, err := service.readableBookRevision(ctx, input.RevisionID)
		if err != nil {
			return err
		}
		var asset model.BookAsset
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND work_id = ? AND status = ?", input.AssetID, canonicalID, model.BookResourceStatusActive).First(&asset).Error; err != nil {
			return publicBookNotFound(err)
		}
		if revision.AssetID != asset.ID {
			return apperr.BadRequest("books.selection_revision_mismatch", "Revision does not belong to this ebook")
		}
		result = model.BookReadingSelection{UserID: user.ID, WorkID: canonicalID, AssetID: asset.ID, RevisionID: revision.ID, UpdatedAt: time.Now().UTC()}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "work_id"}}, DoUpdates: clause.AssignmentColumns([]string{"asset_id", "revision_id", "updated_at"})}).Create(&result).Error
	})
	return result, err
}

func (s *Service) GetBookReadingSelection(ctx context.Context, user authctx.CurrentUser, workID uuid.UUID) (*model.BookReadingSelection, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	var result *model.BookReadingSelection
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, workID)
		if err != nil {
			return err
		}
		result, err = service.bookReadingSelection(ctx, user.ID, uuid.MustParse(work.ID))
		return err
	})
	return result, err
}

func (s *Service) bookReadingSelection(ctx context.Context, userID, workID uuid.UUID) (*model.BookReadingSelection, error) {
	var selection model.BookReadingSelection
	err := s.db.WithContext(ctx).Where("user_id = ? AND work_id = ?", userID, workID).First(&selection).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &selection, err
}

func bookReadingFormats(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{"epub", "pdf", "txt"}, nil
	}
	result := make([]string, 0, len(raw))
	for _, format := range raw {
		format = strings.ToLower(strings.TrimSpace(format))
		switch format {
		case "epub", "pdf", "txt", "cbz", "cbr":
		default:
			return nil, apperr.BadRequest("books.invalid_format", "Unsupported reader format")
		}
		if !containsBookFormat(result, format) {
			result = append(result, format)
		}
	}
	return result, nil
}

func containsBookFormat(formats []string, format string) bool {
	for _, value := range formats {
		if value == format {
			return true
		}
	}
	return false
}

func bookLanguageRank(candidate, preferred string) int {
	if candidate == preferred {
		return 0
	}
	candidateTag, candidateErr := language.Parse(candidate)
	preferredTag, preferredErr := language.Parse(preferred)
	if candidateErr == nil && preferredErr == nil && candidate != "und" && preferred != "und" {
		base, _ := candidateTag.Base()
		preferredBase, _ := preferredTag.Base()
		if base == preferredBase {
			return 1
		}
	}
	if candidate == "und" {
		return 2
	}
	return 3
}

// A recommendation never writes a selection. Only an explicit reader choice pins a revision.
func (s *Service) RecommendBookReading(ctx context.Context, userID uuid.UUID, workID uuid.UUID, rawLanguage string, rawFormats []string) (BookReadingRecommendation, error) {
	preferred, err := normalizeBookResourceLanguage(rawLanguage)
	if err != nil {
		return BookReadingRecommendation{}, err
	}
	formats, err := bookReadingFormats(rawFormats)
	if err != nil {
		return BookReadingRecommendation{}, err
	}
	var result BookReadingRecommendation
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, workID)
		if err != nil {
			return err
		}
		workID = uuid.MustParse(work.ID)
		result = BookReadingRecommendation{WorkID: workID, Source: "none", Reason: "no_readable_asset"}
		if userID != uuid.Nil {
			selection, err := service.bookReadingSelection(ctx, userID, workID)
			if err != nil {
				return err
			}
			if selection != nil {
				result.Selection = selection
				result.RevisionID = &selection.RevisionID
				result.Source = "personal"
				result.Reason = "selected_unavailable"
				_, err := service.readableBookRevision(ctx, selection.RevisionID)
				if err != nil {
					if apperr.FromError(err).HTTPStatus == 404 {
						return nil
					}
					return err
				}
				var asset model.BookAsset
				if err := tx.First(&asset, "id = ?", selection.AssetID).Error; err != nil {
					return err
				}
				result.Asset = &asset
				if !containsBookFormat(formats, asset.Format) {
					result.Reason = "selected_format_unsupported"
					return nil
				}
				result.Available = true
				result.Reason = ""
				return nil
			}
		}
		assets, err := service.ListBookResources(ctx, workID)
		if err != nil {
			return err
		}
		var defaults []model.BookDefaultAsset
		if err := tx.Where("work_id = ?", workID).Find(&defaults).Error; err != nil {
			return err
		}
		defaultIDs := map[uuid.UUID]bool{}
		for _, value := range defaults {
			defaultIDs[value.AssetID] = true
		}
		candidates := make([]model.BookAsset, 0, len(assets))
		for _, asset := range assets {
			if containsBookFormat(formats, asset.Format) {
				candidates = append(candidates, asset)
			}
		}
		formatRank := func(format string) int {
			for i, value := range formats {
				if value == format {
					return i
				}
			}
			return len(formats)
		}
		// There are no quality votes yet; do not invent a quality score. Use stable tie-breaking.
		sort.Slice(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if x, y := bookLanguageRank(a.Language, preferred), bookLanguageRank(b.Language, preferred); x != y {
				return x < y
			}
			if defaultIDs[a.ID] != defaultIDs[b.ID] {
				return defaultIDs[a.ID]
			}
			if x, y := formatRank(a.Format), formatRank(b.Format); x != y {
				return x < y
			}
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
			return a.ID.String() < b.ID.String()
		})
		if len(candidates) == 0 {
			return nil
		}
		asset := candidates[0]
		result.Asset = &asset
		result.RevisionID = asset.CurrentRevisionID
		result.Available = true
		result.Reason = ""
		result.Source = "automatic"
		if defaultIDs[asset.ID] {
			result.Source = "community_default"
		}
		return nil
	})
	return result, err
}
