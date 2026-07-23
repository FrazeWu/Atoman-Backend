package dm

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestConcurrentTargetSendsCreateOneConversationInPostgres(t *testing.T) {
	db := newDMPostgresDB(t)
	actor, recipient := createDMPostgresUser(t, db), createDMPostgresUser(t, db)
	service := NewService(NewRepo(db), nil, nil, nil)

	results := concurrentDMSends(t, func() (MessageDTO, error) {
		return service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: recipient}, SendInput{Content: "hello", ClientMessageID: uuid.New()})
	})
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("concurrent send failed: %v", result.err)
		}
	}
	if results[0].message.ConversationID != results[1].message.ConversationID {
		t.Fatalf("expected one conversation, got %s and %s", results[0].message.ConversationID, results[1].message.ConversationID)
	}
	var count int64
	if err := db.Model(&model.DMConversation{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected one conversation, got %d: %v", count, err)
	}
}

func TestConcurrentIdempotentSendsCreateOneMessageInPostgres(t *testing.T) {
	db := newDMPostgresDB(t)
	actor, recipient, clientID := createDMPostgresUser(t, db), createDMPostgresUser(t, db), uuid.New()
	service := NewService(NewRepo(db), nil, nil, nil)

	results := concurrentDMSends(t, func() (MessageDTO, error) {
		return service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: recipient}, SendInput{Content: "same", ClientMessageID: clientID})
	})
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("concurrent idempotent send failed: %v", result.err)
		}
	}
	if results[0].message.ID != results[1].message.ID {
		t.Fatalf("expected same message, got %s and %s", results[0].message.ID, results[1].message.ID)
	}
	var count int64
	if err := db.Model(&model.DMMessage{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected one message, got %d: %v", count, err)
	}
}

type dmSendResult struct {
	message MessageDTO
	err     error
}

func concurrentDMSends(t *testing.T, send func() (MessageDTO, error)) [2]dmSendResult {
	t.Helper()
	start := make(chan struct{})
	results := make(chan dmSendResult, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			message, err := send()
			results <- dmSendResult{message: message, err: err}
		}()
	}
	close(start)
	group.Wait()
	return [2]dmSendResult{<-results, <-results}
}

func newDMPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_POSTGRES_DSN")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL or TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := adminSQL.Ping(); err != nil {
		t.Fatal(err)
	}
	schema := "dm_concurrency_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	query.Set("options", "-c statement_timeout=6000 -c lock_timeout=4000")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.DMConversation{}, &model.DMMessage{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX uq_dm_conversation_typed ON dm_conversations (participant_a_type, participant_a, participant_b_type, participant_b) WHERE deleted_at IS NULL").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX uq_dm_actor_client_message ON dm_messages (actor_user_id, client_message_id) WHERE deleted_at IS NULL").Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		_ = adminSQL.Close()
	})
	return db
}

func createDMPostgresUser(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	user := model.User{UUID: uuid.New(), Username: uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "test"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return user.UUID
}
