package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunRevisionUniqueIndexesDeduplicatesAndCreatesIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Revision{})
	if err := db.Exec(`DROP INDEX IF EXISTS idx_revisions_content_version`).Error; err != nil {
		t.Fatalf("drop content version index: %v", err)
	}
	if err := db.Exec(`DROP INDEX IF EXISTS idx_revisions_current_content`).Error; err != nil {
		t.Fatalf("drop current content index: %v", err)
	}

	contentID := uuid.New()
	editorID := uuid.New()
	duplicateOld := model.Revision{
		ContentType:     "album",
		ContentID:       contentID,
		VersionNumber:   2,
		ContentSnapshot: []byte(`{"title":"old"}`),
		EditorID:        editorID,
		EditSummary:     "old",
		Status:          "approved",
		IsCurrent:       true,
	}
	duplicateNew := model.Revision{
		ContentType:     "album",
		ContentID:       contentID,
		VersionNumber:   2,
		ContentSnapshot: []byte(`{"title":"new"}`),
		EditorID:        editorID,
		EditSummary:     "new",
		Status:          "approved",
		IsCurrent:       true,
	}
	if err := db.Create(&duplicateOld).Error; err != nil {
		t.Fatalf("create old duplicate revision: %v", err)
	}
	if err := db.Create(&duplicateNew).Error; err != nil {
		t.Fatalf("create new duplicate revision: %v", err)
	}

	if err := RunRevisionUniqueIndexes(db); err != nil {
		t.Fatalf("run revision unique indexes migration: %v", err)
	}

	assertIndexExists(t, db, "revisions", "idx_revisions_content_version")
	assertIndexExists(t, db, "revisions", "idx_revisions_current_content")

	var revisions []model.Revision
	if err := db.Where("content_type = ? AND content_id = ?", "album", contentID).
		Order("version_number ASC").
		Find(&revisions).Error; err != nil {
		t.Fatalf("query revisions: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("expected duplicate revision versions to be deduplicated, got %d rows", len(revisions))
	}
	if !revisions[0].IsCurrent {
		t.Fatal("expected surviving latest revision to remain current")
	}

	conflictingVersion := model.Revision{
		ContentType:     "album",
		ContentID:       contentID,
		VersionNumber:   2,
		ContentSnapshot: []byte(`{"title":"conflict"}`),
		EditorID:        editorID,
		EditSummary:     "conflict",
		Status:          "approved",
		IsCurrent:       false,
	}
	if err := db.Create(&conflictingVersion).Error; err == nil {
		t.Fatal("expected duplicate revision version insert to fail")
	}

	next := model.Revision{
		ContentType:     "album",
		ContentID:       contentID,
		VersionNumber:   3,
		ContentSnapshot: []byte(`{"title":"next"}`),
		EditorID:        editorID,
		EditSummary:     "next",
		Status:          "approved",
		IsCurrent:       true,
	}
	if err := db.Create(&next).Error; err == nil {
		t.Fatal("expected second current revision insert to fail")
	}
}
