package books

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SaveBookReadingMarkInput struct {
	BaseVersion int64                 `json:"base_version"`
	Kind        string                `json:"kind"`
	Anchor      BookReadingMarkAnchor `json:"anchor"`
	Label       string                `json:"label"`
	Quote       string                `json:"quote"`
	Note        string                `json:"note"`
	Color       string                `json:"color"`
	Style       string                `json:"style"`
}

type BookReadingMarkDTO struct {
	model.BookReadingMark
	Anchor BookReadingMarkAnchor `json:"anchor"`
}

type BookHighlightGroup struct {
	ID        uuid.UUID             `json:"id"`
	MemberIDs []uuid.UUID           `json:"member_ids"`
	Anchor    BookReadingMarkAnchor `json:"anchor"`
	Color     string                `json:"color"`
	Style     string                `json:"style"`
}

type BookReadingMarkTombstone struct {
	ID        uuid.UUID `json:"id"`
	Version   int64     `json:"version"`
	DeletedAt time.Time `json:"deleted_at"`
}

type BookReadingMarksResult struct {
	Items           []BookReadingMarkDTO       `json:"items"`
	HighlightGroups []BookHighlightGroup       `json:"highlight_groups"`
	Deleted         []BookReadingMarkTombstone `json:"deleted"`
}

func bookReadingMarkDTO(mark model.BookReadingMark) (BookReadingMarkDTO, error) {
	result := BookReadingMarkDTO{BookReadingMark: mark}
	err := json.Unmarshal([]byte(mark.AnchorJSON), &result.Anchor)
	return result, err
}

func normalizeBookReadingMark(input SaveBookReadingMarkInput, format string) (SaveBookReadingMarkInput, error) {
	input.Kind = strings.TrimSpace(input.Kind)
	input.Label = strings.TrimSpace(input.Label)
	input.Color = strings.ToLower(strings.TrimSpace(input.Color))
	input.Style = strings.ToLower(strings.TrimSpace(input.Style))
	input.Anchor = normalizeBookReadingMarkAnchor(input.Anchor)
	if input.BaseVersion < 0 || (input.Kind != "bookmark" && input.Kind != "highlight") {
		return input, apperr.BadRequest("books.invalid_reading_mark", "Use bookmark or highlight and a nonnegative base version")
	}
	if len(input.Label) > 255 || len(input.Quote) > 50000 || len(input.Note) > bookReadingNotesMaxLength {
		return input, apperr.BadRequest("books.invalid_reading_mark", "Reading mark text is too long")
	}
	if input.Kind == "highlight" {
		if strings.TrimSpace(input.Quote) == "" {
			return input, apperr.BadRequest("books.invalid_reading_mark", "A highlight requires its selected text")
		}
		if input.Color == "" {
			input.Color = model.BookAnnotationColorYellow
		}
		if input.Style == "" {
			input.Style = model.BookAnnotationStyleHighlight
		}
		if !isValidAnnotationColor(input.Color) || !isValidAnnotationStyle(input.Style) {
			return input, apperr.BadRequest("books.invalid_reading_mark", "Unsupported highlight color or style")
		}
	} else if input.Color != "" || input.Style != "" || input.Quote != "" {
		return input, apperr.BadRequest("books.invalid_reading_mark", "A bookmark stores a position and label, not highlight styling or text")
	}
	return input, validateBookReadingMarkAnchor(input.Anchor, format, input.Kind)
}

func normalizeBookReadingMarkAnchor(anchor BookReadingMarkAnchor) BookReadingMarkAnchor {
	anchor.EPUBStartCFI = strings.TrimSpace(anchor.EPUBStartCFI)
	anchor.EPUBEndCFI = strings.TrimSpace(anchor.EPUBEndCFI)
	return anchor
}

