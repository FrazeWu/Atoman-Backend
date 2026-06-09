package storage

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"gorm.io/gorm"

	"atoman/internal/model"
)

// SanitizeName sanitizes artist and album names for consistent file paths
// Rules:
// 1. Convert to lowercase
// 2. Replace spaces and special chars with underscores
// 3. Remove consecutive underscores
// 4. Strip leading/trailing underscores
func SanitizeName(name string) string {
	if name == "" {
		return "unknown"
	}

	// Convert to lowercase
	result := strings.ToLower(name)

	// Replace any non-alphanumeric character (except Chinese/Japanese/Korean characters) with underscore
	var sb strings.Builder
	for _, r := range result {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	result = sb.String()

	// Replace consecutive underscores with single underscore
	re := regexp.MustCompile(`_+`)
	result = re.ReplaceAllString(result, "_")

	// Strip leading/trailing underscores
	result = strings.Trim(result, "_")

	if result == "" {
		return "unknown"
	}

	return result
}

// InitS3Client initializes and returns an S3 client configured for Oracle Object Storage
func InitS3Client() (*s3.S3, error) {
	// AWS S3 session (compatible with Oracle Object Storage)
	sess, err := session.NewSession(&aws.Config{
		Region:           aws.String(os.Getenv("S3_REGION")),
		Endpoint:         aws.String(os.Getenv("S3_ENDPOINT")),
		Credentials:      credentials.NewStaticCredentials(os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), ""),
		S3ForcePathStyle: aws.Bool(true), // Required for Oracle Object Storage
	})
	if err != nil {
		return nil, err
	}

	s3Client := s3.New(sess)
	return s3Client, nil
}

// ValidateS3Connection tests the S3 connection and permissions
func ValidateS3Connection(s3Client *s3.S3) error {
	bucket := os.Getenv("S3_BUCKET")

	// Test bucket access
	_, err := s3Client.HeadBucket(&s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		log.Printf("Bucket %s not found or inaccessible, attempting to create it (err: %v)", bucket, err)
		_, createErr := s3Client.CreateBucket(&s3.CreateBucketInput{
			Bucket: aws.String(bucket),
		})
		if createErr != nil {
			log.Printf("Failed to create bucket %s: %v", bucket, createErr)
			return createErr
		}
		log.Printf("Successfully created bucket: %s", bucket)
	}

	log.Printf("S3 connection validated successfully for bucket: %s", bucket)
	return nil
}

// DeleteS3Object deletes an object from the configured S3 bucket
func DeleteS3Object(s3Client *s3.S3, key string) error {
	bucket := os.Getenv("S3_BUCKET")
	_, err := s3Client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		log.Printf("Failed to delete S3 object %s from bucket %s: %v", key, bucket, err)
		return err
	}
	log.Printf("Successfully deleted S3 object: %s from bucket: %s", key, bucket)
	return nil
}

// DeleteAlbumAndS3Objects deletes an album record and its associated S3 cover image
func DeleteAlbumAndS3Objects(db *gorm.DB, s3Client *s3.S3, album *model.Album) error {
	if album.CoverURL != "" {
		coverKey := strings.TrimPrefix(album.CoverURL, os.Getenv("S3_URL_PREFIX")+"/")
		if err := DeleteS3Object(s3Client, coverKey); err != nil {
			log.Printf("Failed to delete cover file %s from S3: %v", coverKey, err)
			// Don't return error here, try to delete from DB anyway
		}
	}

	if err := db.Delete(&album).Error; err != nil {
		log.Printf("Failed to delete album %d from DB: %v", album.ID, err)
		return err
	}

	return nil
}

// SaveFileLocally saves an uploaded file to local uploads directory
// Returns the local file path and URL for database storage
func SaveFileLocally(file interface{}, filename, artist, album string) (string, string, error) {
	// Import required for file operations
	var reader interface {
		Read([]byte) (int, error)
	}

	// Type assertion for file reader
	if f, ok := file.(interface{ Read([]byte) (int, error) }); ok {
		reader = f
	} else {
		return "", "", os.ErrInvalid
	}

	// Create sanitized directory names for consistency
	safeArtist := SanitizeName(artist)
	safeAlbum := SanitizeName(album)

	// Create directory structure: uploads/music/artist/album/
	dir := "uploads/music/" + safeArtist + "/" + safeAlbum
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", err
	}

	// Create local file path
	localPath := dir + "/" + filename

	// Create file
	dst, err := os.Create(localPath)
	if err != nil {
		return "", "", err
	}
	defer dst.Close()

	// Copy uploaded file to local file
	buffer := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			if _, writeErr := dst.Write(buffer[:n]); writeErr != nil {
				return "", "", writeErr
			}
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return "", "", err
		}
	}

	// Generate local URL (relative path for serving)
	localURL := "/uploads/music/" + safeArtist + "/" + safeAlbum + "/" + filename

	log.Printf("File saved locally: %s", localPath)
	return localPath, localURL, nil
}

