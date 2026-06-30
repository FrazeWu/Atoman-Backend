package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunContentProtectionLiveUniqueIndexAllowsRecreateAfterSoftDelete(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.ContentProtection{})

	contentID := uuid.New()
	initial := model.ContentProtection{
		ContentType:     "album",
		ContentID:       contentID,
		ProtectionLevel: "full",
		ProtectedBy:     uuid.New(),
	}
	if err := db.Create(&initial).Error; err != nil {
		t.Fatalf("create initial protection: %v", err)
	}

	if err := db.Delete(&initial).Error; err != nil {
		t.Fatalf("soft delete initial protection: %v", err)
	}

	recreated := model.ContentProtection{
		ContentType:     "album",
		ContentID:       contentID,
		ProtectionLevel: "semi",
		ProtectedBy:     uuid.New(),
	}
	if err := db.Create(&recreated).Error; err == nil {
		t.Fatal("expected recreate before migration to fail due to unique index")
	}

	if err := RunContentProtectionLiveUniqueIndex(db); err != nil {
		t.Fatalf("run content protection unique index migration: %v", err)
	}

	recreated.ID = uuid.Nil
	recreated.CreatedAt = time.Time{}
	recreated.UpdatedAt = time.Time{}
	if err := db.Create(&recreated).Error; err != nil {
		t.Fatalf("recreate protection after migration: %v", err)
	}

	assertIndexExists(t, db, "content_protections", "idx_content_protections_live_content")

	var rows []model.ContentProtection
	if err := db.Unscoped().
		Where("content_type = ? AND content_id = ?", "album", contentID).
		Order("created_at ASC").
		Find(&rows).Error; err != nil {
		t.Fatalf("load protections: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 protection rows including soft-deleted history, got %d", len(rows))
	}
	if !rows[0].DeletedAt.Valid {
		t.Fatalf("expected first protection row to be soft-deleted")
	}
	if rows[1].DeletedAt.Valid {
		t.Fatalf("expected re-enabled protection row to remain live")
	}
	if rows[1].ProtectionLevel != "semi" {
		t.Fatalf("expected latest protection level semi, got %q", rows[1].ProtectionLevel)
	}
}
