package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunDiscussionReadStateMigrationCreatesPerUserReadStateTable(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Discussion{})

	if err := RunDiscussionReadStateMigration(db); err != nil {
		t.Fatalf("run discussion read state migration: %v", err)
	}

	if !db.Migrator().HasTable(&model.DiscussionReadState{}) {
		t.Fatal("expected discussion_read_states table")
	}

	discussion := model.Discussion{
		ContentType: "album",
		ContentID:   uuid.New(),
		UserID:      uuid.New(),
		Content:     "discussion",
		Status:      "active",
	}
	if err := db.Create(&discussion).Error; err != nil {
		t.Fatalf("create discussion: %v", err)
	}

	readState := model.DiscussionReadState{
		DiscussionID: discussion.ID,
		UserID:       uuid.New(),
		ReadAt:       time.Now().UTC(),
	}
	if err := db.Create(&readState).Error; err != nil {
		t.Fatalf("create discussion read state: %v", err)
	}
}
