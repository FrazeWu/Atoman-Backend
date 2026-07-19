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
	testdb.Migrate(t, db, &model.User{}, &model.Debate{}, &model.DiscussionTarget{}, &model.CommentEntry{}, &model.DebateArgumentDetail{}, &model.DebateVote{}, &model.VoteHistory{})
	user := model.User{Username: "voter", Email: "voter@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	debate := model.Debate{UserID: user.UUID, Title: "topic", Status: "open"}
	require.NoError(t, db.Create(&debate).Error)
	target := model.DiscussionTarget{Kind: "debate", ResourceID: debate.ID, ResourceKey: debate.ID.String(), NextFloor: 1}
	require.NoError(t, db.Create(&target).Error)
	commentID := uuid.New()
	require.NoError(t, db.Create(&model.CommentEntry{Base: model.Base{ID: commentID}, TargetID: target.ID, AuthorID: user.UUID, Content: "claim", ContentHash: "hash", Status: "active"}).Error)
	require.NoError(t, db.Create(&model.DebateArgumentDetail{CommentID: commentID, ArgumentType: "support"}).Error)

	vote, err := NewService(db).SetArgumentVote(authctx.CurrentUser{ID: user.UUID, Role: user.Role}, commentID, 1)
	require.NoError(t, err)
	require.Equal(t, commentID, vote.ArgumentID)
}

func TestArgumentVoteRejectsHiddenAndConcludedArguments(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Debate{}, &model.DiscussionTarget{}, &model.CommentEntry{}, &model.DebateArgumentDetail{}, &model.DebateVote{}, &model.VoteHistory{})
	user := model.User{Username: "voter", Email: "voter2@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	debate := model.Debate{UserID: user.UUID, Title: "topic", Status: "open"}
	require.NoError(t, db.Create(&debate).Error)
	target := model.DiscussionTarget{Kind: "debate", ResourceID: debate.ID, ResourceKey: debate.ID.String(), NextFloor: 1}
	require.NoError(t, db.Create(&target).Error)
	entry := model.CommentEntry{TargetID: target.ID, AuthorID: user.UUID, Content: "claim", ContentHash: "hash", Status: "moderator_hidden"}
	require.NoError(t, db.Create(&entry).Error)
	require.NoError(t, db.Create(&model.DebateArgumentDetail{CommentID: entry.ID, ArgumentType: "support"}).Error)
	_, err := NewService(db).SetArgumentVote(authctx.CurrentUser{ID: user.UUID, Role: user.Role}, entry.ID, 1)
	require.Error(t, err)
	require.NoError(t, db.Model(&entry).Update("status", "active").Error)
	require.NoError(t, db.Model(&debate).Update("status", "concluded").Error)
	_, err = NewService(db).SetArgumentVote(authctx.CurrentUser{ID: user.UUID, Role: user.Role}, entry.ID, 1)
	require.Error(t, err)
}

func TestConclusionVoteThresholdDoesNotInventAConclusionDirection(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Debate{}, &model.DebateConcludeVote{})
	user := model.User{Username: "conclusion-voter", Email: "conclusion@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	debate := model.Debate{
		UserID: user.UUID, Title: "是否形成结论？", Status: "open", ConcludeThreshold: 1,
	}
	require.NoError(t, db.Create(&debate).Error)

	state, err := NewService(db).SetConclusionVote(authctx.CurrentUser{ID: user.UUID, Role: user.Role}, debate.ID)
	require.NoError(t, err)
	require.Equal(t, 1, state.ConcludeVoteCount)
	require.False(t, state.AutoConcluded)

	var stored model.Debate
	require.NoError(t, db.First(&stored, "id = ?", debate.ID).Error)
	require.Equal(t, "open", stored.Status)
	require.Empty(t, stored.ConclusionType)
}