func (s *Service) bookReadingMarkFormat(ctx context.Context, revisionID uuid.UUID) (string, error) {
	var asset model.BookAsset
	err := s.db.WithContext(ctx).Unscoped().Joins("JOIN book_asset_revisions r ON r.asset_id = book_assets.id").Where("r.id = ?", revisionID).First(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", apperr.NotFound("books.revision_not_found", "Book revision not found")
	}
	return asset.Format, err
}

func bookReadingMarkConflict(mark model.BookReadingMark) error {
	err := apperr.Conflict("books.reading_mark_conflict", "Reading mark changed; reload before editing")
	err.Details["current_version"] = mark.Version
	err.Details["deleted"] = mark.DeletedAt.Valid
	return err
}

func (s *Service) SaveBookReadingMark(ctx context.Context, user authctx.CurrentUser, revisionID, markID uuid.UUID, input SaveBookReadingMarkInput) (BookReadingMarkDTO, error) {
	if user.ID == uuid.Nil {
		return BookReadingMarkDTO{}, apperr.Unauthorized("Login required")
	}
	if markID == uuid.Nil || revisionID == uuid.Nil {
		return BookReadingMarkDTO{}, apperr.BadRequest("books.invalid_reading_mark", "Revision and client-generated mark UUID are required")
	}
	var result BookReadingMarkDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		var stored model.BookReadingMark
		load := func() error {
			return tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND revision_id = ?", markID, user.ID, revisionID).First(&stored).Error
		}
		err := load()
		isNew := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !isNew {
			return err
		}
		if isNew {
			if input.BaseVersion != 0 {
				return apperr.NotFound("books.reading_mark_not_found", "Reading mark not found")
			}
			if _, err := service.readableBookRevision(ctx, revisionID); err != nil {
				return err
			}
		}
		format, err := service.bookReadingMarkFormat(ctx, revisionID)
		if err != nil {
			return err
		}
		input, err = normalizeBookReadingMark(input, format)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(input.Anchor)
		if err != nil {
			return err
		}
		candidate := model.BookReadingMark{Base: model.Base{ID: markID}, UserID: user.ID, RevisionID: revisionID, Kind: input.Kind, AnchorJSON: string(encoded), Label: input.Label, Quote: input.Quote, Note: input.Note, Color: input.Color, Style: input.Style, Version: 1}
		if isNew {
			created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
			if created.Error != nil {
				return created.Error
			}
			if created.RowsAffected == 1 {
				result, err = bookReadingMarkDTO(candidate)
				return err
			}
			if err := load(); err != nil {
				return privateBookReadingMarkError(err)
			}
		}
		if stored.DeletedAt.Valid {
			return bookReadingMarkConflict(stored)
		}
		old, err := bookReadingMarkDTO(stored)
		if err != nil {
			return err
		}
		oldAnchor, _ := json.Marshal(old.Anchor)
		identical := string(oldAnchor) == string(encoded) && stored.Kind == input.Kind && stored.Label == input.Label && stored.Quote == input.Quote && stored.Note == input.Note && stored.Color == input.Color && stored.Style == input.Style
		// An exact retry of the previous successful PUT is harmless. A stale
		// different payload must never overwrite an intervening edit.
		if identical && (input.BaseVersion == stored.Version || input.BaseVersion == stored.Version-1) {
			result = old
			return nil
		}
		if input.BaseVersion != stored.Version {
			return bookReadingMarkConflict(stored)
		}
		candidate.Version = stored.Version + 1
		if err := tx.Model(&stored).Select("kind", "anchor_json", "label", "quote", "note", "color", "style", "version", "updated_at").Updates(&candidate).Error; err != nil {
			return err
		}
		if err := load(); err != nil {
			return err
		}
		result, err = bookReadingMarkDTO(stored)
		return err
	})
	return result, err
}

func privateBookReadingMarkError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("books.reading_mark_not_found", "Reading mark not found")
	}
	return err
}

