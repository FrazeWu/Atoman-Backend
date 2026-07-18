package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunUserBlocksMigrationCreatesTableAndUniquePairIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})

	if err := RunUserBlocksMigration(db); err != nil {
		t.Fatalf("run user blocks migration: %v", err)
	}
	if !db.Migrator().HasTable(&model.UserBlock{}) {
		t.Fatal("expected user_blocks table to exist")
	}
	if !db.Migrator().HasIndex("user_blocks", "uq_user_block_pair") {
		t.Fatal("expected unique user block pair index")
	}
	if err := RunUserBlocksMigration(db); err != nil {
		t.Fatalf("run idempotent user blocks migration: %v", err)
	}
}
