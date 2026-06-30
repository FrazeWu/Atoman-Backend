package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"atoman/internal/storage"
)

type migration struct {
	table     string
	column    string
	id        string
	oldURL    string
	oldKey    string
	newKey    string
	newURL    string
	updateSQL string
	updateArg any
}

func parseObjectURL(rawURL, prefix string) (string, bool) {
	rawURL = strings.TrimSpace(rawURL)
	prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
	if rawURL == "" || prefix == "" || !strings.HasPrefix(rawURL, prefix+"/") {
		return "", false
	}
	key, err := url.PathUnescape(strings.TrimPrefix(rawURL, prefix+"/"))
	if err != nil {
		return "", false
	}
	return strings.TrimLeft(key, "/"), key != ""
}

func buildUserMediaKey(module, kind, userID, filename string, createdAt time.Time) string {
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return fmt.Sprintf("%s/%s/users/%s/%04d/%02d/%s", strings.Trim(module, "/"), strings.Trim(kind, "/"), strings.Trim(userID, "/"), createdAt.UTC().Year(), int(createdAt.UTC().Month()), strings.TrimLeft(filename, "/"))
}

func buildLegacyVideoKey(oldKey, userID, kind string, createdAt time.Time) (string, bool) {
	prefix := "video/" + kind + "/" + userID + "/"
	if !strings.HasPrefix(oldKey, prefix) || strings.HasPrefix(oldKey, "video/"+kind+"/users/") {
		return "", false
	}
	filename := path.Base(oldKey)
	if filename == "." || filename == "/" {
		return "", false
	}
	return buildUserMediaKey("video", kind, userID, filename, createdAt), true
}

func buildMusicAudioKey(oldKey, albumID, songID string) string {
	return "music/audio/albums/" + albumID + "/" + songID + path.Ext(oldKey)
}

func buildMusicCoverKey(oldKey, albumID string) string {
	return "music/covers/albums/" + albumID + "/cover" + path.Ext(oldKey)
}

func makeURL(prefix, key string) string {
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(key, "/")
}

func copyObject(s3Client *s3.S3, bucket, oldKey, newKey string) error {
	if oldKey == newKey {
		return nil
	}
	escapedOldKey := strings.ReplaceAll(url.PathEscape(oldKey), "%2F", "/")
	_, err := s3Client.CopyObject(&s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		CopySource: aws.String(bucket + "/" + escapedOldKey),
		Key:        aws.String(newKey),
	})
	return err
}

func collectVideoMigrations(db *gorm.DB, s3Prefix string) ([]migration, error) {
	var videos []model.Video
	if err := db.Find(&videos).Error; err != nil {
		return nil, err
	}

	var migrations []migration
	for _, video := range videos {
		userID := video.UserID.String()
		if oldKey, ok := parseObjectURL(video.VideoURL, s3Prefix); ok {
			if newKey, should := buildLegacyVideoKey(oldKey, userID, "files", video.CreatedAt); should {
				migrations = append(migrations, migration{
					table:     "videos",
					column:    "video_url",
					id:        video.ID.String(),
					oldURL:    video.VideoURL,
					oldKey:    oldKey,
					newKey:    newKey,
					newURL:    makeURL(s3Prefix, newKey),
					updateSQL: "update videos set video_url = ? where id = ?",
					updateArg: video.ID,
				})
			}
		}
		if oldKey, ok := parseObjectURL(video.ThumbnailURL, s3Prefix); ok {
			if newKey, should := buildLegacyVideoKey(oldKey, userID, "covers", video.CreatedAt); should {
				migrations = append(migrations, migration{
					table:     "videos",
					column:    "thumbnail_url",
					id:        video.ID.String(),
					oldURL:    video.ThumbnailURL,
					oldKey:    oldKey,
					newKey:    newKey,
					newURL:    makeURL(s3Prefix, newKey),
					updateSQL: "update videos set thumbnail_url = ? where id = ?",
					updateArg: video.ID,
				})
			}
		}
	}
	return migrations, nil
}

