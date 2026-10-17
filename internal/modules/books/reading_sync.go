package books

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CreateBookReadingSessionInput struct {
	ClientSessionID uuid.UUID `json:"client_session_id"`
	RevisionID      uuid.UUID `json:"revision_id"`
	DeviceID        string    `json:"device_id"`
}

type BookReadingSessionDTO struct {
	Session model.BookReadingSession `json:"session"`
	State   model.BookReadingState   `json:"state"`
}

type SaveBookCloudReadingStateInput struct {
	SessionID       uuid.UUID `json:"session_id"`
	RequestID       uuid.UUID `json:"request_id"`
	Sequence        int64     `json:"sequence"`
	BaseVersion     int64     `json:"base_version"`
	EPUBCFI         string    `json:"epub_cfi"`
	PDFPage         int       `json:"pdf_page"`
	TXTOffset       int64     `json:"txt_offset"`
	CurrentPercent  float64   `json:"current_percent"`
	FurthestPercent *float64  `json:"furthest_percent,omitempty"`
}

type BookReadingWriteResult struct {
	State           model.BookReadingState `json:"state"`
	PositionApplied bool                   `json:"position_applied"`
	Duplicate       bool                   `json:"duplicate"`
	IgnoredReason   string                 `json:"ignored_reason,omitempty"`
}

func lockBookCloudState(tx *gorm.DB, userID, revisionID uuid.UUID) (model.BookReadingState, error) {
	state := model.BookReadingState{UserID: userID, RevisionID: revisionID}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; err != nil {
		return state, err
	}
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND revision_id = ?", userID, revisionID).First(&state).Error
	return state, err
}

func (s *Service) CreateBookReadingSession(ctx context.Context, user authctx.CurrentUser, input CreateBookReadingSessionInput) (BookReadingSessionDTO, error) {
	if user.ID == uuid.Nil {
		return BookReadingSessionDTO{}, apperr.Unauthorized("Login required")
	}
	input.DeviceID = strings.TrimSpace(input.DeviceID)
	if input.ClientSessionID == uuid.Nil || input.RevisionID == uuid.Nil || input.DeviceID == "" || len(input.DeviceID) > 128 {
		return BookReadingSessionDTO{}, apperr.BadRequest("books.invalid_reading_session", "A client session ID, revision and device ID are required")
	}
	var result BookReadingSessionDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		if _, err := service.readableBookRevision(ctx, input.RevisionID); err != nil {
			return err
		}
		state, err := lockBookCloudState(tx, user.ID, input.RevisionID)
		if err != nil {
			return err
		}
		session := model.BookReadingSession{UserID: user.ID, ClientSessionID: input.ClientSessionID, RevisionID: input.RevisionID, DeviceID: input.DeviceID}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "client_session_id"}}, DoNothing: true}).Create(&session).Error; err != nil {
			return err
		}
		var stored model.BookReadingSession
		if err := tx.Where("user_id = ? AND client_session_id = ?", user.ID, input.ClientSessionID).First(&stored).Error; err != nil {
			return err
		}
		if stored.RevisionID != input.RevisionID || stored.DeviceID != input.DeviceID {
			return apperr.Conflict("books.session_id_reused", "Client session ID already belongs to another revision or device")
		}
		result = BookReadingSessionDTO{Session: stored, State: state}
		return nil
	})
	return result, err
}

func (s *Service) GetBookCloudReadingState(ctx context.Context, user authctx.CurrentUser, revisionID uuid.UUID) (model.BookReadingState, error) {
	if user.ID == uuid.Nil {
		return model.BookReadingState{}, apperr.Unauthorized("Login required")
	}
	var state model.BookReadingState
	err := s.db.WithContext(ctx).Where("user_id = ? AND revision_id = ?", user.ID, revisionID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if _, err := s.readableBookRevision(ctx, revisionID); err != nil {
			return state, err
		}
		return model.BookReadingState{UserID: user.ID, RevisionID: revisionID}, nil
	}
	// Existing private history remains available even if its file is taken down.
	return state, err
}

func validateBookCloudPosition(input SaveBookCloudReadingStateInput, format string) error {
	validPercent := func(value float64) bool {
		return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
	}
	if !validPercent(input.CurrentPercent) || (input.FurthestPercent != nil && (!validPercent(*input.FurthestPercent) || *input.FurthestPercent < input.CurrentPercent)) {
		return apperr.BadRequest("books.invalid_reading_percent", "Reading percentages must be within 0 and 1")
	}
	if input.PDFPage < 0 || input.PDFPage > bookReadingMaxPDFPage || input.TXTOffset < 0 || input.TXTOffset > bookReadingMaxTXTOffset || len(input.EPUBCFI) > bookReadingCFIMaxLength {
		return apperr.BadRequest("books.invalid_reading_position", "Reading position is out of range")
	}
	switch format {
	case "epub":
		_, cfiErr := parseBookPointCFI(input.EPUBCFI)
		if input.PDFPage != 0 || input.TXTOffset != 0 || cfiErr != nil {
			return apperr.BadRequest("books.invalid_reading_position", "An EPUB CFI is required for this revision")
		}
	case "pdf", "cbz", "cbr":
		if input.PDFPage < 1 || input.EPUBCFI != "" || input.TXTOffset != 0 {
			return apperr.BadRequest("books.invalid_reading_position", "A page number is required for this revision")
		}
	case "txt":
		if input.PDFPage != 0 || input.EPUBCFI != "" {
			return apperr.BadRequest("books.invalid_reading_position", "Use a text offset for this revision")
		}
	default:
		return apperr.BadRequest("books.invalid_reading_position", "Unknown ebook format")
	}
	return nil
}

