package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestMigrateToRevisionSystemUsesSystemUserForLegacyMissingUploaders(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Artist{},
		&model.Album{},
		&model.Song{},
		&model.AlbumCorrection{},
		&model.SongCorrection{},
	)

	album := model.Album{
		Base:   model.Base{ID: uuid.New(), CreatedAt: time.Now()},
		Title:  "Legacy Album",
		Status: "approved",
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}

	song := model.Song{
		Base:     model.Base{ID: uuid.New(), CreatedAt: time.Now()},
		Title:    "Legacy Song",
		AudioURL: "legacy.mp3",
		Status:   "approved",
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}

	albumCorrection := model.AlbumCorrection{
		Base:           model.Base{ID: uuid.New(), CreatedAt: time.Now().Add(time.Second)},
		AlbumID:        album.ID,
		Status:         "pending",
		CorrectedTitle: "Legacy Album Fixed",
		Reason:         "legacy correction without user",
	}
	if err := db.Create(&albumCorrection).Error; err != nil {
		t.Fatalf("create album correction: %v", err)
	}

	songCorrection := model.SongCorrection{
		Base:           model.Base{ID: uuid.New(), CreatedAt: time.Now().Add(time.Second)},
		SongID:         song.ID,
		Status:         "pending",
		FieldName:      "title",
		CurrentValue:   song.Title,
		CorrectedValue: "Legacy Song Fixed",
		Reason:         "legacy correction without user",
	}
	if err := db.Create(&songCorrection).Error; err != nil {
		t.Fatalf("create song correction: %v", err)
	}

	if err := MigrateToRevisionSystem(db); err != nil {
		t.Fatalf("migrate to revision system: %v", err)
	}

	assertLegacyRevisionEditor(t, db, "album", album.ID, 1)
	assertLegacyRevisionEditor(t, db, "album", album.ID, 2)
	assertLegacyRevisionEditor(t, db, "song", song.ID, 1)
	assertLegacyRevisionEditor(t, db, "song", song.ID, 2)
}

func assertLegacyRevisionEditor(t *testing.T, db *gorm.DB, contentType string, contentID uuid.UUID, version int) {
	t.Helper()

	var revision model.Revision
	if err := db.Where("content_type = ? AND content_id = ? AND version_number = ?", contentType, contentID, version).
		First(&revision).Error; err != nil {
		t.Fatalf("find %s revision v%d: %v", contentType, version, err)
	}
	if revision.EditorID == contentID {
		t.Fatalf("%s revision v%d editor_id must not fallback to content id %s", contentType, version, contentID)
	}
	if revision.ReviewerID != nil {
		t.Fatalf("%s revision v%d reviewer_id = %s, want nil when legacy correction has no approver", contentType, version, *revision.ReviewerID)
	}

	var user model.User
	if err := db.Where("uuid = ?", revision.EditorID).First(&user).Error; err != nil {
		t.Fatalf("%s revision v%d editor_id must reference fallback user: %v", contentType, version, err)
	}
	if user.Username != "system-migration" || user.Role != "admin" {
		t.Fatalf("fallback user = %q/%q, want system-migration/admin", user.Username, user.Role)
	}
}
