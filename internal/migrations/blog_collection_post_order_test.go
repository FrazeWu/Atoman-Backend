package migrations

import (
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunBlogCollectionPostOrderMigrationBackfillsPositionsWithoutRowID(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.PostCollection{})

	collectionA := uuid.New()
	collectionB := uuid.New()
	post1 := uuid.New()
	post2 := uuid.New()
	post3 := uuid.New()

	links := []model.PostCollection{
		{PostID: post1, CollectionID: collectionA},
		{PostID: post2, CollectionID: collectionA},
		{PostID: post3, CollectionID: collectionB},
	}
	if err := db.Create(&links).Error; err != nil {
		t.Fatalf("seed post_collections: %v", err)
	}

	if err := RunBlogCollectionPostOrderMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	var migrated []model.PostCollection
	if err := db.Order("collection_id ASC, position ASC").Find(&migrated).Error; err != nil {
		t.Fatalf("load migrated links: %v", err)
	}

	if len(migrated) != 3 {
		t.Fatalf("expected 3 post collection rows, got %d", len(migrated))
	}

	if migrated[0].CollectionID == migrated[1].CollectionID && migrated[0].Position == migrated[1].Position {
		t.Fatalf("expected distinct positions within collection, got %#v", migrated[:2])
	}
}

func TestBlogCollectionPostOrderBackfillQueryDoesNotUseSQLiteRowID(t *testing.T) {
	query := blogCollectionPostOrderBackfillQuery()
	if strings.Contains(strings.ToLower(query), "rowid") {
		t.Fatalf("expected portable query without rowid, got %q", query)
	}
}
