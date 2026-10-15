package books

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// New public ebooks fail closed: unlike the legacy private import, structural
// validation alone is not enough when the antivirus service is unavailable.
func (s *Service) ProcessBookResourceUploads(ctx context.Context) (int, error) {
	if err := requireBookUploadStore(s.bookResources); err != nil {
		return 0, err
	}
	if s.virusScanner == nil {
		return 0, apperr.New(http.StatusServiceUnavailable, "books.scanner_unavailable", "Public book safety scanner is unavailable", nil)
	}
	cutoff := time.Now().UTC().Add(-bookProcessingStaleAfter)
	if err := s.db.WithContext(ctx).Model(&model.BookUploadSession{}).Where("status = 'scanning' AND updated_at < ?", cutoff).Update("status", "uploaded").Error; err != nil {
		return 0, err
	}
	if err := s.db.WithContext(ctx).Model(&model.BookUploadSession{}).Where("status = 'completing' AND updated_at < ?", cutoff).Update("status", "uploading").Error; err != nil {
		return 0, err
	}
	var sessions []model.BookUploadSession
	if err := s.db.WithContext(ctx).Where("status = 'uploaded'").Order("created_at ASC").Limit(bookProcessingBatchSize).Find(&sessions).Error; err != nil {
		return 0, err
	}
	processed := 0
	var failures error
	for _, session := range sessions {
		claim := s.db.WithContext(ctx).Model(&model.BookUploadSession{}).Where("id = ? AND status = 'uploaded'", session.ID).Update("status", "scanning")
		if claim.Error != nil {
			failures = errors.Join(failures, claim.Error)
			continue
		}
		if claim.RowsAffected != 1 {
			continue
		}
		processed++
		scanCtx, cancel := context.WithTimeout(ctx, bookProcessingTimeout)
		hash, err := s.inspectBookResourceUpload(scanCtx, session)
		cancel()
		if err != nil {
			status := "failed"
			if errors.Is(err, ErrBookVirusDetected) {
				status = "quarantined"
			}
			markErr := s.db.WithContext(ctx).Model(&model.BookUploadSession{}).Where("id = ? AND status = 'scanning'", session.ID).Updates(map[string]any{"status": status, "error_message": truncateBookUploadError(err.Error())}).Error
			failures = errors.Join(failures, err, markErr)
			continue
		}
		if err := s.publishBookResourceUpload(ctx, session, hash); err != nil {
			// A concurrent catalog merge or transient database error can be retried
			// safely. No existing revision or current file has been overwritten.
			resetErr := s.db.WithContext(ctx).Model(&model.BookUploadSession{}).Where("id = ? AND status = 'scanning'", session.ID).Update("status", "uploaded").Error
			failures = errors.Join(failures, err, resetErr)
		}
	}
	return processed, failures
}

func (s *Service) inspectBookResourceUpload(ctx context.Context, session model.BookUploadSession) (string, error) {
	body, err := s.bookResources.OpenObject(session.ObjectKey)
	if err != nil {
		return "", err
	}
	defer body.Close()
	file, err := os.CreateTemp("", "atoman-book-v2-*."+session.Format)
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash, err := inspectBookObject(io.TeeReader(io.LimitReader(body, session.SizeBytes+1), file), session.Format, session.SizeBytes)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch session.Format {
	case "epub":
		_, err = validateBookEPUB(file, session.SizeBytes)
	case "cbz":
		_, err = validateBookCBZ(file, session.SizeBytes)
	case "cbr":
		_, err = validateBookCBR(file.Name())
	}
	if err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err := s.virusScanner.Scan(ctx, file); err != nil {
		return "", err
	}
	return hash, ctx.Err()
}

func (s *Service) publishBookResourceUpload(ctx context.Context, session model.BookUploadSession, hash string) error {
	var initialAsset model.BookAsset
	if err := s.db.WithContext(ctx).First(&initialAsset, "id = ?", session.AssetID).Error; err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var work model.BookWork
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND lifecycle_status = ? AND edit_status <> ?", initialAsset.WorkID, model.BookLifecycleStatusActive, model.BookEditStatusClosed).First(&work).Error; err != nil {
			return publicBookNotFound(err)
		}
		var asset model.BookAsset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND work_id = ? AND status = ?", session.AssetID, work.ID, model.BookResourceStatusActive).First(&asset).Error; err != nil {
			return publicBookNotFound(err)
		}
		var locked model.BookUploadSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", session.ID).Error; err != nil {
			return err
		}
		if locked.Status != "scanning" {
			return nil
		}
		media := model.MediaAsset{UserID: &session.UserID, Purpose: "books.file", Key: session.ObjectKey, ContentType: session.ContentType, Size: session.SizeBytes}
		if err := tx.Create(&media).Error; err != nil {
			return err
		}
		var number int
		if err := tx.Model(&model.BookAssetRevision{}).Unscoped().Select("COALESCE(MAX(number), 0) + 1").Where("asset_id = ?", asset.ID).Scan(&number).Error; err != nil {
			return err
		}
		revision := model.BookAssetRevision{AssetID: asset.ID, Number: number, MediaAssetID: media.ID, SHA256: hash, Status: model.BookRevisionStatusReady, OriginalFilename: session.OriginalFilename, CreatedBy: session.UserID}
		if err := tx.Create(&revision).Error; err != nil {
			return err
		}
		if err := tx.Model(&asset).Update("current_revision_id", revision.ID).Error; err != nil {
			return err
		}
		return tx.Model(&locked).Updates(map[string]any{"status": "ready", "revision_id": revision.ID, "error_message": ""}).Error
	})
}
