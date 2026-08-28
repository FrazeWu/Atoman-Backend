package books

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	bookReadingCFIMaxLength       = 4096
	bookReadingNotesMaxLength     = 50000
	bookReadingPreferencesMaxSize = 16 * 1024
	bookReadingMaxPDFPage         = 10_000_000
	bookReadingMaxTXTOffset       = 1_000_000_000
)

type BookPrivateAssetDTO struct {
	ID               string `json:"id"`
	ImportID         string `json:"import_id"`
	Title            string `json:"title"`
	Author           string `json:"author,omitempty"`
	FileName         string `json:"file_name"`
	Format           string `json:"format"`
	ContentType      string `json:"content_type"`
	Size             int64  `json:"size"`
	Status           string `json:"status"`
	ScanStatus       string `json:"scan_status"`
	ProcessingStatus string `json:"processing_status"`
	ErrorMessage     string `json:"error_message,omitempty"`
}

type BookReadingStateDTO struct {
	AssetID        string         `json:"asset_id"`
	EPUBCFI        string         `json:"epub_cfi,omitempty"`
	PDFPage        int            `json:"pdf_page"`
	TXTOffset      int64          `json:"txt_offset"`
	ReadingPercent float64        `json:"reading_percent"`
	LastReadAt     *time.Time     `json:"last_read_at,omitempty"`
	PrivateNotes   string         `json:"private_notes,omitempty"`
	Preferences    map[string]any `json:"preferences"`
}

type SaveBookReadingStateInput struct {
	EPUBCFI        string         `json:"epub_cfi"`
	PDFPage        int            `json:"pdf_page"`
	TXTOffset      int64          `json:"txt_offset"`
	ReadingPercent float64        `json:"reading_percent"`
	PrivateNotes   string         `json:"private_notes"`
	Preferences    map[string]any `json:"preferences"`
}

type bookPrivateObject struct {
	Asset  model.UserBookAsset
	Import model.UserBookImport
	Body   io.ReadCloser
}

func (s *Service) GetBookAsset(user authctx.CurrentUser, assetID uuid.UUID) (BookPrivateAssetDTO, error) {
	asset, bookImport, err := s.loadBookAssetOwner(user, assetID)
	if err != nil {
		return BookPrivateAssetDTO{}, err
	}
	return buildBookPrivateAssetDTO(asset, bookImport), nil
}

func (s *Service) OpenBookAsset(user authctx.CurrentUser, assetID uuid.UUID) (bookPrivateObject, error) {
	asset, bookImport, err := s.loadReadableBookAsset(user, assetID)
	if err != nil {
		return bookPrivateObject{}, err
	}
	body, err := s.bookUpload.OpenObject(asset.ObjectKey)
	if err != nil {
		return bookPrivateObject{}, apperr.Wrap(http.StatusServiceUnavailable, "storage.unavailable", "Storage is unavailable", err)
	}
	return bookPrivateObject{Asset: asset, Import: bookImport, Body: body}, nil
}

func (s *Service) GetBookReadingState(user authctx.CurrentUser, assetID uuid.UUID) (BookReadingStateDTO, error) {
	asset, _, err := s.loadReadableBookAsset(user, assetID)
	if err != nil {
		return BookReadingStateDTO{}, err
	}
	var state model.UserBookReadingState
	err = s.db.Where("user_id = ? AND asset_id = ?", user.ID, asset.ID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return buildBookReadingStateDTO(asset.ID, model.UserBookReadingState{}), nil
	}
	if err != nil {
		return BookReadingStateDTO{}, err
	}
	return buildBookReadingStateDTO(asset.ID, state), nil
}

func (s *Service) SaveBookReadingState(user authctx.CurrentUser, assetID uuid.UUID, input SaveBookReadingStateInput) (BookReadingStateDTO, error) {
	asset, _, err := s.loadReadableBookAsset(user, assetID)
	if err != nil {
		return BookReadingStateDTO{}, err
	}
	if err := validateBookReadingState(input); err != nil {
		return BookReadingStateDTO{}, err
	}
	preferencesInput := input.Preferences
	if preferencesInput == nil {
		preferencesInput = map[string]any{}
	}
	preferences, err := json.Marshal(preferencesInput)
	if err != nil {
		return BookReadingStateDTO{}, apperr.BadRequest("validation.invalid_request", "reading preferences are invalid")
	}
	now := time.Now().UTC()
	var state model.UserBookReadingState
	err = s.db.Where("user_id = ? AND asset_id = ?", user.ID, asset.ID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		state = model.UserBookReadingState{UserID: user.ID, AssetID: asset.ID}
	} else if err != nil {
		return BookReadingStateDTO{}, err
	}
	state.EPUBCFI = strings.TrimSpace(input.EPUBCFI)
	state.PDFPage = input.PDFPage
	state.TXTOffset = input.TXTOffset
	state.ReadingPercent = input.ReadingPercent
	state.LastReadAt = &now
	state.PrivateNotes = input.PrivateNotes
	state.PreferencesJSON = string(preferences)
	if state.ID == uuid.Nil {
		if err := s.db.Create(&state).Error; err != nil {
			return BookReadingStateDTO{}, err
		}
	} else if err := s.db.Model(&state).Select("epub_cfi", "pdf_page", "txt_offset", "reading_percent", "last_read_at", "private_notes", "preferences_json").Updates(&state).Error; err != nil {
		return BookReadingStateDTO{}, err
	}
	return buildBookReadingStateDTO(asset.ID, state), nil
}

