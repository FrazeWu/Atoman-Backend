package books

import (
	"archive/zip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/storage"

	"github.com/google/uuid"
	"github.com/nwaples/rardecode"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	bookProcessingBatchSize            = 16
	bookEPUBMaxEntries                 = 10000
	bookEPUBMaxExpandedBytes     int64 = 512 * 1024 * 1024
	bookEPUBMaxCompressionRatio        = 100
	bookComicMaxEntries                = 20000
	bookComicMaxExpandedBytes    int64 = 512 * 1024 * 1024
	bookComicMaxCompressionRatio       = 100
)

var bookWindowsAbsolutePath = regexp.MustCompile(`^[A-Za-z]:`)

// ProcessBookAssets claims pending private assets and makes only structurally
// verified objects available to their owner. It is safe to call concurrently.
func (s *Service) ProcessBookAssets(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("book processing database is required")
	}
	if err := requireBookUploadStore(s.bookUpload); err != nil {
		return 0, err
	}
	var candidates []model.UserBookAsset
	if err := s.db.WithContext(ctx).
		Where("processing_status = ?", model.BookAssetStatusScanning).
		Order("created_at ASC").
		Limit(bookProcessingBatchSize).
		Find(&candidates).Error; err != nil {
		return 0, err
	}

	processed := 0
	var processingErr error
	for _, candidate := range candidates {
		claimed, err := s.claimBookAsset(ctx, candidate.ID)
		if err != nil {
			processingErr = errors.Join(processingErr, fmt.Errorf("claim book asset %s: %w", candidate.ID, err))
			continue
		}
		if !claimed {
			continue
		}
		processed++
		if err := s.processBookAsset(ctx, candidate.ID); err != nil {
			if markErr := s.markBookAssetFailed(ctx, candidate.ID, err); markErr != nil {
				processingErr = errors.Join(processingErr, fmt.Errorf("mark book asset %s failed: %w", candidate.ID, markErr))
			} else {
				processingErr = errors.Join(processingErr, fmt.Errorf("process book asset %s: %w", candidate.ID, err))
			}
		}
	}
	return processed, processingErr
}

func (s *Service) claimBookAsset(ctx context.Context, assetID uuid.UUID) (bool, error) {
	result := s.db.WithContext(ctx).Model(&model.UserBookAsset{}).
		Where("id = ? AND processing_status = ?", assetID, model.BookAssetStatusScanning).
		Updates(map[string]any{
			"processing_status": model.BookAssetStatusProcessing,
			"scan_status":       "processing",
		})
	return result.RowsAffected == 1, result.Error
}

