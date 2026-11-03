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

	"github.com/google/uuid"
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
	if err := s.db.WithContext(ctx).Model(&model.BookUploadSession{}).Where("status = 'scanning' AND updated_at < ?", cutoff).
		Updates(map[string]any{"status": "uploaded", "scan_lease_token": ""}).Error; err != nil {
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
		token := uuid.NewString()
		claim := s.db.WithContext(ctx).Model(&model.BookUploadSession{}).Where("id = ? AND status = 'uploaded'", session.ID).
			Updates(map[string]any{"status": "scanning", "scan_lease_token": token})
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
			markErr := s.db.WithContext(ctx).Model(&model.BookUploadSession{}).
				Where("id = ? AND status = 'scanning' AND scan_lease_token = ?", session.ID, token).
				Updates(map[string]any{"status": status, "scan_lease_token": "", "error_message": truncateBookUploadError(err.Error())}).Error
			failures = errors.Join(failures, err, markErr)
			continue
		}
		if err := s.publishBookResourceUpload(ctx, session, hash, token); err != nil {
			// A concurrent catalog merge or transient database error can be retried
			// safely. No existing revision or current file has been overwritten.
			resetErr := s.db.WithContext(ctx).Model(&model.BookUploadSession{}).
				Where("id = ? AND status = 'scanning' AND scan_lease_token = ?", session.ID, token).
				Updates(map[string]any{"status": "uploaded", "scan_lease_token": ""}).Error
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

func (s *Service) publishBookResourceUpload(ctx context.Context, session model.BookUploadSession, hash, token string) error {
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
		if locked.Status != "scanning" || locked.ScanLeaseToken != token {
			return nil // Reclaimed scan: the old worker cannot publish a revision.
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
		// Uploading a file is not permission to publish its content.
		if err := tx.Create(&model.BookFileVisibility{RevisionID: revision.ID, Public: session.PublicDistributionConfirmed, UpdatedBy: session.UserID, UpdatedAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
		if err := tx.Model(&asset).Update("current_revision_id", revision.ID).Error; err != nil {
			return err
		}
		// The first verified file is the default. Later uploads never
		// silently change a reader's or an edition's chosen file.
		if asset.EditionID != nil {
			best := model.BookEditionBestFile{EditionID: *asset.EditionID, AssetID: asset.ID, RevisionID: revision.ID, UpdatedBy: session.UserID, UpdatedAt: time.Now().UTC()}
			inserted := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "edition_id"}}, DoNothing: true}).Create(&best)
			if inserted.Error != nil {
				return inserted.Error
			}
			if inserted.RowsAffected > 0 {
				if err := tx.Create(&model.BookBestFileEvent{EditionID: *asset.EditionID, ToRevisionID: revision.ID, ChangedBy: session.UserID}).Error; err != nil {
					return err
				}
			}
		}
		// A book edition is only published once it has a safe, ready file.
		if asset.EditionID != nil {
			var edition model.BookEdition
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", *asset.EditionID).First(&edition).Error; err != nil {
				return err
			}
			if edition.LifecycleStatus == model.BookLifecycleStatusDraft {
				if edition.CreatedBy == nil || *edition.CreatedBy != session.UserID {
					return apperr.Forbidden("books.edition_upload_owner_required", "Only the draft's creator can finish the initial upload")
				}
				edition.LifecycleStatus = model.BookLifecycleStatusActive
				if err := tx.Model(&edition).Update("lifecycle_status", model.BookLifecycleStatusActive).Error; err != nil {
					return err
				}
				snapshot, err := loadBookEditionSnapshot(tx, edition)
				if err != nil {
					return err
				}
				if _, err := appendBookWikiRevision(tx, "edition", edition.ID, session.UserID, "电子书安全检查通过，出版版本正式创建", "publish", snapshot); err != nil {
					return err
				}
			}
		}
		return tx.Model(&locked).Updates(map[string]any{"status": "ready", "scan_lease_token": "", "revision_id": revision.ID, "error_message": ""}).Error
	})
}
