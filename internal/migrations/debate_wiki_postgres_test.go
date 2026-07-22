package migrations

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// preContentReferenceDebateRevisionReference matches the deployed wiki table
// before content_reference_id became the primary reference link.
type preContentReferenceDebateRevisionReference struct {
	model.Base
	DebateID   uuid.UUID  `gorm:"type:uuid;not null;index"`
	RevisionID uuid.UUID  `gorm:"type:uuid;not null;index"`
	Raw        string     `gorm:"type:text;not null"`
	Kind       string     `gorm:"type:varchar(24);not null"`
	ResourceID uuid.UUID  `gorm:"type:uuid;not null"`
	Title      string     `gorm:"type:text;not null"`
	Qualifier  string     `gorm:"type:varchar(16);not null;default:''"`
	Occurrence int        `gorm:"not null"`
	State      string     `gorm:"type:varchar(16);not null;default:'active'"`
	RelationID *uuid.UUID `gorm:"type:uuid;index"`
}

func (preContentReferenceDebateRevisionReference) TableName() string {
	return "debate_revision_references"
}

func TestRunDebateWikiMigrationBackfillsExistingReferencesBeforeMakingLinkRequiredPostgres(t *testing.T) {
	db := openDebateWikiMigrationPostgres(t)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Debate{}, &model.Revision{}, &model.ContentReference{},
		&preContentReferenceDebateRevisionReference{},
	))

	user := model.User{UUID: uuid.New(), Username: "migration-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	debate := model.Debate{UserID: user.UUID, Title: "Target", Status: model.DebateStatusActive}
	require.NoError(t, db.Create(&debate).Error)
	resourceID := uuid.New()
	revision := model.Revision{ContentType: debateRevisionContentType, ContentID: debate.ID, VersionNumber: 1, ContentSnapshot: []byte(`{"content":"@debate:` + resourceID.String() + `:support"}`), EditorID: user.UUID, IsCurrent: true}
	require.NoError(t, db.Create(&revision).Error)
	require.NoError(t, db.Model(&debate).Update("current_revision_id", revision.ID).Error)
	old := preContentReferenceDebateRevisionReference{DebateID: debate.ID, RevisionID: revision.ID, Raw: "@debate:" + resourceID.String() + ":support", Kind: "debate", ResourceID: resourceID, Title: "Source", Qualifier: "support", Occurrence: 1, State: model.DebateRelationActive}
	require.NoError(t, db.Create(&old).Error)

	require.NoError(t, RunDebateWikiMigration(db))

	var migrated model.DebateRevisionReference
	require.NoError(t, db.First(&migrated, "id = ?", old.ID).Error)
	require.NotEqual(t, uuid.Nil, migrated.ContentReferenceID)
	require.True(t, db.Migrator().HasColumn(&model.DebateRevisionReference{}, "content_reference_id"))
}

func openDebateWikiMigrationPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	adminSQL, err := admin.DB()
	require.NoError(t, err)
	require.NoError(t, adminSQL.Ping())
	schema := "debate_wiki_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		_ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		_ = adminSQL.Close()
	})
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	require.NoError(t, err)
	return db
}