func (s *Service) processBookAsset(ctx context.Context, assetID uuid.UUID) error {
	var asset model.UserBookAsset
	if err := s.db.WithContext(ctx).Where("id = ? AND processing_status = ?", assetID, model.BookAssetStatusProcessing).First(&asset).Error; err != nil {
		return err
	}
	object, err := s.bookUpload.OpenObject(asset.ObjectKey)
	if err != nil {
		return err
	}
	defer object.Close()

	metadata := map[string]any{
		"format":         asset.Format,
		"verified_bytes": asset.SizeBytes,
		"verified_at":    time.Now().UTC().Format(time.RFC3339Nano),
	}
	var sha string
	if asset.Format == "epub" || asset.Format == "cbz" || asset.Format == "cbr" {
		temporary, err := os.CreateTemp("", "atoman-book-*"+path.Ext("."+asset.Format))
		if err != nil {
			return err
		}
		temporaryName := temporary.Name()
		defer os.Remove(temporaryName)
		sha, err = inspectBookObject(io.TeeReader(object, temporary), asset.Format, asset.SizeBytes)
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		file, err := os.Open(temporaryName)
		if err != nil {
			return err
		}
		var structuralMetadata map[string]any
		var validationErr error
		switch asset.Format {
		case "epub":
			structuralMetadata, validationErr = validateBookEPUB(file, asset.SizeBytes)
		case "cbz":
			structuralMetadata, validationErr = validateBookCBZ(file, asset.SizeBytes)
		case "cbr":
			structuralMetadata, validationErr = validateBookCBR(temporaryName)
		}
		closeErr := file.Close()
		if validationErr == nil {
			validationErr = closeErr
		}
		if validationErr != nil {
			return validationErr
		}
		for key, value := range structuralMetadata {
			metadata[key] = value
		}
	} else {
		sha, err = inspectBookObject(object, asset.Format, asset.SizeBytes)
		if err != nil {
			return err
		}
	}
	scanStatus := "structurally_clean"
	if s.virusScanner != nil {
		scanObject, scanErr := s.bookUpload.OpenObject(asset.ObjectKey)
		if scanErr != nil {
			return scanErr
		}
		scanErr = s.virusScanner.Scan(ctx, scanObject)
		closeErr := scanObject.Close()
		if scanErr == nil {
			scanErr = closeErr
		}
		if scanErr != nil {
			return scanErr
		}
		scanStatus = "clean"
	}
	metadata["scan_status"] = scanStatus
	metadata["sha256"] = sha
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	if err := s.publishScannedBookAsset(ctx, asset); err != nil {
		return err
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked model.UserBookAsset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", assetID).First(&locked).Error; err != nil {
			return err
		}
		if locked.ProcessingStatus != model.BookAssetStatusProcessing {
			return nil
		}
		if err := tx.Model(&model.UserBookAsset{}).Where("id = ? AND processing_status = ?", assetID, model.BookAssetStatusProcessing).Updates(map[string]any{
			"sha256":            sha,
			"scan_status":       scanStatus,
			"processing_status": model.BookAssetStatusPrivateAvailable,
			"error_message":     "",
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.UserBookImport{}).Where("id = ? AND status = ?", locked.ImportID, model.BookImportStatusScanning).Updates(map[string]any{
			"status":        model.BookImportStatusMetadataReady,
			"metadata_json": string(encodedMetadata),
			"error_code":    "",
			"error_message": "",
		}).Error
	})
}

// publishScannedBookAsset makes a structurally and virus-scanned upload public.
// The source remains in the owner's private area so private reading progress
// and notes keep working independently from the public copy.
func (s *Service) publishScannedBookAsset(ctx context.Context, source model.UserBookAsset) error {
	var existing model.PublishedBookAsset
	if err := s.db.WithContext(ctx).Where("source_asset_id = ?", source.ID).First(&existing).Error; err == nil {
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	publishedID := uuid.New()
	publicKey := storage.BuildBookPublishedObjectKey(publishedID.String(), source.Format)
	if err := s.bookUpload.CopyObject(source.ObjectKey, publicKey, source.ContentType); err != nil {
		return err
	}

	published := model.PublishedBookAsset{
		Base:          model.Base{ID: publishedID},
		SourceAssetID: source.ID,
		Format:        source.Format,
		ObjectKey:     publicKey,
		SHA256:        source.SHA256,
		Status:        model.BookPublicationStatusPublished,
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.PublishedBookAsset
		if err := tx.Where("source_asset_id = ?", source.ID).First(&current).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&published).Error
	}); err != nil {
		_ = s.bookUpload.DeleteObject(publicKey)
		return err
	}
	return nil
}

func (s *Service) markBookAssetFailed(ctx context.Context, assetID uuid.UUID, cause error) error {
	message := truncateBookUploadError(cause.Error())
	assetStatus := model.BookAssetStatusFailed
	scanStatus := "failed"
	errorCode := "books.processing_failed"
	if errors.Is(cause, ErrBookVirusDetected) {
		assetStatus = model.BookAssetStatusQuarantined
		scanStatus = "infected"
		errorCode = "books.virus_detected"
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var asset model.UserBookAsset
		if err := tx.Where("id = ?", assetID).First(&asset).Error; err != nil {
			return err
		}
		if asset.ProcessingStatus != model.BookAssetStatusProcessing {
			return nil
		}
		if err := tx.Model(&model.UserBookAsset{}).Where("id = ? AND processing_status = ?", assetID, model.BookAssetStatusProcessing).Updates(map[string]any{
			"scan_status":       scanStatus,
			"processing_status": assetStatus,
			"error_message":     message,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.UserBookImport{}).Where("id = ? AND status = ?", asset.ImportID, model.BookImportStatusScanning).Updates(map[string]any{
			"status":        model.BookImportStatusFailed,
			"error_code":    errorCode,
			"error_message": message,
		}).Error
	})
}