func collectMusicMigrations(db *gorm.DB, s3Prefix string) ([]migration, error) {
	var songs []model.Song
	if err := db.Preload("Album").Find(&songs).Error; err != nil {
		return nil, err
	}

	var migrations []migration
	seenAlbumCovers := map[string]bool{}
	for _, song := range songs {
		if song.AlbumID == nil {
			continue
		}
		albumID := song.AlbumID.String()
		if oldKey, ok := parseObjectURL(song.AudioURL, s3Prefix); ok && strings.HasPrefix(oldKey, "music/") && !strings.HasPrefix(oldKey, "music/audio/albums/") && !strings.HasPrefix(oldKey, "music/audio/uploads/") {
			newKey := buildMusicAudioKey(oldKey, albumID, song.ID.String())
			migrations = append(migrations, migration{
				table:     "Songs",
				column:    "audio_url",
				id:        song.ID.String(),
				oldURL:    song.AudioURL,
				oldKey:    oldKey,
				newKey:    newKey,
				newURL:    makeURL(s3Prefix, newKey),
				updateSQL: `update "Songs" set audio_url = ? where id = ?`,
				updateArg: song.ID,
			})
		}
		if oldKey, ok := parseObjectURL(song.CoverURL, s3Prefix); ok && strings.HasPrefix(oldKey, "music/") && !strings.HasPrefix(oldKey, "music/covers/albums/") && !strings.HasPrefix(oldKey, "music/covers/uploads/") {
			newKey := buildMusicCoverKey(oldKey, albumID)
			migrations = append(migrations, migration{
				table:     "Songs",
				column:    "cover_url",
				id:        song.ID.String(),
				oldURL:    song.CoverURL,
				oldKey:    oldKey,
				newKey:    newKey,
				newURL:    makeURL(s3Prefix, newKey),
				updateSQL: `update "Songs" set cover_url = ? where id = ?`,
				updateArg: song.ID,
			})
		}
		if song.Album != nil {
			if oldKey, ok := parseObjectURL(song.Album.CoverURL, s3Prefix); ok && strings.HasPrefix(oldKey, "music/") && !strings.HasPrefix(oldKey, "music/covers/albums/") && !strings.HasPrefix(oldKey, "music/covers/uploads/") {
				if seenAlbumCovers[albumID] {
					continue
				}
				seenAlbumCovers[albumID] = true
				newKey := buildMusicCoverKey(oldKey, albumID)
				migrations = append(migrations, migration{
					table:     "Albums",
					column:    "cover_url",
					id:        albumID,
					oldURL:    song.Album.CoverURL,
					oldKey:    oldKey,
					newKey:    newKey,
					newURL:    makeURL(s3Prefix, newKey),
					updateSQL: `update "Albums" set cover_url = ? where id = ?`,
					updateArg: *song.AlbumID,
				})
			}
		}
	}
	return migrations, nil
}

func main() {
	apply := flag.Bool("apply", false, "copy objects and update database")
	envFile := flag.String("env", ".env.dev", "env file to load")
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

	s3Prefix := strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/")
	bucket := os.Getenv("S3_BUCKET")
	videoMigrations, err := collectVideoMigrations(db, s3Prefix)
	if err != nil {
		log.Fatalf("collect video migrations: %v", err)
	}
	musicMigrations, err := collectMusicMigrations(db, s3Prefix)
	if err != nil {
		log.Fatalf("collect music migrations: %v", err)
	}
	migrations := append(videoMigrations, musicMigrations...)

	for _, m := range migrations {
		fmt.Printf("%s.%s %s\n  %s\n  -> %s\n", m.table, m.column, m.id, m.oldKey, m.newKey)
	}
	log.Printf("planned migrations: %d", len(migrations))
	if !*apply {
		log.Println("dry run only; rerun with -apply to copy objects and update database")
		return
	}

	for _, m := range migrations {
		if err := copyObject(s3Client, bucket, m.oldKey, m.newKey); err != nil {
			log.Fatalf("copy %s -> %s: %v", m.oldKey, m.newKey, err)
		}
		if err := db.Exec(m.updateSQL, m.newURL, m.updateArg).Error; err != nil {
			log.Fatalf("update %s.%s %s: %v", m.table, m.column, m.id, err)
		}
	}
	log.Printf("applied migrations: %d", len(migrations))
}