func (s *Service) DeleteBookReadingMark(ctx context.Context, user authctx.CurrentUser, revisionID, markID uuid.UUID, baseVersion int64) (BookReadingMarkTombstone, error) {
	if user.ID == uuid.Nil {
		return BookReadingMarkTombstone{}, apperr.Unauthorized("Login required")
	}
	if baseVersion < 1 {
		return BookReadingMarkTombstone{}, apperr.BadRequest("books.invalid_reading_mark", "A positive base version is required")
	}
	var result BookReadingMarkTombstone
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var mark model.BookReadingMark
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND revision_id = ?", markID, user.ID, revisionID).First(&mark).Error; err != nil {
			return privateBookReadingMarkError(err)
		}
		if mark.DeletedAt.Valid {
			if mark.Version != baseVersion+1 {
				return bookReadingMarkConflict(mark)
			}
		} else {
			if mark.Version != baseVersion {
				return bookReadingMarkConflict(mark)
			}
			mark.Version++
			mark.DeletedAt = gorm.DeletedAt{Time: time.Now().UTC().Truncate(time.Microsecond), Valid: true}
			if err := tx.Unscoped().Model(&mark).Select("version", "deleted_at", "updated_at").Updates(&mark).Error; err != nil {
				return err
			}
		}
		result = BookReadingMarkTombstone{ID: mark.ID, Version: mark.Version, DeletedAt: mark.DeletedAt.Time.UTC()}
		return nil
	})
	return result, err
}

func (s *Service) ListBookReadingMarks(ctx context.Context, user authctx.CurrentUser, revisionID uuid.UUID) (BookReadingMarksResult, error) {
	result := BookReadingMarksResult{Items: []BookReadingMarkDTO{}, HighlightGroups: []BookHighlightGroup{}, Deleted: []BookReadingMarkTombstone{}}
	if user.ID == uuid.Nil {
		return result, apperr.Unauthorized("Login required")
	}
	var marks []model.BookReadingMark
	if err := s.db.WithContext(ctx).Unscoped().Where("user_id = ? AND revision_id = ?", user.ID, revisionID).Order("created_at ASC, id ASC").Find(&marks).Error; err != nil {
		return result, err
	}
	if len(marks) == 0 {
		if _, err := s.GetBookCloudReadingState(ctx, user, revisionID); err != nil {
			return result, err
		}
	}
	format, err := s.bookReadingMarkFormat(ctx, revisionID)
	if err != nil {
		return result, err
	}
	for _, mark := range marks {
		if mark.DeletedAt.Valid {
			result.Deleted = append(result.Deleted, BookReadingMarkTombstone{ID: mark.ID, Version: mark.Version, DeletedAt: mark.DeletedAt.Time.UTC()})
			continue
		}
		item, err := bookReadingMarkDTO(mark)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	result.HighlightGroups = groupBookHighlights(result.Items, format)
	return result, nil
}

func groupBookHighlights(items []BookReadingMarkDTO, format string) []BookHighlightGroup {
	groups := make([]BookHighlightGroup, 0)
	visited := make([]bool, len(items))
	for i, item := range items {
		if visited[i] || item.Kind != "highlight" {
			continue
		}
		group := BookHighlightGroup{ID: item.ID, MemberIDs: []uuid.UUID{item.ID}, Anchor: item.Anchor, Color: item.Color, Style: item.Style}
		// Copy rectangles: derived unions must not mutate the original mark.
		group.Anchor.PDFRects = append([]BookPDFRect(nil), item.Anchor.PDFRects...)
		visited[i] = true
		for changed := true; changed; {
			changed = false
			for j, other := range items {
				if visited[j] || other.Kind != "highlight" || other.Color != group.Color || other.Style != group.Style {
					continue
				}
				if bookMarkAnchorsOverlap(group.Anchor, other.Anchor, format) {
					group.Anchor = unionBookMarkAnchors(group.Anchor, other.Anchor, format)
					group.MemberIDs = append(group.MemberIDs, other.ID)
					visited[j] = true
					changed = true
				}
			}
		}
		groups = append(groups, group)
	}
	return groups
}
