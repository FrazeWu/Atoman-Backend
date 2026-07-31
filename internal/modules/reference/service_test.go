package reference

import (
	"context"
	"fmt"
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSearchManyStopsWhenRequestContextIsCanceled(t *testing.T) {
	service, _, _, _ := seededReferenceService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.SearchMany(ctx, Viewer{}, []string{"post", "thread"}, "", 2)

	require.ErrorIs(t, err, context.Canceled)
}

func TestReplacePublishedPersistsOccurrencesAndNotifiesMentionedUserOnce(t *testing.T) {
	service, db, actor, ids := seededReferenceService(t)
	sourceID := uuid.New()
	content := fmt.Sprintf("@alice @post:%s @alice", ids["post"])

	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := service.ReplacePublished(tx, Source{
			Type: "post", ID: sourceID, ActorID: actor.UUID, Audience: AudiencePublic,
			Meta: model.NotificationMeta{"module": "blog", "path": "/post/" + sourceID.String()},
		}, []Field{{Name: "content", Content: content}})
		return err
	})
	require.NoError(t, err)

	var rows []model.ContentReference
	require.NoError(t, db.Order("start_offset").Find(&rows, "source_type = ? AND source_id = ?", "post", sourceID).Error)
	require.Len(t, rows, 3)
	require.Equal(t, []string{"user", "post", "user"}, []string{rows[0].TargetType, rows[1].TargetType, rows[2].TargetType})

	var notifications []model.Notification
	require.NoError(t, db.Find(&notifications).Error)
	require.Len(t, notifications, 1)
	require.Equal(t, "content_mention", notifications[0].Type)
	require.Equal(t, actor.UUID, *notifications[0].ActorID)
}

func TestReplacePublishedNotifiesOnlyNewMentionTargetsOnEdit(t *testing.T) {
	service, db, actor, _ := seededReferenceService(t)
	bob := model.User{Username: "bob", DisplayName: "Bob", Email: "bob@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&bob).Error)
	source := Source{Type: "thread", ID: uuid.New(), ActorID: actor.UUID, Audience: AudiencePublic}

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := service.ReplacePublished(tx, source, []Field{{Name: "content", Content: "@alice"}})
		return err
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := service.ReplacePublished(tx, source, []Field{{Name: "content", Content: "@alice @bob"}})
		return err
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := service.ReplacePublished(tx, source, []Field{{Name: "content", Content: "@alice @bob"}})
		return err
	}))

	var notifications []model.Notification
	require.NoError(t, db.Order("recipient_id").Find(&notifications).Error)
	require.Len(t, notifications, 2)
}

func TestReplacePublishedRejectsPrivateTargetFromPublicSource(t *testing.T) {
	service, db, actor, _ := seededReferenceService(t)
	private := model.Post{UserID: actor.UUID, Title: "Private", Content: "body", Status: "published", Visibility: "private"}
	require.NoError(t, db.Create(&private).Error)
	source := Source{Type: "post", ID: uuid.New(), ActorID: actor.UUID, Audience: AudiencePublic}

	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := service.ReplacePublished(tx, source, []Field{{Name: "content", Content: "@post:" + private.ID.String()}})
		return err
	})

	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&model.ContentReference{}).Where("source_id = ?", source.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestReplacePublishedLeavesOldReferencesWhenNewContentIsInvalid(t *testing.T) {
	service, db, actor, _ := seededReferenceService(t)
	source := Source{Type: "debate", ID: uuid.New(), ActorID: actor.UUID, Audience: AudiencePublic}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := service.ReplacePublished(tx, source, []Field{{Name: "content", Content: "@alice"}})
		return err
	}))

	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := service.ReplacePublished(tx, source, []Field{{Name: "content", Content: "@post:" + uuid.New().String()}})
		return err
	})
	require.Error(t, err)

	var rows []model.ContentReference
	require.NoError(t, db.Find(&rows, "source_id = ?", source.ID).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "user", rows[0].TargetType)
}

func TestReplacePublishedAndStoredResolutionIdentifySourceField(t *testing.T) {
	service, db, actor, _ := seededReferenceService(t)
	source := Source{Type: "debate", ID: uuid.New(), ActorID: actor.UUID, Audience: AudiencePublic}
	var replaced []ResolvedReference
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		replaced, err = service.ReplacePublished(tx, source, []Field{
			{Name: "description", Content: "Intro @alice"},
			{Name: "content", Content: "Body @alice"},
		})
		return err
	}))
	require.Equal(t, []string{"description", "content"}, []string{replaced[0].Field, replaced[1].Field})

	var rows []model.ContentReference
	require.NoError(t, db.Where("source_id = ?", source.ID).Order("source_field DESC").Find(&rows).Error)
	resolved, err := service.ResolveStoredRows(db, Viewer{}, rows)
	require.NoError(t, err)
	require.Equal(t, []string{"description", "content"}, []string{resolved[source.ID][0].Field, resolved[source.ID][1].Field})
}

func TestBackfillAvailableStoresResolvableTokensWithoutNotifications(t *testing.T) {
	service, db, actor, ids := seededReferenceService(t)
	source := Source{Type: "post", ID: uuid.New(), ActorID: actor.UUID, Audience: AudiencePublic}
	content := fmt.Sprintf("@alice @channel:%s @post:%s unfinished @post:", ids["channel"], uuid.New())

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return service.BackfillAvailable(tx, source, []Field{{Name: "content", Content: content}})
	}))
	var rows []model.ContentReference
	require.NoError(t, db.Order("start_offset ASC").Find(&rows, "source_id = ?", source.ID).Error)
	require.Len(t, rows, 2)
	require.Equal(t, []string{"user", "channel"}, []string{rows[0].TargetType, rows[1].TargetType})
	var notificationCount int64
	require.NoError(t, db.Model(&model.Notification{}).Count(&notificationCount).Error)
	require.Zero(t, notificationCount)
}

func seededReferenceService(t *testing.T) (*Service, *gorm.DB, model.User, map[string]uuid.UUID) {
	t.Helper()
	db, ids := seedReferenceRegistry(t)
	require.NoError(t, db.AutoMigrate(&model.Notification{}, &model.ContentReference{}))
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_notification_dedup ON notifications (recipient_id, source_type, source_id) WHERE aggregation_key = '' AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_content_reference_source_range ON content_references (source_type, source_id, source_field, start_offset, end_offset) WHERE deleted_at IS NULL`).Error)
	actor := model.User{Username: "author", DisplayName: "Author", Email: "author@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&actor).Error)
	return NewService(db), db, actor, ids
}
