package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"atoman/internal/model"
	"atoman/internal/modules/dm"
	"atoman/internal/storage"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type LegacyDMImageMigrationConfig struct {
	UploadsRoot  string
	S3URLPrefix  string
	PublicBucket string
}

type legacyDMImage struct {
	message model.DMMessage
	local   string
	s3Key   string
}

func collectLegacyDMImages(db *gorm.DB, config LegacyDMImageMigrationConfig) ([]legacyDMImage, error) {
	var messages []model.DMMessage
	if err := db.Where("image_url <> '' AND image_id IS NULL").Find(&messages).Error; err != nil {
		return nil, err
	}
	prefix := strings.TrimRight(strings.TrimSpace(config.S3URLPrefix), "/")
	items := make([]legacyDMImage, 0, len(messages))
	for _, message := range messages {
		rawURL := strings.TrimSpace(message.ImageURL)
		item := legacyDMImage{message: message}
		switch {
		case strings.HasPrefix(rawURL, "/uploads/dm/images/"):
			localPath, err := legacyDMLocalImagePath(config.UploadsRoot, rawURL)
			if err != nil {
				return nil, fmt.Errorf("message %s has invalid local image URL", message.ID)
			}
			item.local = localPath
			if info, err := os.Stat(item.local); err != nil || info.IsDir() {
				return nil, fmt.Errorf("message %s local image unavailable", message.ID)
			}
		case prefix != "" && strings.HasPrefix(rawURL, prefix+"/"):
			key, err := url.PathUnescape(strings.TrimPrefix(rawURL, prefix+"/"))
			if err != nil || key == "" {
				return nil, fmt.Errorf("message %s has invalid legacy object URL", message.ID)
			}
			item.s3Key = strings.TrimLeft(key, "/")
		default:
			return nil, fmt.Errorf("message %s has unsupported legacy image URL", message.ID)
		}
		items = append(items, item)
	}
	return items, nil
}

func legacyDMLocalImagePath(uploadsRoot, rawURL string) (string, error) {
	decoded, err := url.PathUnescape(rawURL)
	if err != nil || !strings.HasPrefix(decoded, "/uploads/dm/images/") {
		return "", fmt.Errorf("invalid local image URL")
	}
	base, err := filepath.Abs(filepath.Join(uploadsRoot, "dm", "images"))
	if err != nil {
		return "", err
	}
	relative := filepath.FromSlash(strings.TrimPrefix(decoded, "/uploads/dm/images/"))
	candidate, err := filepath.Abs(filepath.Join(base, relative))
	if err != nil {
		return "", err
	}
	if candidate == base || !strings.HasPrefix(candidate, base+string(filepath.Separator)) {
		return "", fmt.Errorf("local image URL escapes DM image root")
	}
	return candidate, nil
}

func MigrateLegacyDMImages(ctx context.Context, db *gorm.DB, store dm.ImageStore, config LegacyDMImageMigrationConfig, publicS3 *s3.S3) error {
	items, err := collectLegacyDMImages(db, config)
	if err != nil {
		return err
	}
	for _, item := range items {
		var body io.ReadCloser
		if item.local != "" {
			body, err = os.Open(item.local)
		} else {
			if publicS3 == nil || config.PublicBucket == "" {
				return fmt.Errorf("message %s legacy object storage unavailable", item.message.ID)
			}
			out, getErr := publicS3.GetObject(&s3.GetObjectInput{Bucket: aws.String(config.PublicBucket), Key: aws.String(item.s3Key)})
			if getErr != nil {
				return fmt.Errorf("message %s read legacy object: %w", item.message.ID, getErr)
			}
			body = out.Body
		}
		image := model.DMImage{UploadedByUserID: item.message.ActorUserID, ObjectKey: "images/" + item.message.ActorUserID.String() + "/" + uuid.NewString() + extensionForLegacyImage(item.message.ImageURL), ContentType: "application/octet-stream"}
		data, readErr := io.ReadAll(body)
		_ = body.Close()
		if readErr != nil {
			return fmt.Errorf("message %s copy legacy image: %w", item.message.ID, readErr)
		}
		image.SizeBytes = int64(len(data))
		err = store.Put(ctx, image.ObjectKey, image.ContentType, bytes.NewReader(data), image.SizeBytes)
		if err != nil {
			return fmt.Errorf("message %s copy legacy image: %w", item.message.ID, err)
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&image).Error; err != nil {
				return err
			}
			result := tx.Model(&model.DMMessage{}).Where("id = ? AND image_id IS NULL", item.message.ID).Update("image_id", image.ID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("message %s already migrated", item.message.ID)
			}
			return nil
		}); err != nil {
			if cleanupErr := store.Delete(ctx, image.ObjectKey); cleanupErr != nil {
				return fmt.Errorf("%w; cleanup private image: %w", err, cleanupErr)
			}
			return err
		}
	}
	var remaining int64
	if err := db.Model(&model.DMMessage{}).Where("image_url <> '' AND image_id IS NULL").Count(&remaining).Error; err != nil {
		return err
	}
	if remaining != 0 {
		return fmt.Errorf("%d legacy images remain unbound", remaining)
	}
	return nil
}

func extensionForLegacyImage(rawURL string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(rawURL)))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp":
		return ext
	}
	return ".bin"
}

func main() {
	envFile := flag.String("env", ".env.dev", "env file to load")
	uploadsRoot := flag.String("uploads-root", ".", "root containing uploads")
	flag.Parse()
	if err := godotenv.Load(*envFile); err != nil {
		log.Fatalf("load env: %v", err)
	}
	db, err := gorm.Open(postgres.Open(os.Getenv("DATABASE_URL")), &gorm.Config{})
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	s3Client, err := storage.InitS3Client()
	if err != nil {
		log.Fatalf("init s3: %v", err)
	}
	config := LegacyDMImageMigrationConfig{UploadsRoot: *uploadsRoot, S3URLPrefix: os.Getenv("S3_URL_PREFIX"), PublicBucket: os.Getenv("S3_BUCKET")}
	if err := MigrateLegacyDMImages(context.Background(), db, dm.NewImageStoreFromEnv(s3Client), config, s3Client); err != nil {
		log.Fatal(err)
	}
	log.Println("legacy DM image migration complete")
}
