package comment

import (
	"fmt"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type commentTestContext struct {
	service  *Service
	db       *gorm.DB
	users    []authctx.CurrentUser
	target   TargetRef
	resolved ResolvedTarget
}

func newCommentTestContext(t *testing.T, kind string, duration int) commentTestContext {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.MediaAsset{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentMention{},
		&model.CommentAttachment{},
		&model.CommentLike{},
		&model.CommentReport{},
		&model.CommentTimeAnchor{},
		&model.CommentPublishRecord{},
		&model.Notification{},
		&model.AuditLog{},
		&model.TimelineRevisionProposal{},
		&model.DebateArgumentDetail{},
		&model.DebateArgumentReference{},
		&model.DebateArgumentDebateRef{},
	)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_discussion_target_kind_key ON discussion_targets (kind, resource_key)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_root_floor ON comment_entries (target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_like_user ON comment_likes (comment_id, user_id) WHERE deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_report_user ON comment_reports (comment_id, reporter_id)`).Error)
	require.NoError(t, db.Exec(`CREATE INDEX IF NOT EXISTS idx_comment_publish_author_created ON comment_publish_records (author_id, created_at)`).Error)
	require.NoError(t, db.Exec(`CREATE INDEX IF NOT EXISTS idx_comment_publish_duplicate_window ON comment_publish_records (author_id, target_id, content_hash, created_at)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_notification_dedup ON notifications (recipient_id, source_type, source_id) WHERE aggregation_key = '' AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_notification_unread_aggregate ON notifications (recipient_id, aggregation_key) WHERE aggregation_key <> '' AND read_at IS NULL AND deleted_at IS NULL`).Error)

	users := make([]authctx.CurrentUser, 4)
	for i := range users {
		stored := model.User{
			Username: fmt.Sprintf("comment-user-%d", i),
			Email:    fmt.Sprintf("comment-user-%d@example.com", i),
			Password: "hash",
			IsActive: true,
		}
		require.NoError(t, db.Create(&stored).Error)
		users[i] = authctx.CurrentUser{ID: stored.UUID, Username: stored.Username, Role: authctx.RoleUser}
	}

	resourceID := uuid.New()
	resolved := ResolvedTarget{
		Kind:        kind,
		ResourceID:  resourceID,
		ResourceKey: resourceID.String(),
		OwnerID:     &users[0].ID,
		Visible:     true,
		DurationSec: duration,
		MarkLabel:   "置顶",
	}
	registry := &TargetRegistry{resolvers: map[string]TargetResolver{
		kind: targetResolverFunc(func(_ Viewer, id uuid.UUID) (ResolvedTarget, error) {
			if id != resourceID {
				return ResolvedTarget{}, ErrTargetNotFound
			}
			return resolved, nil
		}),
	}}

	service := NewService(db, registry)
	service.checkAbuse = false
	return commentTestContext{
		service:  service,
		db:       db,
		users:    users,
		target:   TargetRef{Kind: kind, ResourceID: resourceID},
		resolved: resolved,
	}
}

func (ctx commentTestContext) create(t *testing.T, user int, content string, replyTo *uuid.UUID) CommentDTO {
	t.Helper()
	created, err := ctx.service.Create(ctx.users[user], ctx.target, CreateCommentInput{Content: content, ReplyToID: replyTo})
	require.NoError(t, err)
	return created
}

func createImageAsset(t *testing.T, db *gorm.DB, userID uuid.UUID, contentType string) model.MediaAsset {
	t.Helper()
	asset := model.MediaAsset{
		UserID:      &userID,
		Purpose:     "comment.image",
		URL:         "https://assets.example/" + uuid.NewString(),
		Key:         "comments/" + uuid.NewString(),
		ContentType: contentType,
		Size:        128,
	}
	require.NoError(t, db.Create(&asset).Error)
	return asset
}
