package books

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CreateBookDiscussionPassageInput struct {
	Anchor BookReadingMarkAnchor `json:"anchor"`
}

type BookDiscussionPassageDTO struct {
	ID           uuid.UUID             `json:"id"`
	RevisionID   uuid.UUID             `json:"revision_id"`
	Anchor       BookReadingMarkAnchor `json:"anchor"`
	TargetKind   string                `json:"target_kind"`
	CommentsPath string                `json:"comments_path"`
}

func bookDiscussionPassageDTO(passage model.BookDiscussionPassage) (BookDiscussionPassageDTO, error) {
	result := BookDiscussionPassageDTO{ID: passage.ID, RevisionID: passage.RevisionID, TargetKind: "book_passage",
		CommentsPath: "/api/v1/discussions/book_passage/" + passage.ID.String() + "/comments"}
	err := json.Unmarshal([]byte(passage.AnchorJSON), &result.Anchor)
	return result, err
}

func (s *Service) CreateBookDiscussionPassage(ctx context.Context, user authctx.CurrentUser, revisionID uuid.UUID, input CreateBookDiscussionPassageInput) (BookDiscussionPassageDTO, error) {
	if user.ID == uuid.Nil {
		return BookDiscussionPassageDTO{}, apperr.Unauthorized("Login required")
	}
	var result BookDiscussionPassageDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		var reader model.User
		if err := tx.Where("uuid = ? AND is_active = true", user.ID).First(&reader).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.Forbidden("books.discussion_account_inactive", "An active account is required")
			}
			return err
		}
		if _, err := service.readableBookRevision(ctx, revisionID); err != nil {
			return err
		}
		format, err := service.bookReadingMarkFormat(ctx, revisionID)
		if err != nil {
			return err
		}
		// Validate a selected range, without reading or publishing any private mark.
		anchor := normalizeBookReadingMarkAnchor(input.Anchor)
		if err := validateBookReadingMarkAnchor(anchor, format, "highlight"); err != nil {
			return err
		}
		encoded, _ := json.Marshal(anchor)
		digest := sha256.Sum256(encoded)
		passage := model.BookDiscussionPassage{RevisionID: revisionID, AnchorJSON: string(encoded), AnchorDigest: hex.EncodeToString(digest[:]), CreatedBy: user.ID}
		inserted := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "revision_id"}, {Name: "anchor_digest"}}, DoNothing: true}).Create(&passage)
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected == 0 {
			var existing model.BookDiscussionPassage
			if err := tx.Where("revision_id = ? AND anchor_digest = ?", revisionID, passage.AnchorDigest).First(&existing).Error; err != nil {
				return err
			}
			passage = existing
		}
		result, err = bookDiscussionPassageDTO(passage)
		return err
	})
	return result, err
}

func (s *Service) GetBookDiscussionPassage(ctx context.Context, passageID uuid.UUID) (BookDiscussionPassageDTO, error) {
	var passage model.BookDiscussionPassage
	if err := s.db.WithContext(ctx).First(&passage, "id = ?", passageID).Error; err != nil {
		return BookDiscussionPassageDTO{}, publicBookNotFound(err)
	}
	if _, err := s.readableBookRevision(ctx, passage.RevisionID); err != nil {
		return BookDiscussionPassageDTO{}, err
	}
	return bookDiscussionPassageDTO(passage)
}

type BookDiscussionPassageList struct {
	Items  []BookDiscussionPassageDTO `json:"items"`
	Total  int64                      `json:"total"`
	Limit  int                        `json:"limit"`
	Offset int                        `json:"offset"`
}

func (s *Service) ListBookDiscussionPassages(ctx context.Context, revisionID uuid.UUID, limit, offset int) (BookDiscussionPassageList, error) {
	limit, offset = normalizeCatalogPagination(limit, offset)
	result := BookDiscussionPassageList{Items: []BookDiscussionPassageDTO{}, Limit: limit, Offset: offset}
	if _, err := s.readableBookRevision(ctx, revisionID); err != nil {
		return result, err
	}
	query := s.db.WithContext(ctx).Model(&model.BookDiscussionPassage{}).Where("revision_id = ?", revisionID)
	if err := query.Count(&result.Total).Error; err != nil {
		return result, err
	}
	var passages []model.BookDiscussionPassage
	if err := query.Order("created_at ASC, id ASC").Limit(limit).Offset(offset).Find(&passages).Error; err != nil {
		return result, err
	}
	for _, passage := range passages {
		item, err := bookDiscussionPassageDTO(passage)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}