type bookEPUBContainer struct {
	Rootfiles struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfile"`
	} `xml:"rootfiles"`
}

func validateBookEPUB(reader io.ReaderAt, size int64) (map[string]any, error) {
	archive, err := zip.NewReader(reader, size)
	if err != nil {
		return nil, fmt.Errorf("invalid EPUB archive: %w", err)
	}
	if len(archive.File) == 0 || len(archive.File) > bookEPUBMaxEntries {
		return nil, errors.New("EPUB entry count is invalid")
	}
	seen := make(map[string]struct{}, len(archive.File))
	var totalExpanded int64
	var mimetype *zip.File
	var container *zip.File
	for _, entry := range archive.File {
		clean, err := cleanBookArchivePath(entry.Name)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[clean]; exists {
			return nil, fmt.Errorf("duplicate EPUB entry %q", entry.Name)
		}
		seen[clean] = struct{}{}
		if entry.Flags&0x1 != 0 || entry.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unsafe EPUB entry %q", entry.Name)
		}
		if entry.UncompressedSize64 > uint64(bookEPUBMaxExpandedBytes) {
			return nil, errors.New("EPUB entry is too large")
		}
		if entry.CompressedSize64 == 0 && entry.UncompressedSize64 > 0 {
			return nil, fmt.Errorf("invalid EPUB compression size for %q", entry.Name)
		}
		if entry.CompressedSize64 > 0 && entry.UncompressedSize64/entry.CompressedSize64 > bookEPUBMaxCompressionRatio {
			return nil, fmt.Errorf("EPUB compression ratio is too high for %q", entry.Name)
		}
		if entry.UncompressedSize64 > uint64(bookEPUBMaxExpandedBytes)-uint64(totalExpanded) {
			return nil, errors.New("EPUB expands beyond size limit")
		}
		totalExpanded += int64(entry.UncompressedSize64)
		switch clean {
		case "mimetype":
			mimetype = entry
		case "META-INF/container.xml":
			container = entry
		}
	}
	if mimetype == nil || mimetype.UncompressedSize64 != uint64(len("application/epub+zip")) || mimetype.CompressedSize64 != mimetype.UncompressedSize64 {
		return nil, errors.New("EPUB mimetype entry is invalid")
	}
	mimetypeReader, err := mimetype.Open()
	if err != nil {
		return nil, err
	}
	mimetypeBytes, readErr := io.ReadAll(io.LimitReader(mimetypeReader, 64))
	closeErr := mimetypeReader.Close()
	if readErr == nil {
		readErr = closeErr
	}
	if readErr != nil || string(mimetypeBytes) != "application/epub+zip" {
		return nil, errors.New("EPUB mimetype entry is invalid")
	}
	if container == nil {
		return nil, errors.New("EPUB container.xml is missing")
	}
	containerReader, err := container.Open()
	if err != nil {
		return nil, err
	}
	var document bookEPUBContainer
	decodeErr := xml.NewDecoder(io.LimitReader(containerReader, 128*1024)).Decode(&document)
	closeErr = containerReader.Close()
	if decodeErr == nil {
		decodeErr = closeErr
	}
	if decodeErr != nil || len(document.Rootfiles.Rootfiles) == 0 {
		return nil, errors.New("EPUB container.xml is invalid")
	}
	rootfile, err := cleanBookArchivePath(document.Rootfiles.Rootfiles[0].FullPath)
	if err != nil {
		return nil, errors.New("EPUB rootfile path is invalid")
	}
	if _, exists := seen[rootfile]; !exists {
		return nil, errors.New("EPUB rootfile is missing")
	}
	return map[string]any{
		"entry_count":     len(archive.File),
		"expanded_bytes":  totalExpanded,
		"rootfile":        rootfile,
		"structural_scan": "structurally_clean",
	}, nil
}

