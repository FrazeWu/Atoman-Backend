package debate

import (
	"encoding/json"
	"net/http"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func seedChallengeVoters(t *testing.T, ctx debateTestContext, count int) []authctx.CurrentUser {
	t.Helper()
	voters := make([]authctx.CurrentUser, 0, count)
	for i := 0; i < count; i++ {
		id := uuid.New()
		name := "voter-" + id.String()[:8]
		u := model.User{
			UUID:     id,
			Username: name,
			Email:    name + "@example.com",
			Password: "hash",
			Role:     authctx.RoleUser,
			IsActive: true,
		}
		require.NoError(t, ctx.db.Create(&u).Error)
		voters = append(voters, authctx.CurrentUser{ID: u.UUID, Username: u.Username, Role: u.Role})
	}
	return voters
}

func makeConcludedSourceDebate(t *testing.T, ctx debateTestContext, title string) DebateDTO {
	t.Helper()
	src := createDebateForTest(t, ctx, title, "Source body")
	ev := model.DebateConclusionEvent{
		DebateID:   src.ID,
		Direction:  model.DebateVoteYes,
		YesVotes:   5,
		NoVotes:    0,
		TotalVotes: 5,
	}
	require.NoError(t, ctx.db.Create(&ev).Error)
	require.NoError(t, ctx.db.Model(&model.Debate{}).Where("id = ?", src.ID).Updates(map[string]any{
		"conclusion_type":             model.DebateVoteYes,
		"current_conclusion_event_id": ev.ID,
	}).Error)
	return src
}

func TestChallengeRelationUpholdExitsGraphAndHandlesStale(t *testing.T) {
	ctx := newStructuredDebateContext(t)
	voters := seedChallengeVoters(t, ctx, 6)
	source := makeConcludedSourceDebate(t, ctx, "Concluded Source")
	target := createDebateForTest(t, ctx, "Target Topic", "Based on @debate:"+source.ID.String()+":support")

	require.Len(t, target.References, 1)
	require.NotNil(t, target.References[0].Relation)
	relID := target.References[0].Relation.ID

	// Relation references must use target_type = "relation", not "reference".
	var refRow model.ContentReference
	require.NoError(t, ctx.db.First(&refRow, "source_type = ? AND source_id = ?", "debate_revision", *target.CurrentRevisionID).Error)
	_, err := ctx.service.CreateChallenge(voters[0], target.ID, CreateChallengeRequest{
		TargetType: model.DebateChallengeTargetReference,
		TargetID:   refRow.ID,
		ReasonCode: model.DebateChallengeReasonMismatch,
		Rationale:  "Wrong target type",
	})
	requireAppError(t, err, "debate.challenge_invalid", 400)

	// Open challenge on active relation + verify duplicate open fails.
	ch, err := ctx.service.CreateChallenge(voters[0], target.ID, CreateChallengeRequest{
		TargetType: model.DebateChallengeTargetRelation,
		TargetID:   relID,
		ReasonCode: model.DebateChallengeReasonIrrelevant,
		Rationale:  "Conclusion does not logically transfer",
	})
	require.NoError(t, err)
	require.Equal(t, model.DebateChallengeStatusOpen, ch.Status)

	_, err = ctx.service.CreateChallenge(voters[1], target.ID, CreateChallengeRequest{
		TargetType: model.DebateChallengeTargetRelation,
		TargetID:   relID,
		ReasonCode: model.DebateChallengeReasonMismatch,
		Rationale:  "Duplicate challenge",
	})
	requireAppError(t, err, "debate.challenge_already_open", 409)

	// 4 uphold + 1 dismiss = 5 votes (80% > 75% default) -> upheld.
	for i := 0; i < 4; i++ {
		ch, err = ctx.service.VoteChallenge(voters[i], target.ID, ch.ID, model.DebateChallengeVoteUphold)
		require.NoError(t, err)
		require.Equal(t, model.DebateChallengeStatusOpen, ch.Status)
	}
	ch, err = ctx.service.VoteChallenge(voters[4], target.ID, ch.ID, model.DebateChallengeVoteDismiss)
	require.NoError(t, err)
	require.Equal(t, model.DebateChallengeStatusUpheld, ch.Status)
	require.Equal(t, 4, ch.UpholdVotes)
	require.Equal(t, 1, ch.DismissVotes)

	// Relation & revision reference become rejected, leaving DAG empty.
	var rel model.DebateRelation
	require.NoError(t, ctx.db.First(&rel, "id = ?", relID).Error)
	require.Equal(t, model.DebateRelationRejected, rel.Status)

	graph, err := ctx.service.GetDebateGraph(target.ID, "graph", 1)
	require.NoError(t, err)
	require.Empty(t, graph.Relations)

	// Stale relations can also be challenged.
	source2 := makeConcludedSourceDebate(t, ctx, "Stale Source")
	target2 := createDebateForTest(t, ctx, "Target Two", "Supports @debate:"+source2.ID.String()+":support")
	rel2ID := target2.References[0].Relation.ID
	require.NoError(t, ctx.db.Model(&model.DebateRelation{}).Where("id = ?", rel2ID).Update("status", model.DebateRelationStale).Error)
	ch2, err := ctx.service.CreateChallenge(voters[5], target2.ID, CreateChallengeRequest{
		TargetType: model.DebateChallengeTargetRelation,
		TargetID:   rel2ID,
		ReasonCode: model.DebateChallengeReasonOutdated,
		Rationale:  "Stale relation no longer holds",
	})
	require.NoError(t, err)
	require.Equal(t, model.DebateChallengeStatusOpen, ch2.Status)
}

func TestChallengeNonRelationReferenceSurvivesWikiWhitespaceEdit(t *testing.T) {
	ctx := newStructuredDebateContext(t)
	voters := seedChallengeVoters(t, ctx, 6)
	targetPerson := seedReferencePerson(t, ctx, "Disputed Person")
	counterPerson := seedReferencePerson(t, ctx, "Counter Reference")

	topic := createDebateForTest(t, ctx, "Citation Check", "Claim relies on @person:"+targetPerson.ID.String())
	var refRow model.ContentReference
	require.NoError(t, ctx.db.First(&refRow, "source_type = ? AND source_id = ?", "debate_revision", *topic.CurrentRevisionID).Error)

	ch, err := ctx.service.CreateChallenge(voters[0], topic.ID, CreateChallengeRequest{
		TargetType: model.DebateChallengeTargetReference,
		TargetID:   refRow.ID,
		ReasonCode: model.DebateChallengeReasonLowReliability,
		Rationale:  "Source lacks primary verification; see @person:" + counterPerson.ID.String(),
	})
	require.NoError(t, err)
	require.Len(t, ch.References, 1)

	for i := 0; i < 5; i++ {
		ch, err = ctx.service.VoteChallenge(voters[i], topic.ID, ch.ID, model.DebateChallengeVoteUphold)
		require.NoError(t, err)
	}
	require.Equal(t, model.DebateChallengeStatusUpheld, ch.Status)

	detail, err := ctx.service.GetDebate(topic.ID)
	require.NoError(t, err)
	require.Len(t, detail.References, 1)
	require.NotNil(t, detail.References[0].Challenge)
	require.Equal(t, model.DebateChallengeStatusUpheld, detail.References[0].Challenge.Status)

	// Whitespace Wiki edit creates a new ContentReference row ID, but the same target stays rejected.
	edited, err := ctx.service.SaveWiki(ctx.owner, topic.ID, SaveWikiRequest{
		Title:          topic.Title,
		Description:    topic.Description,
		Content:        "Claim relies on   @person:" + targetPerson.ID.String() + "  ",
		Tags:           topic.Tags,
		EditSummary:    "whitespace edit",
		BaseRevisionID: *topic.CurrentRevisionID,
	})
	require.NoError(t, err)
	require.Len(t, edited.References, 1)
	require.NotNil(t, edited.References[0].Challenge)
	require.Equal(t, model.DebateChallengeStatusUpheld, edited.References[0].Challenge.Status)

	var newRefRow model.ContentReference
	require.NoError(t, ctx.db.First(&newRefRow, "source_type = ? AND source_id = ?", "debate_revision", *edited.CurrentRevisionID).Error)
	require.NotEqual(t, refRow.ID, newRefRow.ID)
	_, err = ctx.service.CreateChallenge(voters[0], topic.ID, CreateChallengeRequest{
		TargetType: model.DebateChallengeTargetReference,
		TargetID:   newRefRow.ID,
		ReasonCode: model.DebateChallengeReasonMismatch,
		Rationale:  "Re-challenge after whitespace edit",
	})
	requireAppError(t, err, "debate.challenge_invalid", 400)
}

func TestChallengeEvidenceDismissUpholdWithdrawVoteAndCustomThreshold(t *testing.T) {
	ctx := newStructuredDebateContext(t)
	voters := seedChallengeVoters(t, ctx, 7)
	person := seedReferencePerson(t, ctx, "Primary Author")
	topic := createDebateForTest(t, ctx, "Evidence Challenge", "Body")

	ev, err := ctx.service.CreateEntry(ctx.owner, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeEvidence,
		Stance:    model.DebateEntryStanceSupport,
		Content:   "Verified @person:" + person.ID.String(),
	})
	require.NoError(t, err)

	// Configure custom threshold: challenge_min_votes = 6, threshold = 80%.
	sa := service.NewSiteAccessService(ctx.db)
	matrix, err := sa.Load()
	require.NoError(t, err)
	input := matrix.ToInput()
	minVotes := 6
	pct := 80
	input.Settings.Debate = &service.SiteAccessDebateSettingsInput{
		ChallengeMinVotes:          &minVotes,
		ConclusionThresholdPercent: &pct,
	}
	require.NoError(t, sa.SaveInput(input))

	ch1, err := ctx.service.CreateChallenge(voters[0], topic.ID, CreateChallengeRequest{
		TargetType: model.DebateChallengeTargetEntry,
		TargetID:   ev.ID,
		ReasonCode: model.DebateChallengeReasonOutdated,
		Rationale:  "First challenge",
	})
	require.NoError(t, err)
	require.Equal(t, 6, ch1.MinVotes)
	require.Equal(t, 80, ch1.ThresholdPercent)

	// Vote and withdraw vote before resolution.
	ch1, err = ctx.service.VoteChallenge(voters[0], topic.ID, ch1.ID, model.DebateChallengeVoteUphold)
	require.NoError(t, err)
	require.Equal(t, model.DebateChallengeVoteUphold, ch1.CurrentUserVote)
	ch1, err = ctx.service.DeleteChallengeVote(voters[0], topic.ID, ch1.ID)
	require.NoError(t, err)
	require.Empty(t, ch1.CurrentUserVote)
	require.Equal(t, 0, ch1.UpholdVotes)

	// Dismiss ch1 cleanly with 6 dismiss votes.
	for i := 0; i < 6; i++ {
		ch1, err = ctx.service.VoteChallenge(voters[i], topic.ID, ch1.ID, model.DebateChallengeVoteDismiss)
		require.NoError(t, err)
	}
	require.Equal(t, model.DebateChallengeStatusDismissed, ch1.Status)

	// Evidence remains active; a second challenge can now open and uphold to reject the evidence.
	ch2, err := ctx.service.CreateChallenge(voters[0], topic.ID, CreateChallengeRequest{
		TargetType: model.DebateChallengeTargetEntry,
		TargetID:   ev.ID,
		ReasonCode: model.DebateChallengeReasonMismatch,
		Rationale:  "Second challenge with stronger proof",
	})
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		ch2, err = ctx.service.VoteChallenge(voters[i], topic.ID, ch2.ID, model.DebateChallengeVoteUphold)
		require.NoError(t, err)
		require.Equal(t, model.DebateChallengeStatusOpen, ch2.Status)
	}
	// 5/6 = 83.3% > 80%? With 1 dismiss, 5 uphold + 1 dismiss = 6 votes (83.3% > 80%) -> upheld!
	ch2, err = ctx.service.VoteChallenge(voters[5], topic.ID, ch2.ID, model.DebateChallengeVoteDismiss)
	require.NoError(t, err)
	require.Equal(t, model.DebateChallengeStatusUpheld, ch2.Status)

	// Evidence becomes rejected, stats reflect ActiveEvidenceCount = 0, and closed challenge rejects votes.
	entries, err := ctx.service.ListEntries(topic.ID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, model.DebateEntryStatusRejected, entries[0].Status)
	require.NotNil(t, entries[0].RejectedChallengeID)
	require.Equal(t, ch2.ID, *entries[0].RejectedChallengeID)

	detail, err := ctx.service.GetDebate(topic.ID)
	require.NoError(t, err)
	require.Equal(t, 0, detail.Stats.ActiveEvidenceCount)

	_, err = ctx.service.VoteChallenge(voters[6], topic.ID, ch2.ID, model.DebateChallengeVoteDismiss)
	requireAppError(t, err, "debate.challenge_closed", 409)
}

func TestChallengesHTTPFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := newStructuredDebateContext(t)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		switch c.GetHeader("X-Test-User") {
		case "owner":
			authctx.SetCurrentUser(c, ctx.owner)
		case "editor":
			authctx.SetCurrentUser(c, ctx.editor)
		}
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1"), ctx.service)

	topic := createDebateForTest(t, ctx, "HTTP Challenge", "Body")
	person := seedReferencePerson(t, ctx, "Reference Person")
	ev, err := ctx.service.CreateEntry(ctx.owner, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeEvidence,
		Stance:    model.DebateEntryStanceSupport,
		Content:   "Citation @person:" + person.ID.String(),
	})
	require.NoError(t, err)

	createRes := performDebateRequest(t, router, http.MethodPost, "/api/v1/debate/topics/"+topic.ID.String()+"/challenges", map[string]any{
		"target_type": "entry",
		"target_id":   ev.ID,
		"reason_code": "mismatch",
		"rationale":   "HTTP rationale",
	}, "editor")
	require.Equal(t, http.StatusCreated, createRes.Code, createRes.Body.String())

	var createdPayload struct {
		Data DebateChallengeDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(createRes.Body.Bytes(), &createdPayload))
	chID := createdPayload.Data.ID

	voteRes := performDebateRequest(t, router, http.MethodPut, "/api/v1/debate/topics/"+topic.ID.String()+"/challenges/"+chID.String()+"/vote", map[string]any{
		"decision": "uphold",
	}, "owner")
	require.Equal(t, http.StatusOK, voteRes.Code, voteRes.Body.String())

	delVoteRes := performDebateRequest(t, router, http.MethodDelete, "/api/v1/debate/topics/"+topic.ID.String()+"/challenges/"+chID.String()+"/vote", nil, "owner")
	require.Equal(t, http.StatusOK, delVoteRes.Code, delVoteRes.Body.String())

	listRes := performDebateRequest(t, router, http.MethodGet, "/api/v1/debate/topics/"+topic.ID.String()+"/challenges?status=open", nil, "editor")
	require.Equal(t, http.StatusOK, listRes.Code, listRes.Body.String())
	assertDebateDataEnvelope(t, listRes)
}
