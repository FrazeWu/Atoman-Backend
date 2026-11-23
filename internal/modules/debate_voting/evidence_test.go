package debate_voting

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func intPtrTest(v int) *int { return &v }

func TestEvidenceVotingIsIndependentAndDrivesWeakFlag(t *testing.T) {
	ctx := newVotingTestContext(t, 6)
	require.NoError(t, ctx.db.AutoMigrate(&model.DebateEvidenceVote{}, &model.DebateEntry{}, &model.SiteSetting{}))

	// Zero active evidence marks evidence_weak = true, even before votes arrive.
	stats, err := ctx.service.GetVotes(ctx.users[0], ctx.debate.ID)
	require.NoError(t, err)
	require.Equal(t, 0, stats.ActiveEvidenceCount)
	require.True(t, stats.EvidenceWeak)
	require.Equal(t, service.DefaultDebateConclusionMinVotes, stats.ConclusionMinVotes)
	require.Equal(t, service.DefaultDebateConclusionThresholdPct, stats.ConclusionThresholdPercent)

	// Add 1 active evidence entry -> active_evidence_count=1 clears evidence_weak unless insufficient > 50%.
	entry := model.DebateEntry{
		DebateID:  ctx.debate.ID,
		UserID:    ctx.users[0].ID,
		EntryType: model.DebateEntryTypeEvidence,
		Stance:    model.DebateEntryStanceSupport,
		Content:   "Verified citation",
		Status:    model.DebateEntryStatusActive,
	}
	require.NoError(t, ctx.db.Create(&entry).Error)

	// Independent evidence vote without any stance vote.
	stats, err = ctx.service.SetEvidenceVote(ctx.users[0], ctx.debate.ID, model.DebateEvidenceRatingSufficient)
	require.NoError(t, err)
	require.Equal(t, 1, stats.EvidenceSufficient)
	require.Equal(t, 1, stats.EvidenceTotal)
	require.Equal(t, model.DebateEvidenceRatingSufficient, stats.CurrentUserEvidenceVote)
	require.Empty(t, stats.CurrentUserVote)
	require.Equal(t, 1, stats.ActiveEvidenceCount)
	require.False(t, stats.EvidenceWeak)

	// Two insufficient vs one sufficient => insufficient * 2 > total => evidence_weak = true.
	_, err = ctx.service.SetEvidenceVote(ctx.users[1], ctx.debate.ID, model.DebateEvidenceRatingInsufficient)
	require.NoError(t, err)
	stats, err = ctx.service.SetEvidenceVote(ctx.users[2], ctx.debate.ID, model.DebateEvidenceRatingInsufficient)
	require.NoError(t, err)
	require.Equal(t, 1, stats.EvidenceSufficient)
	require.Equal(t, 2, stats.EvidenceInsufficient)
	require.Equal(t, 3, stats.EvidenceTotal)
	require.True(t, stats.EvidenceWeak)

	// Evidence insufficiency is a soft signal: 5 yes votes still form a conclusion and snapshot evidence stats.
	for i := 0; i < 5; i++ {
		stats, err = ctx.service.SetVote(ctx.users[i], ctx.debate.ID, model.DebateVoteYes)
		require.NoError(t, err)
	}
	require.Equal(t, model.DebateVoteYes, stats.CurrentDirection)

	events, err := ctx.service.ListConclusions(ctx.debate.ID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, model.DebateVoteYes, events[0].Direction)
	require.Equal(t, 1, events[0].EvidenceSufficient)
	require.Equal(t, 2, events[0].EvidenceInsufficient)
	require.Equal(t, 1, events[0].ActiveEvidenceCount)
	require.True(t, events[0].EvidenceWeak)

	// Deleting one insufficient vote flips insufficient (1) <= total/2 (2/2=1) -> evidence_weak becomes false.
	stats, err = ctx.service.DeleteEvidenceVote(ctx.users[2], ctx.debate.ID)
	require.NoError(t, err)
	require.Equal(t, 1, stats.EvidenceInsufficient)
	require.Equal(t, 2, stats.EvidenceTotal)
	require.False(t, stats.EvidenceWeak)
}

