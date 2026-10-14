package books

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

type BookAnnotationDTO struct {
	ID             string    `json:"id"`
	AssetID        string    `json:"asset_id"`
	WorkID         *string   `json:"work_id,omitempty"`
	Format         string    `json:"format"`
	EPUBCFI        string    `json:"epub_cfi,omitempty"`
	PDFPage        int       `json:"pdf_page"`
	PDFRects       string    `json:"pdf_rects,omitempty"`
	TXTOffsetStart int64     `json:"txt_offset_start"`
	TXTOffsetEnd   int64     `json:"txt_offset_end"`
	Text           string    `json:"text"`
	Color          string    `json:"color"`
	Style          string    `json:"style"`
	Note           string    `json:"note"`
	ChapterTitle   string    `json:"chapter_title,omitempty"`
	BookTitle      string    `json:"book_title,omitempty"`
	BookAuthor     string    `json:"book_author,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type BookAnnotationListResult struct {
	Items []BookAnnotationDTO `json:"items"`
	Total int64               `json:"total"`
}

type CreateBookAnnotationInput struct {
	Format         string `json:"format"`
	EPUBCFI        string `json:"epub_cfi"`
	PDFPage        int    `json:"pdf_page"`
	PDFRects       string `json:"pdf_rects"`
	TXTOffsetStart int64  `json:"txt_offset_start"`
	TXTOffsetEnd   int64  `json:"txt_offset_end"`
	Text           string `json:"text"`
	Color          string `json:"color"`
	Style          string `json:"style"`
	Note           string `json:"note"`
	ChapterTitle   string `json:"chapter_title"`
}

type UpdateBookAnnotationInput struct {
	Note  *string `json:"note"`
	Color *string `json:"color"`
	Style *string `json:"style"`
}

type ListGlobalNotesFilter struct {
	Keyword string
	AssetID *uuid.UUID
	HasNote *bool
	Color   string
	Limit   int
	Offset  int
}

func isValidAnnotationColor(c string) bool {
	switch c {
	case model.BookAnnotationColorYellow, model.BookAnnotationColorGreen,
		model.BookAnnotationColorBlue, model.BookAnnotationColorPink:
		return true
	default:
		return false
	}
}

func isValidAnnotationStyle(s string) bool {
	switch s {
	case model.BookAnnotationStyleHighlight, model.BookAnnotationStyleUnderline:
		return true
	default:
		return false
	}
}

func isValidAnnotationFormat(f string) bool {
	switch f {
	case "epub", "pdf", "txt":
		return true
	default:
		return false
	}
}

func (s *Service) CreateAnnotation(user authctx.CurrentUser, assetID uuid.UUID, input CreateBookAnnotationInput) (BookAnnotationDTO, error) {
	asset, bookImport, err := s.loadBookAssetOwner(user, assetID)
	if err != nil {
		return BookAnnotationDTO{}, err
	}

	trimmedText := strings.TrimSpace(input.Text)
	if trimmedText == "" {
		return BookAnnotationDTO{}, apperr.BadRequest("validation.invalid_request", "text is required")
	}
	if len(input.Text) > 50000 {
		return BookAnnotationDTO{}, apperr.BadRequest("validation.invalid_request", "text is too long")
	}

	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format == "" {
		format = asset.Format
	}
	if !isValidAnnotationFormat(format) {
		return BookAnnotationDTO{}, apperr.BadRequest("validation.invalid_request", "format must be epub, pdf, or txt")
	}

	color := strings.ToLower(strings.TrimSpace(input.Color))
	if color == "" {
		color = model.BookAnnotationColorYellow
	}
	if !isValidAnnotationColor(color) {
		return BookAnnotationDTO{}, apperr.BadRequest("validation.invalid_request", "color must be yellow, green, blue, or pink")
	}

	style := strings.ToLower(strings.TrimSpace(input.Style))
	if style == "" {
		style = model.BookAnnotationStyleHighlight
	}
	if !isValidAnnotationStyle(style) {
		return BookAnnotationDTO{}, apperr.BadRequest("validation.invalid_request", "style must be highlight or underline")
	}

	if len(input.Note) > bookReadingNotesMaxLength {
		return BookAnnotationDTO{}, apperr.BadRequest("validation.invalid_request", "note is too long")
	}
	if len(input.EPUBCFI) > bookReadingCFIMaxLength {
		return BookAnnotationDTO{}, apperr.BadRequest("validation.invalid_request", "epub_cfi is too long")
	}

	pdfRects := strings.TrimSpace(input.PDFRects)
	if pdfRects == "" {
		pdfRects = "[]"
	}

	chapterTitle := strings.TrimSpace(input.ChapterTitle)
	if len(chapterTitle) > 255 {
		chapterTitle = chapterTitle[:255]
	}

	annotation := model.UserBookAnnotation{
		UserID:         user.ID,
		AssetID:        asset.ID,
		WorkID:         bookImport.WorkID,
		Format:         format,
		EPUBCFI:        strings.TrimSpace(input.EPUBCFI),
		PDFPage:        input.PDFPage,
		PDFRects:       pdfRects,
		TXTOffsetStart: input.TXTOffsetStart,
		TXTOffsetEnd:   input.TXTOffsetEnd,
		Text:           input.Text,
		Color:          color,
		Style:          style,
		Note:           input.Note,
		ChapterTitle:   chapterTitle,
	}

	if err := s.db.Create(&annotation).Error; err != nil {
		return BookAnnotationDTO{}, err
	}

	return buildBookAnnotationDTO(annotation, bookImport.Title, asset.OriginalFilename, bookImport.Author), nil
}

func (s *Service) ListAssetAnnotations(user authctx.CurrentUser, assetID uuid.UUID) ([]BookAnnotationDTO, error) {
	asset, bookImport, err := s.loadBookAssetOwner(user, assetID)
	if err != nil {
		return nil, err
	}

	var records []model.UserBookAnnotation
	if err := s.db.Where("user_id = ? AND asset_id = ?", user.ID, asset.ID).
		Order("created_at ASC, id ASC").
		Find(&records).Error; err != nil {
		return nil, err
	}

	items := make([]BookAnnotationDTO, len(records))
	for i, r := range records {
		items[i] = buildBookAnnotationDTO(r, bookImport.Title, asset.OriginalFilename, bookImport.Author)
	}
	return items, nil
}

func (s *Service) ListGlobalNotes(user authctx.CurrentUser, filter ListGlobalNotesFilter) (BookAnnotationListResult, error) {
	if user.ID == uuid.Nil {
		return BookAnnotationListResult{}, apperr.Unauthorized("Login required")
	}

	query := s.db.Model(&model.UserBookAnnotation{}).Where("user_id = ?", user.ID)

	if filter.AssetID != nil && *filter.AssetID != uuid.Nil {
		query = query.Where("asset_id = ?", *filter.AssetID)
	}
	if filter.HasNote != nil && *filter.HasNote {
		query = query.Where("BTRIM(note) <> '' AND note IS NOT NULL")
	}
	if filter.Color != "" {
		color := strings.ToLower(strings.TrimSpace(filter.Color))
		if isValidAnnotationColor(color) {
			query = query.Where("color = ?", color)
		}
	}
	if strings.TrimSpace(filter.Keyword) != "" {
		kw := "%" + strings.ToLower(strings.TrimSpace(filter.Keyword)) + "%"
		query = query.Where("LOWER(text) LIKE ? OR LOWER(note) LIKE ?", kw, kw)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return BookAnnotationListResult{}, err
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	var records []model.UserBookAnnotation
	if err := query.Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&records).Error; err != nil {
		return BookAnnotationListResult{}, err
	}

	// Batch lookup assets and imports for book_title and book_author
	type assetMeta struct {
		title  string
		author string
	}
	metaMap := make(map[uuid.UUID]assetMeta)

	if len(records) > 0 {
		assetIDs := make([]uuid.UUID, 0, len(records))
		seen := make(map[uuid.UUID]struct{}, len(records))
		for _, r := range records {
			if _, ok := seen[r.AssetID]; !ok {
				seen[r.AssetID] = struct{}{}
				assetIDs = append(assetIDs, r.AssetID)
			}
		}

		var assets []model.UserBookAsset
		if err := s.db.Where("id IN ? AND user_id = ?", assetIDs, user.ID).Find(&assets).Error; err == nil {
			importIDs := make([]uuid.UUID, 0, len(assets))
			assetToImport := make(map[uuid.UUID]uuid.UUID, len(assets))
			assetOriginal := make(map[uuid.UUID]string, len(assets))
			for _, a := range assets {
				assetToImport[a.ID] = a.ImportID
				assetOriginal[a.ID] = a.OriginalFilename
				importIDs = append(importIDs, a.ImportID)
			}

			var imports []model.UserBookImport
			if len(importIDs) > 0 {
				_ = s.db.Where("id IN ? AND user_id = ?", importIDs, user.ID).Find(&imports).Error
			}

			importMap := make(map[uuid.UUID]model.UserBookImport, len(imports))
			for _, imp := range imports {
				importMap[imp.ID] = imp
			}

			for aID, impID := range assetToImport {
				imp := importMap[impID]
				title := imp.Title
				if strings.TrimSpace(title) == "" {
					title = assetOriginal[aID]
				}
				metaMap[aID] = assetMeta{title: title, author: imp.Author}
			}
		}
	}

	items := make([]BookAnnotationDTO, len(records))
	for i, r := range records {
		meta := metaMap[r.AssetID]
		items[i] = buildBookAnnotationDTO(r, meta.title, "", meta.author)
	}

	return BookAnnotationListResult{
		Items: items,
		Total: total,
	}, nil
}

func (s *Service) UpdateAnnotation(user authctx.CurrentUser, annotationID uuid.UUID, input UpdateBookAnnotationInput) (BookAnnotationDTO, error) {
	if user.ID == uuid.Nil {
		return BookAnnotationDTO{}, apperr.Unauthorized("Login required")
	}

	var annotation model.UserBookAnnotation
	if err := s.db.Where("id = ? AND user_id = ?", annotationID, user.ID).First(&annotation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BookAnnotationDTO{}, apperr.NotFound("books.annotation_not_found", "Annotation not found")
		}
		return BookAnnotationDTO{}, err
	}

	updates := map[string]any{}
	if input.Note != nil {
		if len(*input.Note) > bookReadingNotesMaxLength {
			return BookAnnotationDTO{}, apperr.BadRequest("validation.invalid_request", "note is too long")
		}
		updates["note"] = *input.Note
		annotation.Note = *input.Note
	}
	if input.Color != nil {
		color := strings.ToLower(strings.TrimSpace(*input.Color))
		if !isValidAnnotationColor(color) {
			return BookAnnotationDTO{}, apperr.BadRequest("validation.invalid_request", "color must be yellow, green, blue, or pink")
		}
		updates["color"] = color
		annotation.Color = color
	}
	if input.Style != nil {
		style := strings.ToLower(strings.TrimSpace(*input.Style))
		if !isValidAnnotationStyle(style) {
			return BookAnnotationDTO{}, apperr.BadRequest("validation.invalid_request", "style must be highlight or underline")
		}
		updates["style"] = style
		annotation.Style = style
	}

	if len(updates) > 0 {
		if err := s.db.Model(&annotation).Updates(updates).Error; err != nil {
			return BookAnnotationDTO{}, err
		}
	}

	var asset model.UserBookAsset
	var bookImport model.UserBookImport
	_ = s.db.Where("id = ? AND user_id = ?", annotation.AssetID, user.ID).First(&asset).Error
	if asset.ImportID != uuid.Nil {
		_ = s.db.Where("id = ? AND user_id = ?", asset.ImportID, user.ID).First(&bookImport).Error
	}

	return buildBookAnnotationDTO(annotation, bookImport.Title, asset.OriginalFilename, bookImport.Author), nil
}

func (s *Service) DeleteAnnotation(user authctx.CurrentUser, annotationID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}

	var annotation model.UserBookAnnotation
	if err := s.db.Where("id = ? AND user_id = ?", annotationID, user.ID).First(&annotation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("books.annotation_not_found", "Annotation not found")
		}
		return err
	}

	return s.db.Delete(&annotation).Error
}

func buildBookAnnotationDTO(a model.UserBookAnnotation, importTitle, originalFilename, author string) BookAnnotationDTO {
	title := strings.TrimSpace(importTitle)
	if title == "" {
		title = strings.TrimSpace(originalFilename)
	}
	var workIDStr *string
	if a.WorkID != nil && *a.WorkID != uuid.Nil {
		s := a.WorkID.String()
		workIDStr = &s
	}
	return BookAnnotationDTO{
		ID:             a.ID.String(),
		AssetID:        a.AssetID.String(),
		WorkID:         workIDStr,
		Format:         a.Format,
		EPUBCFI:        a.EPUBCFI,
		PDFPage:        a.PDFPage,
		PDFRects:       a.PDFRects,
		TXTOffsetStart: a.TXTOffsetStart,
		TXTOffsetEnd:   a.TXTOffsetEnd,
		Text:           a.Text,
		Color:          a.Color,
		Style:          a.Style,
		Note:           a.Note,
		ChapterTitle:   a.ChapterTitle,
		BookTitle:      title,
		BookAuthor:     author,
		CreatedAt:      a.CreatedAt,
		UpdatedAt:      a.UpdatedAt,
	}
}
