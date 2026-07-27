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

func TestRunDMV2PostgresUpgradesLegacyTables(t *testing.T) {
	db := openDMV2Postgres(t)
	if err := db.AutoMigrate(&legacyDMConversation{}, &legacyDMMessage{}); err != nil {
		t.Fatalf("create legacy dm tables: %v", err)
	}
	first, second := uuid.New(), uuid.New()
	if first.String() > second.String() {
		first, second = second, first
	}
	conversationID, messageID := uuid.New(), uuid.New()
	if err := db.Create(&legacyDMConversation{ID: conversationID, ParticipantA: first, ParticipantB: second}).Error; err != nil {
		t.Fatalf("seed legacy conversation: %v", err)
	}
	if err := db.Create(&legacyDMMessage{ID: messageID, ConversationID: conversationID, SenderID: first, Content: "legacy"}).Error; err != nil {
		t.Fatalf("seed legacy message: %v", err)
	}
	for _, statement := range []string{
		`ALTER TABLE dm_messages ADD COLUMN actor_user_id uuid`,
		`ALTER TABLE dm_messages ADD COLUMN client_message_id uuid`,
		`UPDATE dm_messages SET actor_user_id = '00000000-0000-0000-0000-000000000000', client_message_id = '00000000-0000-0000-0000-000000000000'`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("prepare legacy zero uuid values: %v", err)
		}
	}

	if err := RunDMV2Migration(db); err != nil {
		t.Fatalf("upgrade legacy dm tables: %v", err)
	}
	var message model.DMMessage
	if err := db.First(&message, "id = ?", messageID).Error; err != nil {
		t.Fatalf("load upgraded message: %v", err)
	}
	if message.SenderType != model.DMPartyUser || message.ActorUserID != first || message.ClientMessageID != messageID {
		t.Fatalf("upgraded message fields = %#v", message)
	}
	for table, columns := range map[string][]string{
		"dm_conversations": {"participant_a_type", "participant_b_type"},
		"dm_messages":      {"sender_type", "actor_user_id", "client_message_id"},
	} {
		for _, column := range columns {
			var notNull bool
			if err := db.Raw(`SELECT attnotnull FROM pg_attribute WHERE attrelid = ?::regclass AND attname = ?`, table, column).Scan(&notNull).Error; err != nil || !notNull {
				t.Fatalf("%s.%s should be NOT NULL, value=%v err=%v", table, column, notNull, err)
			}
		}
	}
	if err := db.Exec(`INSERT INTO dm_messages (id, conversation_id, sender_type, sender_id, client_message_id, content, created_at, updated_at) VALUES (?, ?, 'user', ?, ?, 'missing actor', NOW(), NOW())`, uuid.New(), conversationID, first, uuid.New()).Error; err == nil {
		t.Fatal("expected actor_user_id NOT NULL constraint")
	}
	if err := db.Exec(`INSERT INTO dm_conversations (id, participant_a_type, participant_a, participant_b_type, participant_b, created_at, updated_at) VALUES (?, 'user', ?, 'user', ?, NOW(), NOW())`, uuid.New(), first, second).Error; err == nil {
		t.Fatal("expected typed conversation unique index")
	}
	if err := db.Exec(`INSERT INTO dm_conversations (id, participant_a_type, participant_a, participant_b_type, participant_b, created_at, updated_at) VALUES (?, 'channel', ?, 'user', ?, NOW(), NOW())`, uuid.New(), first, second).Error; err == nil {
		t.Fatal("expected participant type check constraint")
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
