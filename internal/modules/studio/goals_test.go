package studio

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"atoman/internal/model"

	"github.com/stretchr/testify/require"
)

func TestStudioGoalCycleSupportsGoalsActionsAndReview(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	now := time.Now().UTC()
	publishedAt := now.AddDate(0, 0, -2)
	entry := model.ContentEntry{
		AuthorID: &fixture.user.ID, ChannelID: fixture.channel.ID, Kind: "blog", Title: "Goal article",
		Status: "published", Visibility: "public", PublishedAt: &publishedAt,
	}
	require.NoError(t, fixture.db.Create(&entry).Error)

	cycle, err := fixture.service.CreateGoalCycle(fixture.user, CreateStudioGoalCycleInput{
		ChannelID: fixture.channel.ID,
		StartDate: now.AddDate(0, 0, -3).Format("2006-01-02"),
		EndDate:   now.AddDate(0, 0, -1).Format("2006-01-02"),
		Timezone:  "UTC",
	})
	require.NoError(t, err)
	require.Equal(t, "needs_review", cycle.Status)

	goal, err := fixture.service.CreateGoal(fixture.user, cycle.ID, CreateStudioGoalInput{
		Name: "稳定发布产品观察", Module: ModuleBlog, Metric: "published", TargetValue: 2,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), goal.CurrentValue)

	action, err := fixture.service.CreateGoalAction(fixture.user, goal.ID, CreateStudioGoalActionInput{
		Title:   "完成第二篇观察",
		DueDate: now.AddDate(0, 0, -1).Format("2006-01-02"),
	})
	require.NoError(t, err)
	require.Equal(t, "pending", action.Status)
	action, err = fixture.service.UpdateGoalAction(fixture.user, action.ID, UpdateStudioGoalActionInput{Status: stringPtr("completed")})
	require.NoError(t, err)
	require.Equal(t, "completed", action.Status)

	cycle, err = fixture.service.ReviewGoalCycle(fixture.user, cycle.ID, CreateStudioGoalReviewInput{
		Result: "完成一篇，下一周期减少并行主题。", Learning: "提前安排素材更容易坚持。", NextAction: "保留周一选题时间。",
	})
	require.NoError(t, err)
	require.Equal(t, "reviewed", cycle.Status)
	require.NotNil(t, cycle.Review)
	require.Len(t, cycle.Goals, 1)
	require.Equal(t, int64(1), cycle.Goals[0].CurrentValue)
	require.Equal(t, int64(1), *cycle.Goals[0].ActualValue)

	_, err = fixture.service.CreateGoal(fixture.user, cycle.ID, CreateStudioGoalInput{Name: "不能修改", Module: ModuleBlog, Metric: "published", TargetValue: 1})
	require.Error(t, err)
}

func TestStudioGoalCycleRejectsOverlappingPeriods(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	input := CreateStudioGoalCycleInput{
		ChannelID: fixture.channel.ID,
		StartDate: "2026-07-01",
		EndDate:   "2026-07-31",
		Timezone:  "Asia/Shanghai",
	}
	_, err := fixture.service.CreateGoalCycle(fixture.user, input)
	require.NoError(t, err)
	_, err = fixture.service.CreateGoalCycle(fixture.user, CreateStudioGoalCycleInput{
		ChannelID: fixture.channel.ID, StartDate: "2026-07-15", EndDate: "2026-08-15", Timezone: "Asia/Shanghai",
	})
	require.Error(t, err)
}

func TestStudioGoalRoutesExposeCycleAndReviewWorkflow(t *testing.T) {
	fixture := newStudioHTTPFixture(t)
	channel := createStudioChannel(t, fixture.db, fixture.owner, "Goal Routes")
	start := time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02")
	end := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	create := studioRequest(t, fixture, fixture.owner, http.MethodPost, "/api/v1/studio/goals/cycles", `{"channel_id":"`+channel.ID.String()+`","start_date":"`+start+`","end_date":"`+end+`","timezone":"UTC"}`)
	require.Equal(t, http.StatusCreated, create.Code, create.Body.String())
	var cyclePayload struct {
		Data StudioGoalCycle `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &cyclePayload))

	goal := studioRequest(t, fixture, fixture.owner, http.MethodPost, "/api/v1/studio/goals/cycles/"+cyclePayload.Data.ID.String()+"/goals", `{"name":"发布目标","module":"blog","metric":"published","target_value":1}`)
	require.Equal(t, http.StatusCreated, goal.Code, goal.Body.String())
	response := studioRequest(t, fixture, fixture.owner, http.MethodGet, "/api/v1/studio/goals?channel_id="+channel.ID.String(), "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
}

func stringPtr(value string) *string { return &value }
