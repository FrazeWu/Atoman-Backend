package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunContentPublicationEventIndexesCreatesDispatchCandidateIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.ContentPublicationEvent{})

	if err := RunContentPublicationEventIndexes(db); err != nil {
		t.Fatalf("run content publication event indexes: %v", err)
	}
	if !db.Migrator().HasIndex("content_publication_events", "idx_content_publication_events_dispatch_candidates") {
		t.Fatal("expected content publication dispatch candidate index")
	}
	if err := RunContentPublicationEventIndexes(db); err != nil {
		t.Fatalf("rerun content publication event indexes: %v", err)
	}
}
