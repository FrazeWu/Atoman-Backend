package migrations

import (
	"strings"
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

	var definition string
	if err := db.Raw(`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, "uq_user_block_pair").Scan(&definition).Error; err != nil {
		t.Fatalf("load user_blocks index definition: %v", err)
	}
	if !strings.Contains(strings.ToLower(definition), "deleted_at is null") {
		t.Fatalf("expected live-only unique index, got %q", definition)
	}
}

func TestRunUserBlocksMigrationAllowsReblockAfterSoftDelete(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.UserBlock{})

	if err := RunUserBlocksMigration(db); err != nil {
		t.Fatalf("run user blocks migration: %v", err)
	}

	blocker := model.User{Username: "blocker-user", Email: "blocker@example.test", Password: "hash", IsActive: true}
	blocked := model.User{Username: "blocked-user", Email: "blocked@example.test", Password: "hash", IsActive: true}
	if err := db.Create(&blocker).Error; err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	if err := db.Create(&blocked).Error; err != nil {
		t.Fatalf("create blocked: %v", err)
	}

	first := model.UserBlock{BlockerID: blocker.UUID, BlockedID: blocked.UUID}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create initial block: %v", err)
	}
	if err := db.Delete(&first).Error; err != nil {
		t.Fatalf("soft delete block: %v", err)
	}

	second := model.UserBlock{BlockerID: blocker.UUID, BlockedID: blocked.UUID}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("recreate block after soft delete: %v", err)
	}

	var liveCount int64
	if err := db.Model(&model.UserBlock{}).
		Where("blocker_id = ? AND blocked_id = ?", blocker.UUID, blocked.UUID).
		Count(&liveCount).Error; err != nil {
		t.Fatalf("count live blocks: %v", err)
	}
	if liveCount != 1 {
		t.Fatalf("expected one live block, got %d", liveCount)
	}

	var totalCount int64
	if err := db.Unscoped().Model(&model.UserBlock{}).
		Where("blocker_id = ? AND blocked_id = ?", blocker.UUID, blocked.UUID).
		Count(&totalCount).Error; err != nil {
		t.Fatalf("count total blocks: %v", err)
	}
	if totalCount != 2 {
		t.Fatalf("expected one deleted row plus one live row, got %d", totalCount)
	}
}
