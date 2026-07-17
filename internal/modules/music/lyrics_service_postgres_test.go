package music

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func openLyricsPostgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("MUSIC_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("MUSIC_POSTGRES_TEST_DSN is not set")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("MUSIC_POSTGRES_TEST_DSN must be a PostgreSQL URL: %v", err)
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	schema := "music_lyrics_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	if err := admin.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)).Error; err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schema)).Error
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open isolated PostgreSQL schema: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Song{}, &model.MusicSongLyric{}, &model.MusicSongLyricLine{},
		&model.MusicSongLyricVersion{}, &model.MusicLyricAnnotation{}, &model.MusicLyricAnnotationVote{},
	); err != nil {
		t.Fatalf("migrate isolated schema: %v", err)
	}
	return db
}

func createLyricsPostgresFixture(t *testing.T, db *gorm.DB) (authctx.CurrentUser, model.Song) {
	t.Helper()
	suffix := uuid.NewString()
	userModel := model.User{Username: "lyrics-" + suffix, Email: "lyrics-" + suffix + "@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&userModel).Error; err != nil {
		t.Fatal(err)
	}
	song := model.Song{Title: "PostgreSQL Lyrics " + suffix, AudioURL: "/postgres.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	return authctx.CurrentUser{ID: userModel.UUID, Username: userModel.Username, Role: authctx.RoleUser}, song
}

func TestPostgresConcurrentSaveSongLyricsAcrossServices(t *testing.T) {
	db := openLyricsPostgresTestDB(t)
	user, song := createLyricsPostgresFixture(t, db)
	services := []*Service{NewService(db), NewService(db)}
	const saves = 8
	errs := make(chan error, saves)
	var wg sync.WaitGroup
	for index := range saves {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := services[index%len(services)].SaveSongLyrics(user, song.ID, SaveLyricsInput{
				Content: fmt.Sprintf("line %d", index), Format: "plain", EditSummary: "concurrent",
			})
			errs <- err
		}(index)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent save: %v", err)
		}
	}
	var versions []model.MusicSongLyricVersion
	if err := db.Where("song_id = ?", song.ID).Order("version").Find(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if len(versions) != saves {
		t.Fatalf("expected %d versions, got %d", saves, len(versions))
	}
	for index, version := range versions {
		if version.Version != index+1 {
			t.Fatalf("expected continuous versions, got %#v", versions)
		}
	}
	var current model.MusicSongLyric
	if err := db.First(&current, "song_id = ?", song.ID).Error; err != nil || current.Version != saves {
		t.Fatalf("unexpected current lyric: %#v, %v", current, err)
	}
}

func TestPostgresConcurrentFirstVoteAcrossServices(t *testing.T) {
	db := openLyricsPostgresTestDB(t)
	user, song := createLyricsPostgresFixture(t, db)
	services := []*Service{NewService(db), NewService(db)}
	lyrics, err := services[0].SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	annotation, err := services[0].CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "note",
	})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, len(services))
	var wg sync.WaitGroup
	for _, service := range services {
		wg.Add(1)
		go func(service *Service) {
			defer wg.Done()
			_, err := service.SetLyricAnnotationVote(user, song.ID, annotation.ID, "up")
			errs <- err
		}(service)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent first vote: %v", err)
		}
	}
	var votes int64
	if err := db.Model(&model.MusicLyricAnnotationVote{}).
		Where("annotation_id = ? AND user_id = ?", annotation.ID, user.ID).Count(&votes).Error; err != nil {
		t.Fatal(err)
	}
	if votes != 1 {
		t.Fatalf("expected one live vote, got %d", votes)
	}
}
