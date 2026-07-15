package debate_voting

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestArgumentVoteUsesTypedCommentID(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.CommentEntry{}, &model.DebateArgumentDetail{}, &model.DebateVote{}, &model.VoteHistory{})
	user := model.User{Username: "voter", Email: "voter@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	commentID := uuid.New()
	require.NoError(t, db.Create(&model.CommentEntry{Base: model.Base{ID: commentID}, TargetID: uuid.New(), AuthorID: user.UUID, Content: "claim", ContentHash: "hash", Status: "active"}).Error)
	require.NoError(t, db.Create(&model.DebateArgumentDetail{CommentID: commentID, ArgumentType: "support"}).Error)

	vote, err := NewService(db).SetArgumentVote(authctx.CurrentUser{ID: user.UUID, Role: user.Role}, commentID, 1)
	require.NoError(t, err)
	require.Equal(t, commentID, vote.ArgumentID)
}
