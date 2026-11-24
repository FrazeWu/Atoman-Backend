package indexnow

import (
	"errors"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"gorm.io/gorm"
)

func TestEnqueuePathsTxCommitsAndRollsBackWithPublication(t *testing.T) {
	db := testdb.OpenPostgres(t, "indexnow_tx")
	testdb.Migrate(t, db, &model.IndexNowSubmission{})
	submitter, err := New(Config{Key: "test-key-12345678"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker := &Worker{db: db, submitter: submitter}
	previous := activeWorker.Swap(worker)
	t.Cleanup(func() { activeWorker.Store(previous) })

	sentinel := errors.New("publication rolled back")
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := EnqueuePathsTx(tx, "/books/lists/private-draft"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("unexpected rollback: %v", err)
	}
	var count int64
	if err := db.Model(&model.IndexNowSubmission{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rolled-back URL was retained: count=%d err=%v", count, err)
	}
	for range 2 {
		if err := db.Transaction(func(tx *gorm.DB) error {
			return EnqueuePathsTx(tx, "/books/lists/public", "/books/lists/public")
		}); err != nil {
			t.Fatal(err)
		}
	}
	var row model.IndexNowSubmission
	if err := db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.URL != "https://www.atoman.org/books/lists/public" || row.Revision != 2 {
		t.Fatalf("expected one committed URL with revision 2, got %+v", row)
	}
}