// UploadLocalFileToS3 uploads a local file to S3 and returns the S3 URL
func UploadLocalFileToS3(s3Client *s3.S3, localPath string) (string, error) {
	if s3Client == nil {
		return "", fmt.Errorf("s3 client is nil")
	}

	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		return "", fmt.Errorf("S3_BUCKET is not configured")
	}

	s3URLPrefix := os.Getenv("S3_URL_PREFIX")
	if s3URLPrefix == "" {
		return "", fmt.Errorf("S3_URL_PREFIX is not configured")
	}

	// Open local file
	file, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Extract S3 key from local path
	// uploads/music/Artist/Album/file.mp3 -> music/Artist/Album/file.mp3
	s3Key := strings.TrimPrefix(localPath, "uploads/")
	s3Key = strings.ReplaceAll(s3Key, "\\", "/") // Windows compatibility

	// Upload to S3
	_, err = s3Client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(s3Key),
		Body:   file,
		ACL:    aws.String("public-read"),
	})
	if err != nil {
		return "", err
	}

	// Generate S3 URL
	s3URL := s3URLPrefix + "/" + s3Key
	log.Printf("File uploaded to S3: %s -> %s", localPath, s3URL)

	return s3URL, nil
}

// DeleteLocalFile deletes a file from local uploads directory
func DeleteLocalFile(localPath string) error {
	if localPath == "" {
		return nil
	}

	// Only delete files in uploads directory for safety
	if !strings.HasPrefix(localPath, "uploads/") {
		log.Printf("Skipping deletion of non-upload file: %s", localPath)
		return nil
	}

	if err := os.Remove(localPath); err != nil {
		log.Printf("Failed to delete local file %s: %v", localPath, err)
		return err
	}

	log.Printf("Successfully deleted local file: %s", localPath)
	return nil
}

// GetLocalPathFromURL extracts local file path from URL
// /uploads/music/Artist/Album/file.mp3 -> uploads/music/Artist/Album/file.mp3
func GetLocalPathFromURL(url string) string {
	if strings.HasPrefix(url, "/uploads/") {
		return strings.TrimPrefix(url, "/")
	}
	return ""
}

// SaveFileToPath writes an io.Reader to the specified destination path.
// The caller is responsible for creating parent directories beforehand.
func SaveFileToPath(reader interface{ Read([]byte) (int, error) }, destPath string) error {
	dst, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if readErr != nil {
			if readErr.Error() == "EOF" {
				break
			}
			return readErr
		}
	}
	return nil
}

// DeleteSongAndS3Objects deletes a song record and its associated files
func DeleteSongAndS3Objects(db *gorm.DB, s3Client *s3.S3, song *model.Song) error {
	if song.AudioURL != "" {
		if song.AudioSource == "local" {
			localPath := GetLocalPathFromURL(song.AudioURL)
			if localPath != "" {
				DeleteLocalFile(localPath)
			}
		} else if song.AudioSource == "s3" {
			audioKey := strings.TrimPrefix(song.AudioURL, os.Getenv("S3_URL_PREFIX")+"/")
			if err := DeleteS3Object(s3Client, audioKey); err != nil {
				log.Printf("Failed to delete audio file %s from S3: %v", audioKey, err)
			}
		}
	}

	if song.CoverURL != "" && (song.Album == nil || song.CoverURL != song.Album.CoverURL) {
		if song.CoverSource == "local" {
			localPath := GetLocalPathFromURL(song.CoverURL)
			if localPath != "" {
				DeleteLocalFile(localPath)
			}
		} else if song.CoverSource == "s3" {
			coverKey := strings.TrimPrefix(song.CoverURL, os.Getenv("S3_URL_PREFIX")+"/")
			if err := DeleteS3Object(s3Client, coverKey); err != nil {
				log.Printf("Failed to delete cover file %s from S3: %v", coverKey, err)
			}
		}
	}

	if err := db.Delete(&song).Error; err != nil {
		log.Printf("Failed to delete song %d from DB: %v", song.ID, err)
		return err
	}

	return nil
}
