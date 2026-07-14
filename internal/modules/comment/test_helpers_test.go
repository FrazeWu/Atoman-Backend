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
		&model.CommentTimeAnchor{},
	)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_discussion_target_kind_key ON discussion_targets (kind, resource_key)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_comment_root_floor ON comment_entries (target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`).Error)

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

	return commentTestContext{
		service:  NewService(db, registry),
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
