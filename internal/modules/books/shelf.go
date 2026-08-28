package books

import (
	"context"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const bookShelfNoteMaxLength = 5000

type SaveBookShelfInput struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

type BookShelfItemDTO struct {
	ID        string            `json:"id"`
	WorkID    string            `json:"work_id"`
	Status    string            `json:"status"`
	Note      string            `json:"note,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
	Work      BookPublicWorkDTO `json:"work"`
}

type BookShelfListResult struct {
	Items  []BookShelfItemDTO `json:"items"`
	Total  int64              `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

type BookContinueReadingDTO struct {
	AssetID          string     `json:"asset_id"`
	Title            string     `json:"title"`
	Author           string     `json:"author,omitempty"`
	FileName         string     `json:"file_name"`
	Format           string     `json:"format"`
	ProcessingStatus string     `json:"processing_status"`
	ReadingPercent   float64    `json:"reading_percent"`
	LastReadAt       *time.Time `json:"last_read_at,omitempty"`
}

func (s *Service) SaveBookShelf(user authctx.CurrentUser, workID uuid.UUID, input SaveBookShelfInput) (BookShelfItemDTO, error) {
	if user.ID == uuid.Nil {
		return BookShelfItemDTO{}, apperr.Unauthorized("Login required")
	}
	if err := validateBookShelfInput(input); err != nil {
		return BookShelfItemDTO{}, err
	}
	work, err := s.GetPublicWork(context.Background(), workID)
	if err != nil {
		return BookShelfItemDTO{}, err
	}
	var shelf model.UserBookShelf
	err = s.db.Where("user_id = ? AND work_id = ?", user.ID, workID).First(&shelf).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		shelf = model.UserBookShelf{UserID: user.ID, WorkID: workID}
	} else if err != nil {
		return BookShelfItemDTO{}, err
	}
	shelf.Status = input.Status
	shelf.Note = strings.TrimSpace(input.Note)
	if shelf.ID == uuid.Nil {
		if err := s.db.Create(&shelf).Error; err != nil {
			return BookShelfItemDTO{}, err
		}
	} else if err := s.db.Model(&shelf).Select("status", "note", "updated_at").Updates(&shelf).Error; err != nil {
		return BookShelfItemDTO{}, err
	}
	return buildBookShelfItemDTO(shelf, work), nil
}

func (s *Service) ListBookShelf(ctx context.Context, user authctx.CurrentUser, status string, limit, offset int) ([]BookShelfItemDTO, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	if status != "" {
		if err := validateBookShelfStatus(status); err != nil {
			return nil, 0, err
		}
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Model(&model.UserBookShelf{}).Where("user_id = ?", user.ID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var shelves []model.UserBookShelf
	if err := query.Order("updated_at DESC").Offset(offset).Limit(limit).Find(&shelves).Error; err != nil {
		return nil, 0, err
	}
	items := make([]BookShelfItemDTO, 0, len(shelves))
	for _, shelf := range shelves {
		work, err := s.GetPublicWork(ctx, shelf.WorkID)
		if err != nil {
			if isPublicBookNotFound(err) {
				continue
			}
			return nil, 0, err
		}
		items = append(items, buildBookShelfItemDTO(shelf, work))
	}
	return items, total, nil
}

func (s *Service) DeleteBookShelf(user authctx.CurrentUser, workID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if workID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "workId must be a valid UUID")
	}
	return s.db.Unscoped().Where("user_id = ? AND work_id = ?", user.ID, workID).Delete(&model.UserBookShelf{}).Error
}

func (s *Service) ListContinueReading(ctx context.Context, user authctx.CurrentUser, limit int) ([]BookContinueReadingDTO, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	type row struct {
		AssetID          uuid.UUID  `gorm:"column:asset_id"`
		Title            string     `gorm:"column:title"`
		Author           string     `gorm:"column:author"`
		FileName         string     `gorm:"column:file_name"`
		Format           string     `gorm:"column:format"`
		ProcessingStatus string     `gorm:"column:processing_status"`
		ReadingPercent   float64    `gorm:"column:reading_percent"`
		LastReadAt       *time.Time `gorm:"column:last_read_at"`
	}
	var rows []row
	err := s.db.WithContext(ctx).Table("user_book_reading_states AS state").
		Select("state.asset_id, imports.title, imports.author, assets.original_filename AS file_name, assets.format, assets.processing_status, state.reading_percent, state.last_read_at").
		Joins("JOIN user_book_assets AS assets ON assets.id = state.asset_id AND assets.user_id = state.user_id").
		Joins("JOIN user_book_imports AS imports ON imports.id = assets.import_id AND imports.user_id = state.user_id").
		Where("state.user_id = ? AND state.reading_percent > 0 AND state.reading_percent < 1 AND assets.processing_status = ? AND imports.status = ?", user.ID, model.BookAssetStatusPrivateAvailable, model.BookImportStatusMetadataReady).
		Order("state.last_read_at DESC NULLS LAST").Limit(limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	items := make([]BookContinueReadingDTO, 0, len(rows))
	for _, item := range rows {
		items = append(items, BookContinueReadingDTO{
			AssetID: item.AssetID.String(), Title: item.Title, Author: item.Author, FileName: item.FileName,
			Format: item.Format, ProcessingStatus: item.ProcessingStatus, ReadingPercent: item.ReadingPercent, LastReadAt: item.LastReadAt,
		})
	}
	return items, nil
}

func validateBookShelfInput(input SaveBookShelfInput) error {
	if err := validateBookShelfStatus(input.Status); err != nil {
		return err
	}
	if len(input.Note) > bookShelfNoteMaxLength {
		return apperr.BadRequest("validation.invalid_request", "shelf note is too long")
	}
	return nil
}

func validateBookShelfStatus(status string) error {
	if status == "" {
		return apperr.BadRequest("validation.invalid_request", "shelf status is required")
	}
	switch status {
	case model.BookShelfStatusWantToRead, model.BookShelfStatusReading, model.BookShelfStatusRead, model.BookShelfStatusOnHold, model.BookShelfStatusDropped:
		return nil
	default:
		return apperr.BadRequest("validation.invalid_request", "shelf status is invalid")
	}
}

func buildBookShelfItemDTO(shelf model.UserBookShelf, work BookPublicWorkDTO) BookShelfItemDTO {
	return BookShelfItemDTO{ID: shelf.ID.String(), WorkID: shelf.WorkID.String(), Status: shelf.Status, Note: shelf.Note, UpdatedAt: shelf.UpdatedAt, Work: work}
}

func isPublicBookNotFound(err error) bool {
	return apperr.FromError(err).Code == "books.catalog_not_found"
}
