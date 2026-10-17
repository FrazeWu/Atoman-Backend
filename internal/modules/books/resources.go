package books

import (
	"context"
	"errors"
	"io"
	"net/http"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BookRevisionContent struct {
	Revision    model.BookAssetRevision
	ContentType string
	Size        int64
	Body        io.ReadCloser
}

func (s *Service) readableBookRevision(ctx context.Context, revisionID uuid.UUID) (model.BookAssetRevision, error) {
	var revision model.BookAssetRevision
	err := s.db.WithContext(ctx).Model(&model.BookAssetRevision{}).
		Joins("JOIN book_assets a ON a.id = book_asset_revisions.asset_id AND a.deleted_at IS NULL").
		Joins("JOIN book_works w ON w.id = a.work_id AND w.deleted_at IS NULL").
		Where("book_asset_revisions.id = ? AND book_asset_revisions.status = ? AND a.status = ? AND w.lifecycle_status = ? AND w.edit_status <> ?",
			revisionID, model.BookRevisionStatusReady, model.BookResourceStatusActive, model.BookLifecycleStatusActive, model.BookEditStatusClosed).
		First(&revision).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return revision, apperr.NotFound("books.revision_not_found", "Readable book revision not found")
	}
	return revision, err
}

func (s *Service) OpenBookRevision(ctx context.Context, revisionID uuid.UUID) (BookRevisionContent, error) {
	revision, err := s.readableBookRevision(ctx, revisionID)
	if err != nil {
		return BookRevisionContent{}, err
	}
	if err := requireBookUploadStore(s.bookResources); err != nil {
		return BookRevisionContent{}, err
	}
	var media model.MediaAsset
	if err := s.db.WithContext(ctx).Where("id = ? AND purpose = ? AND url = ''", revision.MediaAssetID, "books.file").First(&media).Error; err != nil {
		return BookRevisionContent{}, apperr.NotFound("books.revision_not_found", "Book file not found")
	}
	body, err := s.bookResources.OpenObject(media.Key)
	if err != nil {
		return BookRevisionContent{}, apperr.Wrap(http.StatusServiceUnavailable, "storage.unavailable", "Book storage is unavailable", err)
	}
	return BookRevisionContent{Revision: revision, ContentType: media.ContentType, Size: media.Size, Body: body}, nil
}

func (s *Service) ListBookAssetRevisions(ctx context.Context, assetID uuid.UUID) ([]model.BookAssetRevision, error) {
	var asset model.BookAsset
	err := s.db.WithContext(ctx).Joins("JOIN book_works w ON w.id = book_assets.work_id AND w.deleted_at IS NULL").
		Where("book_assets.id = ? AND book_assets.status = ? AND w.lifecycle_status = ? AND w.edit_status <> ?", assetID, model.BookResourceStatusActive, model.BookLifecycleStatusActive, model.BookEditStatusClosed).First(&asset).Error
	if err != nil {
		return nil, publicBookNotFound(err)
	}
	revisions := make([]model.BookAssetRevision, 0)
	err = s.db.WithContext(ctx).Where("asset_id = ? AND status = ?", asset.ID, model.BookRevisionStatusReady).Order("number DESC").Find(&revisions).Error
	return revisions, err
}
