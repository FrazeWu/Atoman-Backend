package debate_voting

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

type votingTestContext struct {
	db      *gorm.DB
	service *Service
	debate  model.Debate
	users   []authctx.CurrentUser
}

func newVotingTestContext(t *testing.T, userCount int) votingTestContext {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Debate{}, &model.DebateVote{}, &model.DebateConclusionEvent{}, &model.DebateRelation{})
	users := make([]model.User, 0, userCount+1)
	owner := model.User{UUID: uuid.New(), Username: "owner", Email: "owner@example.com", Password: "hash", IsActive: true}
	users = append(users, owner)
	for i := 0; i < userCount; i++ {
		users = append(users, model.User{UUID: uuid.New(), Username: fmt.Sprintf("voter-%d", i), Email: fmt.Sprintf("voter-%d@example.com", i), Password: "hash", IsActive: true})
	}
	require.NoError(t, db.Create(&users).Error)
	debate := model.Debate{UserID: owner.UUID, Title: "Vote", Status: model.DebateStatusActive}
	require.NoError(t, db.Create(&debate).Error)
	currentUsers := make([]authctx.CurrentUser, 0, userCount)
	for _, user := range users[1:] {
		currentUsers = append(currentUsers, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser})
	}
	return votingTestContext{db: db, service: NewService(db), debate: debate, users: currentUsers}
}

func TestConclusionThresholdTable(t *testing.T) {
	tests := []struct {
		yes, no int
		want    string
	}{
		{yes: 7, no: 2, want: ""},
		{yes: 7, no: 3, want: ""},
		{yes: 8, no: 2, want: model.DebateVoteYes},
		{yes: 9, no: 3, want: ""},
		{yes: 10, no: 2, want: model.DebateVoteYes},
		{yes: 2, no: 8, want: model.DebateVoteNo},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%d_yes_%d_no", test.yes, test.no), func(t *testing.T) {
			ctx := newVotingTestContext(t, test.yes+test.no)
			for index, user := range ctx.users {
				direction := model.DebateVoteYes
				if index < test.no {
					direction = model.DebateVoteNo
				}
				_, err := ctx.service.SetVote(user, ctx.debate.ID, direction)
				require.NoError(t, err)
			}
			stats, err := ctx.service.GetVotes(authctx.CurrentUser{}, ctx.debate.ID)
			require.NoError(t, err)
			require.Equal(t, test.yes, stats.YesVotes)
			require.Equal(t, test.no, stats.NoVotes)
			require.Equal(t, test.want, stats.CurrentDirection)
		})
	}
}

func TestChangingVoteReversesConclusionAndStalesOldEventRelations(t *testing.T) {
	ctx := newVotingTestContext(t, 10)
	for index, user := range ctx.users {
		direction := model.DebateVoteYes
		if index >= 8 {
			direction = model.DebateVoteNo
		}
		_, err := ctx.service.SetVote(user, ctx.debate.ID, direction)
		require.NoError(t, err)
	}
	var initial model.Debate
	require.NoError(t, ctx.db.First(&initial, "id = ?", ctx.debate.ID).Error)
	require.Equal(t, model.DebateVoteYes, initial.ConclusionType)
	require.NotNil(t, initial.CurrentConclusionEventID)
	target := model.Debate{UserID: ctx.users[0].ID, Title: "Target", Status: model.DebateStatusActive}
	require.NoError(t, ctx.db.Create(&target).Error)
	relation := model.DebateRelation{SourceDebateID: ctx.debate.ID, TargetDebateID: target.ID, Stance: model.DebateRelationSupport, TargetRevisionID: uuid.New(), SourceConclusionEventID: *initial.CurrentConclusionEventID, Status: model.DebateRelationActive}
	require.NoError(t, ctx.db.Create(&relation).Error)

	for index := 0; index < 6; index++ {
		_, err := ctx.service.SetVote(ctx.users[index], ctx.debate.ID, model.DebateVoteNo)
		require.NoError(t, err)
	}
	var reversed model.Debate
	require.NoError(t, ctx.db.First(&reversed, "id = ?", ctx.debate.ID).Error)
	require.Equal(t, model.DebateVoteNo, reversed.ConclusionType)
	require.NotEqual(t, initial.CurrentConclusionEventID, reversed.CurrentConclusionEventID)
	var storedRelation model.DebateRelation
	require.NoError(t, ctx.db.First(&storedRelation, "id = ?", relation.ID).Error)
	require.Equal(t, model.DebateRelationStale, storedRelation.Status)
	var eventCount int64
	require.NoError(t, ctx.db.Model(&model.DebateConclusionEvent{}).Where("debate_id = ?", ctx.debate.ID).Count(&eventCount).Error)
	require.EqualValues(t, 2, eventCount)
}

func TestDeletingVoteDoesNotClearExistingConclusion(t *testing.T) {
	ctx := newVotingTestContext(t, 10)
	for index, user := range ctx.users {
		direction := model.DebateVoteNo
		if index < 8 {
			direction = model.DebateVoteYes
		}
		_, err := ctx.service.SetVote(user, ctx.debate.ID, direction)
		require.NoError(t, err)
	}
	stats, err := ctx.service.DeleteVote(ctx.users[0], ctx.debate.ID)
	require.NoError(t, err)
	require.Equal(t, 7, stats.YesVotes)
	require.Equal(t, 2, stats.NoVotes)
	require.Equal(t, model.DebateVoteYes, stats.CurrentDirection)
	require.Empty(t, stats.CurrentUserVote)
}

func TestVoteCanBeChangedAndRemovedAtAnyDebateStatus(t *testing.T) {
	ctx := newVotingTestContext(t, 1)
	require.NoError(t, ctx.db.Model(&model.Debate{}).Where("id = ?", ctx.debate.ID).Update("status", model.DebateStatusArchived).Error)
	stats, err := ctx.service.SetVote(ctx.users[0], ctx.debate.ID, model.DebateVoteYes)
	require.NoError(t, err)
	require.Equal(t, model.DebateVoteYes, stats.CurrentUserVote)
	stats, err = ctx.service.SetVote(ctx.users[0], ctx.debate.ID, model.DebateVoteNo)
	require.NoError(t, err)
	require.Equal(t, model.DebateVoteNo, stats.CurrentUserVote)
	_, err = ctx.service.DeleteVote(ctx.users[0], ctx.debate.ID)
	require.NoError(t, err)
}

func TestListConclusionsReturnsImmutableEventsNewestFirst(t *testing.T) {
	ctx := newVotingTestContext(t, 1)
	first := model.DebateConclusionEvent{DebateID: ctx.debate.ID, Direction: model.DebateVoteYes, YesVotes: 8, NoVotes: 2, TotalVotes: 10}
	second := model.DebateConclusionEvent{DebateID: ctx.debate.ID, Direction: model.DebateVoteNo, YesVotes: 2, NoVotes: 8, TotalVotes: 10}
	require.NoError(t, ctx.db.Create(&first).Error)
	require.NoError(t, ctx.db.Create(&second).Error)
	events, err := ctx.service.ListConclusions(ctx.debate.ID)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{second.ID, first.ID}, []uuid.UUID{events[0].ID, events[1].ID})
}
