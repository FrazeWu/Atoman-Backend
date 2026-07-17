package migrations

import (
	"testing"

	"atoman/internal/testdb"
)

func TestRunForumReportUniqueIndexDeduplicatesLegacyRows(t *testing.T) {
	db := testdb.Open(t)
	if err := db.Exec(`CREATE TABLE forum_reports (
		id TEXT PRIMARY KEY,
		created_at DATETIME,
		user_id TEXT NOT NULL,
		target_type TEXT NOT NULL,
		target_id TEXT NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO forum_reports (id, created_at, user_id, target_type, target_id) VALUES
		('1', '2026-01-01', 'u1', 'topic', 't1'),
		('2', '2026-01-02', 'u1', 'topic', 't1')`).Error; err != nil {
		t.Fatal(err)
	}

	if err := RunForumReportUniqueIndex(db); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Table("forum_reports").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one deduplicated report, got %d", count)
	}
	if !db.Migrator().HasIndex("forum_reports", "idx_forum_reports_user_target") {
		t.Fatal("expected unique report index")
	}
}
