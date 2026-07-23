package migrations

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRunDMV2PostgresConstraintsAndIndexes(t *testing.T) {
	db := openDMV2Postgres(t)
	if err := RunDMV2Migration(db); err != nil {
		t.Fatalf("run dm v2 migration: %v", err)
	}

	first, second := uuid.New(), uuid.New()
	if first.String() > second.String() {
		first, second = second, first
	}
	conversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: first, ParticipantBType: model.DMPartyUser, ParticipantB: second}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create valid conversation: %v", err)
	}
	duplicate := conversation
	duplicate.ID = uuid.Nil
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("expected typed conversation unique index")
	}
	if err := db.Exec(`INSERT INTO dm_conversations (id, participant_a_type, participant_a, participant_b_type, participant_b, created_at, updated_at) VALUES (?, 'channel', ?, 'user', ?, NOW(), NOW())`, uuid.New(), first, second).Error; err == nil {
		t.Fatal("expected participant A type check constraint")
	}
	if err := db.Exec(`INSERT INTO dm_message_reports (id, message_id, reporter_user_id, reported_actor_user_id, reason, status, created_at, updated_at) VALUES (?, ?, ?, ?, 'spam', 'invalid', NOW(), NOW())`, uuid.New(), uuid.New(), uuid.New(), uuid.New()).Error; err == nil {
		t.Fatal("expected report status check constraint")
	}
}

func openDMV2Postgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	sqlDB, err := admin.DB()
	if err != nil {
		t.Fatalf("open postgres db: %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	schema := "dm_v2_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		_ = sqlDB.Close()
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse postgres url: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open schema postgres: %v", err)
	}
	return db
}