func (s *Service) loadBookAssetOwner(user authctx.CurrentUser, assetID uuid.UUID) (model.UserBookAsset, model.UserBookImport, error) {
	if user.ID == uuid.Nil {
		return model.UserBookAsset{}, model.UserBookImport{}, apperr.Unauthorized("Login required")
	}
	var asset model.UserBookAsset
	if err := s.db.Where("id = ? AND user_id = ?", assetID, user.ID).First(&asset).Error; err != nil {
		return model.UserBookAsset{}, model.UserBookImport{}, bookAssetNotFound(err)
	}
	var bookImport model.UserBookImport
	if err := s.db.Where("id = ? AND user_id = ?", asset.ImportID, user.ID).First(&bookImport).Error; err != nil {
		return model.UserBookAsset{}, model.UserBookImport{}, bookAssetNotFound(err)
	}
	if bookImport.Status == model.BookImportStatusDeleted || asset.ProcessingStatus == model.BookAssetStatusRemoved {
		return model.UserBookAsset{}, model.UserBookImport{}, apperr.NotFound("books.asset_not_found", "Book asset not found")
	}
	return asset, bookImport, nil
}

func (s *Service) loadReadableBookAsset(user authctx.CurrentUser, assetID uuid.UUID) (model.UserBookAsset, model.UserBookImport, error) {
	asset, bookImport, err := s.loadBookAssetOwner(user, assetID)
	if err != nil {
		return model.UserBookAsset{}, model.UserBookImport{}, err
	}
	if asset.ProcessingStatus != model.BookAssetStatusPrivateAvailable || bookImport.Status != model.BookImportStatusMetadataReady {
		return model.UserBookAsset{}, model.UserBookImport{}, apperr.Unprocessable("books.asset_not_ready", "Book asset is not ready for reading")
	}
	if err := requireBookUploadStore(s.bookUpload); err != nil {
		return model.UserBookAsset{}, model.UserBookImport{}, err
	}
	return asset, bookImport, nil
}

func validateBookReadingState(input SaveBookReadingStateInput) error {
	if len(input.EPUBCFI) > bookReadingCFIMaxLength || input.PDFPage < 0 || input.PDFPage > bookReadingMaxPDFPage || input.TXTOffset < 0 || input.TXTOffset > bookReadingMaxTXTOffset || input.ReadingPercent < 0 || input.ReadingPercent > 1 {
		return apperr.BadRequest("validation.invalid_request", "reading state is invalid")
	}
	if len(input.PrivateNotes) > bookReadingNotesMaxLength {
		return apperr.BadRequest("validation.invalid_request", "private notes are too long")
	}
	if input.Preferences == nil {
		return nil
	}
	encoded, err := json.Marshal(input.Preferences)
	if err != nil || len(encoded) > bookReadingPreferencesMaxSize {
		return apperr.BadRequest("validation.invalid_request", "reading preferences are invalid")
	}
	return nil
}

func buildBookPrivateAssetDTO(asset model.UserBookAsset, bookImport model.UserBookImport) BookPrivateAssetDTO {
	return BookPrivateAssetDTO{
		ID:               asset.ID.String(),
		ImportID:         bookImport.ID.String(),
		Title:            bookImport.Title,
		Author:           bookImport.Author,
		FileName:         asset.OriginalFilename,
		Format:           asset.Format,
		ContentType:      asset.ContentType,
		Size:             asset.SizeBytes,
		Status:           bookImport.Status,
		ScanStatus:       asset.ScanStatus,
		ProcessingStatus: asset.ProcessingStatus,
		ErrorMessage:     asset.ErrorMessage,
	}
}

func buildBookReadingStateDTO(assetID uuid.UUID, state model.UserBookReadingState) BookReadingStateDTO {
	preferences := map[string]any{}
	if strings.TrimSpace(state.PreferencesJSON) != "" {
		_ = json.Unmarshal([]byte(state.PreferencesJSON), &preferences)
	}
	return BookReadingStateDTO{
		AssetID:        assetID.String(),
		EPUBCFI:        state.EPUBCFI,
		PDFPage:        state.PDFPage,
		TXTOffset:      state.TXTOffset,
		ReadingPercent: state.ReadingPercent,
		LastReadAt:     state.LastReadAt,
		PrivateNotes:   state.PrivateNotes,
		Preferences:    preferences,
	}
}

func bookAssetNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("books.asset_not_found", "Book asset not found")
	}
	return err
}

func contentDispositionForBook(filename string) string {
	filename = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(filename))
	if filename == "" {
		filename = "book"
	}
	return `inline; filename="` + strings.ReplaceAll(strings.ReplaceAll(filename, `\`, "_"), `"`, "_") + `"`
}
