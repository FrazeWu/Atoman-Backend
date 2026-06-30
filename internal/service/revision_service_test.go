package service

import (
	"sync"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestCreateRevisionConcurrentAutoApproveKeepsUniqueVersionAndCurrent(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Revision{}, &model.EditConflict{})

	contentID := uuid.New()
	editorID := uuid.New()
	base := model.Revision{
		ContentType:     "album",
		ContentID:       contentID,
		VersionNumber:   1,
		ContentSnapshot: []byte(`{"title":"base","note":"start"}`),
		EditorID:        editorID,
		EditSummary:     "base",
		EditType:        "creation",
		Status:          "approved",
		IsCurrent:       true,
	}
	if err := db.Create(&base).Error; err != nil {
		t.Fatalf("create base revision: %v", err)
	}

	service := NewRevisionService(db)
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)

	for _, title := range []string{"first", "second"} {
		title := title
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, conflicts, err := service.CreateRevision(
				"album",
				contentID,
				editorID,
				map[string]interface{}{"title": title},
				title,
				1,
				true,
			)
			if len(conflicts) > 0 {
				return
			}
			errs <- err
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 concurrent edit to create a revision, got %d", successes)
	}

	assertSingleCurrentAndUniqueVersions(t, db, contentID)
}

func TestApproveRevisionKeepsOnlyOneCurrentWithUniqueIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Revision{})

	contentID := uuid.New()
	editorID := uuid.New()
	reviewerID := uuid.New()
	current := model.Revision{
		ContentType:     "album",
		ContentID:       contentID,
		VersionNumber:   1,
		ContentSnapshot: []byte(`{"title":"base"}`),
		EditorID:        editorID,
		EditSummary:     "base",
		EditType:        "creation",
		Status:          "approved",
		IsCurrent:       true,
	}
	if err := db.Create(&current).Error; err != nil {
		t.Fatalf("create current revision: %v", err)
	}

	pending := model.Revision{
		ContentType:        "album",
		ContentID:          contentID,
		VersionNumber:      2,
		PreviousRevisionID: &current.ID,
		ContentSnapshot:    []byte(`{"title":"next"}`),
		EditorID:           editorID,
		EditSummary:        "next",
		EditType:           "edit",
		Status:             "pending",
		IsCurrent:          false,
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create pending revision: %v", err)
	}

	service := NewRevisionService(db)
	if err := service.ApproveRevision(pending.ID, reviewerID, "ok"); err != nil {
		t.Fatalf("approve revision: %v", err)
	}

	assertSingleCurrentAndUniqueVersions(t, db, contentID)

	var approved model.Revision
	if err := db.First(&approved, "id = ?", pending.ID).Error; err != nil {
		t.Fatalf("load approved revision: %v", err)
	}
	if approved.Status != "approved" || !approved.IsCurrent {
		t.Fatalf("expected pending revision to be approved current, got status=%q current=%v", approved.Status, approved.IsCurrent)
	}
}

func assertSingleCurrentAndUniqueVersions(t *testing.T, db *gorm.DB, contentID uuid.UUID) {
	t.Helper()

	var currentCount int64
	if err := db.Model(&model.Revision{}).
		Where("content_type = ? AND content_id = ? AND is_current = ?", "album", contentID, true).
		Count(&currentCount).Error; err != nil {
		t.Fatalf("count current revisions: %v", err)
	}
	if currentCount != 1 {
		t.Fatalf("expected 1 current revision, got %d", currentCount)
	}

	var rows []struct {
		VersionNumber int
		Count         int64
	}
	if err := db.Model(&model.Revision{}).
		Select("version_number, count(*) as count").
		Where("content_type = ? AND content_id = ?", "album", contentID).
		Group("version_number").
		Having("count(*) > 1").
		Scan(&rows).Error; err != nil {
		t.Fatalf("query duplicate versions: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected unique revision versions, got duplicates: %+v", rows)
	}
}
