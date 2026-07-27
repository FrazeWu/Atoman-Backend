package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type legacyDMConversation struct {
	ID                 uuid.UUID `gorm:"type:uuid;primaryKey"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          gorm.DeletedAt `gorm:"index"`
	ParticipantA       uuid.UUID      `gorm:"type:uuid;not null"`
	ParticipantB       uuid.UUID      `gorm:"type:uuid;not null"`
	LastMessageAt      *time.Time
	LastMessagePreview string
}

func (legacyDMConversation) TableName() string { return "dm_conversations" }

type legacyDMMessage struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
	ConversationID uuid.UUID      `gorm:"type:uuid;not null"`
	SenderID       uuid.UUID      `gorm:"type:uuid;not null"`
	Content        string
	ImageURL       string
	ReadAt         *time.Time
}

func (legacyDMMessage) TableName() string { return "dm_messages" }

func TestRunDMV2MigrationBackfillsLegacyData(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &legacyDMConversation{}, &legacyDMMessage{}, &model.UserSettings{})

	conversationID, firstUserID, secondUserID, messageID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if firstUserID.String() > secondUserID.String() {
		firstUserID, secondUserID = secondUserID, firstUserID
	}
	if err := db.Create(&legacyDMConversation{ID: conversationID, ParticipantA: firstUserID, ParticipantB: secondUserID}).Error; err != nil {
		t.Fatalf("seed legacy conversation: %v", err)
	}
	if err := db.Create(&legacyDMMessage{ID: messageID, ConversationID: conversationID, SenderID: firstUserID, Content: "legacy"}).Error; err != nil {
		t.Fatalf("seed legacy message: %v", err)
	}
	if err := db.Create(&model.UserSettings{UserID: firstUserID, DMPermission: ""}).Error; err != nil {
		t.Fatalf("seed blank dm permission: %v", err)
	}
	if err := db.Create(&model.UserSettings{UserID: secondUserID, DMPermission: model.DMPermissionAnyone}).Error; err != nil {
		t.Fatalf("seed explicit dm permission: %v", err)
	}

	if err := RunDMV2Migration(db); err != nil {
		t.Fatalf("run dm v2 migration: %v", err)
	}

	var conversation model.DMConversation
	if err := db.First(&conversation, "id = ?", conversationID).Error; err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	if conversation.ParticipantAType != model.DMPartyUser || conversation.ParticipantBType != model.DMPartyUser {
		t.Fatalf("conversation types = %q/%q", conversation.ParticipantAType, conversation.ParticipantBType)
	}
	var message model.DMMessage
	if err := db.First(&message, "id = ?", messageID).Error; err != nil {
		t.Fatalf("load message: %v", err)
	}
	if message.SenderType != model.DMPartyUser || message.ActorUserID != firstUserID || message.ClientMessageID != messageID {
		t.Fatalf("message backfill = %#v", message)
	}
	var blank, explicit model.UserSettings
	if err := db.First(&blank, "user_id = ?", firstUserID).Error; err != nil {
		t.Fatalf("load blank permission: %v", err)
	}
	if err := db.First(&explicit, "user_id = ?", secondUserID).Error; err != nil {
		t.Fatalf("load explicit permission: %v", err)
	}
	if blank.DMPermission != model.DMPermissionOneBeforeReply || explicit.DMPermission != model.DMPermissionAnyone {
		t.Fatalf("permissions = %q/%q", blank.DMPermission, explicit.DMPermission)
	}
}

func TestRunDMV2MigrationCreatesFreshSchemaAndIsIdempotent(t *testing.T) {
	db := testdb.Open(t)
	if err := RunDMV2Migration(db); err != nil {
		t.Fatalf("first dm v2 migration: %v", err)
	}
	if err := RunDMV2Migration(db); err != nil {
		t.Fatalf("second dm v2 migration: %v", err)
	}
	for _, schemaModel := range []any{
		&model.DMConversation{}, &model.DMMessage{}, &model.DMImage{}, &model.DMChannelSettings{}, &model.DMMessageReport{},
	} {
		if !db.Migrator().HasTable(schemaModel) {
			t.Fatalf("expected table for %T", schemaModel)
		}
	}
	if !db.Migrator().HasIndex("dm_conversations", "uq_dm_conversation_typed") {
		t.Fatal("expected typed conversation index")
	}
	if !db.Migrator().HasIndex("dm_messages", "uq_dm_actor_client_message") {
		t.Fatal("expected actor/client index")
	}
}

func TestRunDMV2MigrationBackfillsZeroUUIDMessageFields(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.DMConversation{}, &model.DMMessage{})

	conversationID, senderID, messageID := uuid.New(), uuid.New(), uuid.New()
	if err := db.Create(&model.DMMessage{
		Base:            model.Base{ID: messageID},
		ConversationID:  conversationID,
		SenderType:      model.DMPartyUser,
		SenderID:        senderID,
		ActorUserID:     uuid.Nil,
		ClientMessageID: uuid.Nil,
		Content:         "zero ids",
	}).Error; err != nil {
		t.Fatalf("seed zero uuid message: %v", err)
	}

	if err := RunDMV2Migration(db); err != nil {
		t.Fatalf("first dm v2 migration: %v", err)
	}
	if err := RunDMV2Migration(db); err != nil {
		t.Fatalf("second dm v2 migration: %v", err)
	}

	var message model.DMMessage
	if err := db.First(&message, "id = ?", messageID).Error; err != nil {
		t.Fatalf("load migrated message: %v", err)
	}
	if message.ActorUserID != senderID || message.ClientMessageID != messageID {
		t.Fatalf("zero uuid backfill = actor %s client %s", message.ActorUserID, message.ClientMessageID)
	}
	if !db.Migrator().HasIndex("dm_messages", "uq_dm_actor_client_message") {
		t.Fatal("expected actor/client unique index")
	}
}