func validateBookCBZ(reader io.ReaderAt, size int64) (map[string]any, error) {
	archive, err := zip.NewReader(reader, size)
	if err != nil {
		return nil, fmt.Errorf("invalid CBZ archive: %w", err)
	}
	if len(archive.File) == 0 || len(archive.File) > bookComicMaxEntries {
		return nil, errors.New("CBZ entry count is invalid")
	}
	seen := make(map[string]struct{}, len(archive.File))
	var totalExpanded int64
	imageCount := 0
	for _, entry := range archive.File {
		clean, err := cleanBookArchivePath(entry.Name)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[clean]; exists {
			return nil, fmt.Errorf("duplicate CBZ entry %q", entry.Name)
		}
		seen[clean] = struct{}{}
		if entry.Flags&0x1 != 0 || entry.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unsafe CBZ entry %q", entry.Name)
		}
		if entry.UncompressedSize64 > uint64(bookComicMaxExpandedBytes) {
			return nil, errors.New("CBZ entry is too large")
		}
		if entry.CompressedSize64 == 0 && entry.UncompressedSize64 > 0 {
			return nil, fmt.Errorf("invalid CBZ compression size for %q", entry.Name)
		}
		if entry.CompressedSize64 > 0 && entry.UncompressedSize64/entry.CompressedSize64 > bookComicMaxCompressionRatio {
			return nil, fmt.Errorf("CBZ compression ratio is too high for %q", entry.Name)
		}
		if entry.UncompressedSize64 > uint64(bookComicMaxExpandedBytes)-uint64(totalExpanded) {
			return nil, errors.New("CBZ expands beyond size limit")
		}
		totalExpanded += int64(entry.UncompressedSize64)
		if !entry.FileInfo().IsDir() && isBookComicImage(clean) {
			imageCount++
		}
	}
	if imageCount == 0 {
		return nil, errors.New("CBZ contains no supported comic images")
	}
	return map[string]any{
		"entry_count":     len(archive.File),
		"image_count":     imageCount,
		"expanded_bytes":  totalExpanded,
		"structural_scan": "structurally_clean",
	}, nil
}

func validateBookCBR(filename string) (map[string]any, error) {
	reader, err := rardecode.OpenReader(filename, "")
	if err != nil {
		return nil, fmt.Errorf("invalid CBR archive: %w", err)
	}
	defer reader.Close()
	seen := make(map[string]struct{})
	entryCount := 0
	imageCount := 0
	var totalExpanded int64
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, fmt.Errorf("invalid CBR archive: %w", nextErr)
		}
		entryCount++
		if entryCount > bookComicMaxEntries {
			return nil, errors.New("CBR entry count is invalid")
		}
		if header.UnPackedSize < 0 || header.PackedSize < 0 {
			return nil, errors.New("invalid CBR entry size")
		}
		if header.PackedSize > 0 && header.UnPackedSize > header.PackedSize*bookComicMaxCompressionRatio {
			return nil, fmt.Errorf("CBR compression ratio is too high for %q", header.Name)
		}
		clean, cleanErr := cleanBookArchivePath(header.Name)
		if cleanErr != nil {
			return nil, cleanErr
		}
		if header.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unsafe CBR entry %q", header.Name)
		}
		if header.IsDir {
			continue
		}
		if _, exists := seen[clean]; exists {
			return nil, fmt.Errorf("duplicate CBR entry %q", header.Name)
		}
		seen[clean] = struct{}{}
		if header.UnPackedSize > bookComicMaxExpandedBytes-totalExpanded {
			return nil, errors.New("CBR expands beyond size limit")
		}
		totalExpanded += header.UnPackedSize
		if isBookComicImage(clean) {
			imageCount++
		}
		if _, copyErr := io.Copy(io.Discard, reader); copyErr != nil {
			return nil, fmt.Errorf("read CBR entry %q: %w", header.Name, copyErr)
		}
	}
	if imageCount == 0 {
		return nil, errors.New("CBR contains no supported comic images")
	}
	return map[string]any{
		"entry_count":     entryCount,
		"image_count":     imageCount,
		"expanded_bytes":  totalExpanded,
		"structural_scan": "structurally_clean",
	}, nil
}

func isBookComicImage(filename string) bool {
	switch strings.ToLower(path.Ext(filename)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func cleanBookArchivePath(raw string) (string, error) {
	normalized := strings.ReplaceAll(raw, `\`, "/")
	if normalized == "" || strings.HasPrefix(normalized, "/") || bookWindowsAbsolutePath.MatchString(normalized) {
		return "", fmt.Errorf("unsafe EPUB path %q", raw)
	}
	clean := path.Clean(normalized)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("unsafe EPUB path %q", raw)
	}
	return clean, nil
}
