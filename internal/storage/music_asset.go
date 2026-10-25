package storage

import (
	"errors"
	"net/url"
	"os"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type PromotedMusicAsset struct {
	URL            string
	SourceKey      string
	DestinationKey string
}

func PromoteMusicUploadAsset(db *gorm.DB, client *s3.S3, ownerID uuid.UUID, rawURL, destinationKey string) (PromotedMusicAsset, error) {
	result := PromotedMusicAsset{URL: strings.TrimSpace(rawURL)}
	prefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if prefix == "" || !strings.HasPrefix(result.URL, prefix+"/") {
		return result, nil
	}
	sourceKey, err := url.PathUnescape(strings.TrimPrefix(result.URL, prefix+"/"))
	if err != nil {
		return result, nil
	}
	sourceKey = strings.TrimLeft(sourceKey, "/")
	purpose := ""
	switch {
	case strings.HasPrefix(sourceKey, "music/covers/uploads/"):
		purpose = "music.cover"
	case strings.HasPrefix(sourceKey, "music/audio/uploads/"):
		purpose = "music.audio"
	default:
		return result, nil
	}
	if ownerID == uuid.Nil || db == nil {
		return PromotedMusicAsset{}, apperr.Forbidden("music.upload_asset_forbidden", "Music upload asset is not owned by the current user")
	}
	if !validMusicDestination(purpose, destinationKey) {
		return PromotedMusicAsset{}, apperr.BadRequest("music.upload_asset_target_invalid", "Music upload asset does not match the target type")
	}
	var asset model.MediaAsset
	if err := db.Where("user_id = ? AND purpose = ? AND key = ? AND url = ?", ownerID, purpose, sourceKey, result.URL).First(&asset).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PromotedMusicAsset{}, apperr.Forbidden("music.upload_asset_forbidden", "Music upload asset is not owned by the current user")
		}
		return PromotedMusicAsset{}, err
	}
	if !validMusicUploadContentType(asset) {
		return PromotedMusicAsset{}, apperr.BadRequest("music.upload_asset_invalid", "Music upload asset has an unsupported content type")
	}
	if client == nil || bucket == "" || !strings.EqualFold(strings.TrimSpace(os.Getenv("STORAGE_TYPE")), "s3") {
		return result, nil
	}
	escapedSource := strings.ReplaceAll(url.PathEscape(bucket+"/"+sourceKey), "%2F", "/")
	if _, err := client.CopyObject(&s3.CopyObjectInput{
		Bucket: aws.String(bucket), CopySource: aws.String(escapedSource), Key: aws.String(destinationKey),
	}); err != nil {
		return PromotedMusicAsset{}, err
	}
	return PromotedMusicAsset{
		URL: prefix + "/" + destinationKey, SourceKey: sourceKey, DestinationKey: destinationKey,
	}, nil
}

func validMusicUploadContentType(asset model.MediaAsset) bool {
	if asset.Size <= 0 {
		return false
	}
	if asset.Purpose == "music.cover" {
		return strings.HasPrefix(strings.ToLower(asset.ContentType), "image/")
	}
	if asset.Purpose != "music.audio" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(asset.ContentType), "audio/") || asset.ContentType == "application/ogg"
}

func validMusicDestination(purpose, destinationKey string) bool {
	if !strings.HasPrefix(destinationKey, "music/") || strings.Contains(destinationKey, "/uploads/") {
		return false
	}
	switch purpose {
	case "music.cover":
		return strings.Contains(destinationKey, "/covers/") || strings.Contains(destinationKey, "/images/")
	case "music.audio":
		return strings.Contains(destinationKey, "/audio/") || strings.Contains(destinationKey, "/tracks/")
	default:
		return false
	}
}

func DeleteMusicObjects(client *s3.S3, keys []string) {
	if client == nil {
		return
	}
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	seen := map[string]bool{}
	for _, key := range keys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		_, _ = client.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	}
}
