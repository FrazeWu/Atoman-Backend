package books

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const bookCoverMaxBytes = 2 << 20
const bookCoverMaxDimension = 4096

type UploadBookEditionCoverInput struct {
	ImageData string `json:"image_data"`
}

func decodeEditionCover(data string) ([]byte, string, error) {
	data = strings.TrimSpace(data)
	parts := strings.SplitN(data, ",", 2)
	if len(parts) != 2 {
		return nil, "", apperr.BadRequest("books.invalid_cover", "Cover must be a JPEG or PNG data URL")
	}
	mime := ""
	switch parts[0] {
	case "data:image/jpeg;base64":
		mime = "image/jpeg"
	case "data:image/png;base64":
		mime = "image/png"
	default:
		return nil, "", apperr.BadRequest("books.invalid_cover", "Cover must be JPEG or PNG")
	}
	if len(parts[1]) > (bookCoverMaxBytes*4/3)+8 {
		return nil, "", apperr.BadRequest("books.invalid_cover", "Cover image is too large")
	}
	dataBytes, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || len(dataBytes) == 0 || len(dataBytes) > bookCoverMaxBytes {
		return nil, "", apperr.BadRequest("books.invalid_cover", "Invalid or oversized cover image")
	}
	if http.DetectContentType(dataBytes) != mime {
		return nil, "", apperr.BadRequest("books.invalid_cover", "Image type does not match image data")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(dataBytes))
	if err != nil || (format != "jpeg" && format != "png") || config.Width < 1 || config.Height < 1 || config.Width > bookCoverMaxDimension || config.Height > bookCoverMaxDimension {
		return nil, "", apperr.BadRequest("books.invalid_cover", "Invalid cover dimensions")
	}
	return dataBytes, mime, nil
}

func (s *Service) SaveBookEditionCover(ctx context.Context, user authctx.CurrentUser, editionID uuid.UUID, body UploadBookEditionCoverInput) (string, error) {
	if user.ID == uuid.Nil {
		return "", apperr.Unauthorized("Login required")
	}
	if editionID == uuid.Nil {
		return "", apperr.BadRequest("books.invalid_edition", "Edition ID is required")
	}
	imageBytes, contentType, err := decodeEditionCover(body.ImageData)
	if err != nil {
		return "", err
	}
	if err := requireBookUploadStore(s.bookResources); err != nil {
		return "", err
	}
	ext := "jpg"
	if contentType == "image/png" {
		ext = "png"
	}
	key := "book/edition-covers/" + editionID.String() + "/" + uuid.NewString() + "." + ext
	if err := s.bookResources.PutObject(key, contentType, bytes.NewReader(imageBytes), int64(len(imageBytes))); err != nil {
		return "", apperr.Wrap(http.StatusServiceUnavailable, "storage.unavailable", "Book cover storage is unavailable", err)
	}
	coverURL := "/api/v1/books/editions/" + editionID.String() + "/cover"
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var edition model.BookEdition
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND lifecycle_status = ? AND edit_status <> ?", editionID, model.BookLifecycleStatusActive, model.BookEditStatusClosed).First(&edition).Error; err != nil {
			return publicBookNotFound(err)
		}
		current := model.BookEditionCover{EditionID: editionID, ObjectKey: key, ContentType: contentType, UpdatedBy: user.ID, UpdatedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "edition_id"}}, DoUpdates: clause.AssignmentColumns([]string{"object_key", "content_type", "updated_by", "updated_at"})}).Create(&current).Error; err != nil {
			return err
		}
		edition.CoverURL = coverURL
		edition.CoverSource = "user_selected"
		if err := tx.Model(&edition).Updates(map[string]any{"cover_url": coverURL, "cover_source": "user_selected"}).Error; err != nil {
			return err
		}
		snapshot, err := loadBookEditionSnapshot(tx, edition)
		if err != nil {
			return err
		}
		_, err = appendBookWikiRevision(tx, "edition", edition.ID, user.ID, "更新出版封面", "edit", snapshot)
		return err
	})
	if err != nil {
		_ = s.bookResources.DeleteObject(key)
		return "", err
	}
	return coverURL, nil
}

func (s *Service) OpenBookEditionCover(ctx context.Context, editionID uuid.UUID) (io.ReadCloser, string, error) {
	var cover model.BookEditionCover
	err := s.db.WithContext(ctx).Where("edition_id = ?", editionID).First(&cover).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", apperr.NotFound("books.cover_not_found", "No cover found")
	}
	if err != nil {
		return nil, "", err
	}
	if err := requireBookUploadStore(s.bookResources); err != nil {
		return nil, "", err
	}
	stream, err := s.bookResources.OpenObject(cover.ObjectKey)
	if err != nil {
		return nil, "", apperr.Wrap(http.StatusServiceUnavailable, "storage.unavailable", "Cover storage is unavailable", err)
	}
	return stream, cover.ContentType, nil
}

func (h *Handler) setEditionCover(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "editionId")
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 3<<20)
	var input UploadBookEditionCoverInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("books.invalid_cover", "Cover image must be a JPEG or PNG data URL"))
		return
	}
	url, err := h.service.SaveBookEditionCover(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"cover_url": url})
}

func (h *Handler) getEditionCover(c *gin.Context) {
	id, ok := parseCatalogID(c, "editionId")
	if !ok {
		return
	}
	body, mime, err := h.service.OpenBookEditionCover(c.Request.Context(), id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer body.Close()
	c.Header("Content-Type", mime)
	c.Header("Cache-Control", "public, max-age=60")
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, body)
}