func TestConclusionRespectsCustomSiteAccessSettings(t *testing.T) {
	ctx := newVotingTestContext(t, 12)
	require.NoError(t, ctx.db.AutoMigrate(&model.DebateEvidenceVote{}, &model.DebateEntry{}, &model.SiteSetting{}))

	sa := service.NewSiteAccessService(ctx.db)
	matrix, err := sa.Load()
	require.NoError(t, err)
	input := matrix.ToInput()
	input.Settings.Debate = &service.SiteAccessDebateSettingsInput{
		ConclusionMinVotes:         intPtrTest(8),
		ConclusionThresholdPercent: intPtrTest(80),
		ChallengeMinVotes:          intPtrTest(6),
	}
	require.NoError(t, sa.SaveInput(input))

	// 6 yes (< min_votes=8), then 2 no -> 6/8 (75%), 7/9 (77.8%), 8/10 (80.0%, must be strictly > 80%).
	for i := 0; i < 6; i++ {
		stats, err := ctx.service.SetVote(ctx.users[i], ctx.debate.ID, model.DebateVoteYes)
		require.NoError(t, err)
		require.Empty(t, stats.CurrentDirection)
		require.Equal(t, 8, stats.ConclusionMinVotes)
		require.Equal(t, 80, stats.ConclusionThresholdPercent)
	}
	for i := 6; i < 8; i++ {
		stats, err := ctx.service.SetVote(ctx.users[i], ctx.debate.ID, model.DebateVoteNo)
		require.NoError(t, err)
		require.Empty(t, stats.CurrentDirection)
	}
	for i := 8; i < 10; i++ {
		stats, err := ctx.service.SetVote(ctx.users[i], ctx.debate.ID, model.DebateVoteYes)
		require.NoError(t, err)
		require.Empty(t, stats.CurrentDirection)
	}

	// 9 yes vs 2 no = 81.8% > 80% -> reaches conclusion.
	stats, err := ctx.service.SetVote(ctx.users[10], ctx.debate.ID, model.DebateVoteYes)
	require.NoError(t, err)
	require.Equal(t, model.DebateVoteYes, stats.CurrentDirection)
}

func TestEvidenceVoteHTTPPutAndDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := newVotingTestContext(t, 2)
	require.NoError(t, ctx.db.AutoMigrate(&model.DebateEvidenceVote{}, &model.DebateEntry{}, &model.SiteSetting{}))

	router := gin.New()
	router.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-User") == "u0" {
			authctx.SetCurrentUser(c, ctx.users[0])
		}
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1"), ctx.service)

	body, err := json.Marshal(map[string]any{"rating": model.DebateEvidenceRatingPartial})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/debate/topics/"+ctx.debate.ID.String()+"/evidence-vote", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", "u0")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var putPayload struct {
		Data VoteStats `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &putPayload))
	require.Equal(t, 1, putPayload.Data.EvidencePartial)
	require.Equal(t, model.DebateEvidenceRatingPartial, putPayload.Data.CurrentUserEvidenceVote)

	// Invalid rating rejected 400.
	badBody, _ := json.Marshal(map[string]any{"rating": "invalid"})
	badReq := httptest.NewRequest(http.MethodPut, "/api/v1/debate/topics/"+ctx.debate.ID.String()+"/evidence-vote", bytes.NewReader(badBody))
	badReq.Header.Set("Content-Type", "application/json")
	badReq.Header.Set("X-Test-User", "u0")
	badRec := httptest.NewRecorder()
	router.ServeHTTP(badRec, badReq)
	require.Equal(t, http.StatusBadRequest, badRec.Code, badRec.Body.String())

	// Delete evidence vote.
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/debate/topics/"+ctx.debate.ID.String()+"/evidence-vote", nil)
	delReq.Header.Set("X-Test-User", "u0")
	delRec := httptest.NewRecorder()
	router.ServeHTTP(delRec, delReq)
	require.Equal(t, http.StatusOK, delRec.Code, delRec.Body.String())

	var delPayload struct {
		Data VoteStats `json:"data"`
	}
	require.NoError(t, json.Unmarshal(delRec.Body.Bytes(), &delPayload))
	require.Equal(t, 0, delPayload.Data.EvidencePartial)
	require.Empty(t, delPayload.Data.CurrentUserEvidenceVote)
	require.NotEqual(t, uuid.Nil, ctx.debate.ID)
}