func (s *Service) SaveBookCloudReadingState(ctx context.Context, user authctx.CurrentUser, revisionID uuid.UUID, input SaveBookCloudReadingStateInput) (BookReadingWriteResult, error) {
	if user.ID == uuid.Nil {
		return BookReadingWriteResult{}, apperr.Unauthorized("Login required")
	}
	if input.SessionID == uuid.Nil || input.RequestID == uuid.Nil || input.Sequence < 1 || input.BaseVersion < 0 {
		return BookReadingWriteResult{}, apperr.BadRequest("books.invalid_reading_write", "Session, request ID, positive sequence and base version are required")
	}
	input.EPUBCFI = strings.TrimSpace(input.EPUBCFI)
	encoded, err := json.Marshal(input)
	if err != nil {
		return BookReadingWriteResult{}, apperr.BadRequest("books.invalid_reading_write", "Invalid reading state")
	}
	hash := sha256.Sum256(encoded)
	requestHash := hex.EncodeToString(hash[:])
	var result BookReadingWriteResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Authorize before creating or locking any other user's data.
		var session model.BookReadingSession
		if err := tx.Where("id = ? AND user_id = ? AND revision_id = ?", input.SessionID, user.ID, revisionID).First(&session).Error; err != nil {
			return privateBookReadingSessionError(err)
		}
		state, err := lockBookCloudState(tx, user.ID, revisionID)
		if err != nil {
			return err
		}
		// All writers take state -> session locks in the same order.
		var locked model.BookReadingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND revision_id = ?", session.ID, user.ID, revisionID).First(&locked).Error; err != nil {
			return privateBookReadingSessionError(err)
		}
		var receipt model.BookReadingWrite
		err = tx.Where("user_id = ? AND revision_id = ? AND request_id = ?", user.ID, revisionID, input.RequestID).First(&receipt).Error
		if err == nil {
			if receipt.RequestHash != requestHash {
				return apperr.Conflict("books.request_id_reused", "Request ID was already used for a different reading update")
			}
			if err := json.Unmarshal([]byte(receipt.ResultJSON), &result); err != nil {
				return err
			}
			result.State.UserID = user.ID
			result.Duplicate = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var revision model.BookAssetRevision
		if err := tx.Unscoped().First(&revision, "id = ?", revisionID).Error; err != nil {
			return err
		}
		var asset model.BookAsset
		if err := tx.Unscoped().First(&asset, "id = ?", revision.AssetID).Error; err != nil {
			return err
		}
		if err := validateBookCloudPosition(input, asset.Format); err != nil {
			return err
		}
		furthest := math.Max(state.FurthestPercent, input.CurrentPercent)
		if input.FurthestPercent != nil {
			furthest = math.Max(furthest, *input.FurthestPercent)
		}
		if input.Sequence <= locked.LastSequence {
			// Older positions never move the reader. Their furthest observation can
			// still contribute monotonically without replacing the actual position.
			if furthest > state.FurthestPercent {
				state.FurthestPercent = furthest
				state.Version++
				if err := tx.Model(&state).Select("furthest_percent", "version", "updated_at").Updates(&state).Error; err != nil {
					return err
				}
			}
			result = BookReadingWriteResult{State: state, IgnoredReason: "out_of_order"}
		} else {
			if input.BaseVersion != state.Version {
				err := apperr.Conflict("books.reading_state_conflict", "Cloud reading position changed; choose which position to continue from")
				err.Details["state"] = state
				return err
			}
			state.EPUBCFI = input.EPUBCFI
			state.PDFPage = input.PDFPage
			state.TXTOffset = input.TXTOffset
			state.CurrentPercent = input.CurrentPercent
			state.FurthestPercent = furthest
			state.Version++
			if err := tx.Model(&state).Select("epub_cfi", "pdf_page", "txt_offset", "current_percent", "furthest_percent", "version", "updated_at").Updates(&state).Error; err != nil {
				return err
			}
			if err := tx.Model(&locked).Update("last_sequence", input.Sequence).Error; err != nil {
				return err
			}
			result = BookReadingWriteResult{State: state, PositionApplied: true}
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Create(&model.BookReadingWrite{UserID: user.ID, RevisionID: revisionID, RequestID: input.RequestID, SessionID: session.ID, RequestHash: requestHash, ResultJSON: string(encoded)}).Error
	})
	return result, err
}

func privateBookReadingSessionError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("books.reading_session_not_found", "Reading session not found")
	}
	return err
}
