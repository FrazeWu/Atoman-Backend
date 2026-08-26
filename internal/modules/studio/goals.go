package studio

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxGoalCycleLength = 366 * 24 * time.Hour

var goalMetricLabels = map[Module]map[string]string{
	ModuleBlog: {
		"published": "发布数量",
		"view":      "阅读",
		"comment":   "评论",
		"like":      "点赞",
		"bookmark":  "收藏",
		"share":     "分享",
	},
	ModulePodcast: {
		"published": "发布数量",
		"play":      "播放",
		"complete":  "完成播放",
		"comment":   "评论",
		"bookmark":  "收藏",
		"share":     "分享",
	},
	ModuleVideo: {
		"published": "发布数量",
		"play":      "播放",
		"complete":  "完成播放",
		"comment":   "评论",
		"like":      "点赞",
		"bookmark":  "收藏",
		"share":     "分享",
	},
}

var goalMetricOrder = map[Module][]string{
	ModuleBlog:    {"published", "view", "comment", "like", "bookmark", "share"},
	ModulePodcast: {"published", "play", "complete", "comment", "bookmark", "share"},
	ModuleVideo:   {"published", "play", "complete", "comment", "like", "bookmark", "share"},
}

func goalMetricOptions() []StudioGoalMetricOption {
	options := make([]StudioGoalMetricOption, 0, 19)
	for _, module := range []Module{ModuleBlog, ModulePodcast, ModuleVideo} {
		for _, metric := range goalMetricOrder[module] {
			options = append(options, StudioGoalMetricOption{
				Module: module,
				Metric: metric,
				Label:  goalMetricLabels[module][metric],
			})
		}
	}
	return options
}

func goalMetricAllowed(module Module, metric string) bool {
	_, ok := goalMetricLabels[module][metric]
	return ok
}

func parseGoalDate(value, field string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, apperr.BadRequest("studio.invalid_goal_cycle", field+" must be a YYYY-MM-DD date")
	}
	return parsed.UTC(), nil
}

func normalizeGoalTimezone(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "UTC", nil
	}
	if _, err := time.LoadLocation(value); err != nil {
		return "", apperr.BadRequest("studio.invalid_goal_timezone", "timezone must be a valid IANA timezone")
	}
	return value, nil
}

func goalDate(value time.Time) string {
	return value.UTC().Format("2006-01-02")
}

func goalCycleBounds(cycle model.StudioGoalCycle) (time.Time, time.Time, error) {
	location, err := time.LoadLocation(cycle.Timezone)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start := cycle.StartDate.UTC()
	end := cycle.EndDate.UTC()
	startLocal := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, location)
	endLocal := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, location).AddDate(0, 0, 1)
	return startLocal.UTC(), endLocal.UTC(), nil
}

func (s *Service) ListGoals(user authctx.CurrentUser, channelID uuid.UUID) (StudioGoalsResponse, error) {
	if err := requireUser(user); err != nil {
		return StudioGoalsResponse{}, err
	}
	channel, err := s.resolveContentChannel(user.ID, channelID)
	if err != nil {
		return StudioGoalsResponse{}, err
	}
	var cycles []model.StudioGoalCycle
	if err := s.db.Where("channel_id = ?", channel.ID).
		Preload("Goals", func(db *gorm.DB) *gorm.DB { return db.Order("created_at ASC") }).
		Preload("Goals.Actions", func(db *gorm.DB) *gorm.DB { return db.Order("created_at ASC") }).
		Preload("Review").
		Order("start_date DESC, created_at DESC").Limit(12).Find(&cycles).Error; err != nil {
		return StudioGoalsResponse{}, err
	}

	response := StudioGoalsResponse{Cycles: make([]StudioGoalCycle, 0, len(cycles)), Metrics: goalMetricOptions()}
	for index := range cycles {
		cycle, err := s.goalCycleResponse(user, cycles[index])
		if err != nil {
			return StudioGoalsResponse{}, err
		}
		response.Cycles = append(response.Cycles, cycle)
		if response.CurrentCycle == nil && (cycle.Status == "active" || cycle.Status == "needs_review") {
			current := cycle
			response.CurrentCycle = &current
		}
	}
	return response, nil
}

func (s *Service) CreateGoalCycle(user authctx.CurrentUser, input CreateStudioGoalCycleInput) (StudioGoalCycle, error) {
	if err := requireUser(user); err != nil {
		return StudioGoalCycle{}, err
	}
	channel, err := s.resolveContentChannel(user.ID, input.ChannelID)
	if err != nil {
		return StudioGoalCycle{}, err
	}
	start, err := parseGoalDate(input.StartDate, "start_date")
	if err != nil {
		return StudioGoalCycle{}, err
	}
	end, err := parseGoalDate(input.EndDate, "end_date")
	if err != nil {
		return StudioGoalCycle{}, err
	}
	if !start.Before(end) || end.Sub(start) > maxGoalCycleLength {
		return StudioGoalCycle{}, apperr.BadRequest("studio.invalid_goal_cycle", "goal cycle must be between 1 and 366 days")
	}
	timezone, err := normalizeGoalTimezone(input.Timezone)
	if err != nil {
		return StudioGoalCycle{}, err
	}
	var overlap int64
	if err := s.db.Model(&model.StudioGoalCycle{}).
		Where("channel_id = ? AND start_date <= ? AND end_date >= ?", channel.ID, end, start).
		Count(&overlap).Error; err != nil {
		return StudioGoalCycle{}, err
	}
	if overlap > 0 {
		return StudioGoalCycle{}, apperr.Conflict("studio.goal_cycle_overlap", "goal cycle overlaps an existing cycle")
	}
	cycle := model.StudioGoalCycle{ChannelID: channel.ID, CreatedBy: user.ID, StartDate: start, EndDate: end, Timezone: timezone}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&cycle).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.goal_cycle_created", "studio_goal_cycle", cycle.ID, map[string]any{
			"channel_id": channel.ID.String(), "start_date": goalDate(start), "end_date": goalDate(end),
		})
	}); err != nil {
		return StudioGoalCycle{}, err
	}
	return s.loadGoalCycleResponse(user, cycle.ID)
}

func (s *Service) CreateGoal(user authctx.CurrentUser, cycleID uuid.UUID, input CreateStudioGoalInput) (StudioGoal, error) {
	if err := requireUser(user); err != nil {
		return StudioGoal{}, err
	}
	cycle, err := s.goalCycleForUser(user.ID, cycleID)
	if err != nil {
		return StudioGoal{}, err
	}
	channel, err := s.ownedChannel(user.ID, cycle.ChannelID)
	if err != nil {
		return StudioGoal{}, err
	}
	if cycle.Review != nil {
		return StudioGoal{}, apperr.Conflict("studio.goal_cycle_reviewed", "reviewed goal cycles cannot be changed")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 160 {
		return StudioGoal{}, apperr.BadRequest("studio.invalid_goal", "name is required and must be at most 160 characters")
	}
	module, err := ParseModule(string(input.Module))
	if err != nil {
		return StudioGoal{}, err
	}
	if !goalMetricAllowed(module, input.Metric) {
		return StudioGoal{}, apperr.BadRequest("studio.invalid_goal_metric", "metric is not available for this module")
	}
	if input.TargetValue <= 0 {
		return StudioGoal{}, apperr.BadRequest("studio.invalid_goal", "target_value must be greater than zero")
	}
	baseline, err := s.calculateGoalValue(user.ID, channel.ID, cycle, module, input.Metric, time.Now().UTC())
	if err != nil {
		return StudioGoal{}, err
	}
	goal := model.StudioGoal{
		CycleID: cycle.ID, ChannelID: channel.ID, Name: name, Module: string(module), Metric: input.Metric,
		BaselineValue: baseline, TargetValue: input.TargetValue,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&goal).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.goal_created", "studio_goal", goal.ID, map[string]any{
			"cycle_id": cycle.ID.String(), "module": module, "metric": input.Metric, "target_value": input.TargetValue,
		})
	}); err != nil {
		return StudioGoal{}, err
	}
	return s.loadGoalResponse(user, goal.ID)
}

func (s *Service) UpdateGoal(user authctx.CurrentUser, goalID uuid.UUID, input UpdateStudioGoalInput) (StudioGoal, error) {
	if err := requireUser(user); err != nil {
		return StudioGoal{}, err
	}
	var goal model.StudioGoal
	if err := s.db.Preload("Actions").Preload("Cycle").Preload("Cycle.Review").First(&goal, "id = ?", goalID).Error; err != nil {
		return StudioGoal{}, contentLookupError(err)
	}
	if _, err := s.ownedChannel(user.ID, goal.ChannelID); err != nil {
		return StudioGoal{}, err
	}
	if goal.Cycle == nil {
		return StudioGoal{}, apperr.NotFound("studio.goal_cycle_not_found", "goal cycle not found")
	}
	if goal.Cycle.Review != nil {
		return StudioGoal{}, apperr.Conflict("studio.goal_cycle_reviewed", "reviewed goal cycles cannot be changed")
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || len([]rune(name)) > 160 {
			return StudioGoal{}, apperr.BadRequest("studio.invalid_goal", "name is required and must be at most 160 characters")
		}
		goal.Name = name
	}
	if input.TargetValue != nil {
		if *input.TargetValue <= 0 {
			return StudioGoal{}, apperr.BadRequest("studio.invalid_goal", "target_value must be greater than zero")
		}
		goal.TargetValue = *input.TargetValue
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&goal).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.goal_updated", "studio_goal", goal.ID, nil)
	}); err != nil {
		return StudioGoal{}, err
	}
	return s.loadGoalResponse(user, goal.ID)
}

func (s *Service) CreateGoalAction(user authctx.CurrentUser, goalID uuid.UUID, input CreateStudioGoalActionInput) (StudioGoalAction, error) {
	if err := requireUser(user); err != nil {
		return StudioGoalAction{}, err
	}
	goal, channel, err := s.goalForUser(user.ID, goalID)
	if err != nil {
		return StudioGoalAction{}, err
	}
	if goal.Cycle == nil {
		return StudioGoalAction{}, apperr.NotFound("studio.goal_cycle_not_found", "goal cycle not found")
	}
	if goal.Cycle.Review != nil {
		return StudioGoalAction{}, apperr.Conflict("studio.goal_cycle_reviewed", "reviewed goal cycles cannot be changed")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" || len([]rune(title)) > 240 {
		return StudioGoalAction{}, apperr.BadRequest("studio.invalid_goal_action", "title is required and must be at most 240 characters")
	}
	dueDate, err := parseOptionalGoalDate(input.DueDate)
	if err != nil {
		return StudioGoalAction{}, err
	}
	if dueDate != nil && (dueDate.Before(goal.Cycle.StartDate) || dueDate.After(goal.Cycle.EndDate)) {
		return StudioGoalAction{}, apperr.BadRequest("studio.invalid_goal_action", "due_date must be inside the goal cycle")
	}
	if input.ContentID != nil {
		contentModule, parseErr := ParseModule(string(input.ContentModule))
		if parseErr != nil {
			return StudioGoalAction{}, parseErr
		}
		var count int64
		if err := s.db.Model(&model.ContentEntry{}).Where("id = ? AND channel_id = ? AND author_id = ? AND kind = ?", *input.ContentID, channel.ID, user.ID, string(contentModule)).Count(&count).Error; err != nil {
			return StudioGoalAction{}, err
		}
		if count == 0 {
			return StudioGoalAction{}, apperr.NotFound("studio.content_not_found", "content not found")
		}
		input.ContentModule = contentModule
	}
	action := model.StudioGoalAction{
		GoalID: goal.ID, CycleID: goal.CycleID, ChannelID: channel.ID, Title: title, Status: "pending",
		DueDate: dueDate, ContentID: input.ContentID, ContentModule: string(input.ContentModule),
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&action).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.goal_action_created", "studio_goal_action", action.ID, map[string]any{"goal_id": goal.ID.String()})
	}); err != nil {
		return StudioGoalAction{}, err
	}
	return studioGoalActionResponse(action), nil
}

func (s *Service) UpdateGoalAction(user authctx.CurrentUser, actionID uuid.UUID, input UpdateStudioGoalActionInput) (StudioGoalAction, error) {
	if err := requireUser(user); err != nil {
		return StudioGoalAction{}, err
	}
	var action model.StudioGoalAction
	if err := s.db.Preload("Goal").Preload("Goal.Cycle").Preload("Goal.Cycle.Review").First(&action, "id = ?", actionID).Error; err != nil {
		return StudioGoalAction{}, contentLookupError(err)
	}
	if _, err := s.ownedChannel(user.ID, action.ChannelID); err != nil {
		return StudioGoalAction{}, err
	}
	if action.Goal == nil || action.Goal.Cycle == nil {
		return StudioGoalAction{}, apperr.NotFound("studio.goal_cycle_not_found", "goal cycle not found")
	}
	if action.Goal.Cycle.Review != nil {
		return StudioGoalAction{}, apperr.Conflict("studio.goal_cycle_reviewed", "reviewed goal cycles cannot be changed")
	}
	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		if title == "" || len([]rune(title)) > 240 {
			return StudioGoalAction{}, apperr.BadRequest("studio.invalid_goal_action", "title is required and must be at most 240 characters")
		}
		action.Title = title
	}
	if input.Status != nil {
		if *input.Status != "pending" && *input.Status != "completed" {
			return StudioGoalAction{}, apperr.BadRequest("studio.invalid_goal_action", "status must be pending or completed")
		}
		action.Status = *input.Status
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&action).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.goal_action_updated", "studio_goal_action", action.ID, nil)
	}); err != nil {
		return StudioGoalAction{}, err
	}
	return studioGoalActionResponse(action), nil
}

func (s *Service) DeleteGoalAction(user authctx.CurrentUser, actionID uuid.UUID) error {
	if err := requireUser(user); err != nil {
		return err
	}
	var action model.StudioGoalAction
	if err := s.db.Preload("Goal").Preload("Goal.Cycle").Preload("Goal.Cycle.Review").First(&action, "id = ?", actionID).Error; err != nil {
		return contentLookupError(err)
	}
	if _, err := s.ownedChannel(user.ID, action.ChannelID); err != nil {
		return err
	}
	if action.Goal == nil || action.Goal.Cycle == nil {
		return apperr.NotFound("studio.goal_cycle_not_found", "goal cycle not found")
	}
	if action.Goal.Cycle.Review != nil {
		return apperr.Conflict("studio.goal_cycle_reviewed", "reviewed goal cycles cannot be changed")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&action).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.goal_action_deleted", "studio_goal_action", action.ID, nil)
	})
}

func (s *Service) ReviewGoalCycle(user authctx.CurrentUser, cycleID uuid.UUID, input CreateStudioGoalReviewInput) (StudioGoalCycle, error) {
	if err := requireUser(user); err != nil {
		return StudioGoalCycle{}, err
	}
	cycle, err := s.goalCycleForUser(user.ID, cycleID)
	if err != nil {
		return StudioGoalCycle{}, err
	}
	channel, err := s.ownedChannel(user.ID, cycle.ChannelID)
	if err != nil {
		return StudioGoalCycle{}, err
	}
	if cycle.Review != nil {
		return StudioGoalCycle{}, apperr.Conflict("studio.goal_cycle_reviewed", "goal cycle has already been reviewed")
	}
	result := strings.TrimSpace(input.Result)
	if result == "" {
		return StudioGoalCycle{}, apperr.BadRequest("studio.invalid_goal_review", "result is required")
	}
	_, cycleEnd, err := goalCycleBounds(cycle)
	if err != nil {
		return StudioGoalCycle{}, err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var goals []model.StudioGoal
		if err := tx.Where("cycle_id = ?", cycle.ID).Find(&goals).Error; err != nil {
			return err
		}
		calculator := &Service{db: tx}
		for _, goal := range goals {
			module, parseErr := ParseModule(goal.Module)
			if parseErr != nil {
				return parseErr
			}
			value, valueErr := calculator.calculateGoalValue(user.ID, channel.ID, cycle, module, goal.Metric, cycleEnd)
			if valueErr != nil {
				return valueErr
			}
			if err := tx.Model(&model.StudioGoal{}).Where("id = ?", goal.ID).Update("actual_value", value).Error; err != nil {
				return err
			}
		}
		review := model.StudioGoalReview{CycleID: cycle.ID, ChannelID: channel.ID, CreatedBy: user.ID, Result: result, Learning: strings.TrimSpace(input.Learning), NextAction: strings.TrimSpace(input.NextAction)}
		if err := tx.Create(&review).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.goal_cycle_reviewed", "studio_goal_cycle", cycle.ID, map[string]any{"goal_count": len(goals)})
	}); err != nil {
		return StudioGoalCycle{}, err
	}
	return s.loadGoalCycleResponse(user, cycle.ID)
}

func parseOptionalGoalDate(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := parseGoalDate(value, "due_date")
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func (s *Service) goalCycleForUser(userID, cycleID uuid.UUID) (model.StudioGoalCycle, error) {
	if cycleID == uuid.Nil {
		return model.StudioGoalCycle{}, apperr.NotFound("studio.goal_cycle_not_found", "goal cycle not found")
	}
	var cycle model.StudioGoalCycle
	if err := s.db.Preload("Review").First(&cycle, "id = ?", cycleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.StudioGoalCycle{}, apperr.NotFound("studio.goal_cycle_not_found", "goal cycle not found")
		}
		return model.StudioGoalCycle{}, err
	}
	if _, err := s.ownedChannel(userID, cycle.ChannelID); err != nil {
		return model.StudioGoalCycle{}, err
	}
	return cycle, nil
}

func (s *Service) goalForUser(userID, goalID uuid.UUID) (model.StudioGoal, model.Channel, error) {
	var goal model.StudioGoal
	if err := s.db.Preload("Cycle").Preload("Cycle.Review").First(&goal, "id = ?", goalID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.StudioGoal{}, model.Channel{}, apperr.NotFound("studio.goal_not_found", "goal not found")
		}
		return model.StudioGoal{}, model.Channel{}, err
	}
	channel, err := s.ownedChannel(userID, goal.ChannelID)
	if err != nil {
		return model.StudioGoal{}, model.Channel{}, err
	}
	return goal, channel, nil
}

func (s *Service) loadGoalCycleResponse(user authctx.CurrentUser, cycleID uuid.UUID) (StudioGoalCycle, error) {
	var cycle model.StudioGoalCycle
	if err := s.db.Preload("Goals", func(db *gorm.DB) *gorm.DB { return db.Order("created_at ASC") }).Preload("Goals.Actions", func(db *gorm.DB) *gorm.DB { return db.Order("created_at ASC") }).Preload("Review").First(&cycle, "id = ?", cycleID).Error; err != nil {
		return StudioGoalCycle{}, contentLookupError(err)
	}
	if _, err := s.ownedChannel(user.ID, cycle.ChannelID); err != nil {
		return StudioGoalCycle{}, err
	}
	return s.goalCycleResponse(user, cycle)
}

func (s *Service) loadGoalResponse(user authctx.CurrentUser, goalID uuid.UUID) (StudioGoal, error) {
	var goal model.StudioGoal
	if err := s.db.Preload("Cycle").Preload("Cycle.Review").Preload("Actions", func(db *gorm.DB) *gorm.DB { return db.Order("created_at ASC") }).First(&goal, "id = ?", goalID).Error; err != nil {
		return StudioGoal{}, contentLookupError(err)
	}
	if _, err := s.ownedChannel(user.ID, goal.ChannelID); err != nil {
		return StudioGoal{}, err
	}
	if goal.Cycle == nil {
		return StudioGoal{}, apperr.NotFound("studio.goal_cycle_not_found", "goal cycle not found")
	}
	return s.goalResponse(user, *goal.Cycle, goal)
}

func (s *Service) goalCycleResponse(user authctx.CurrentUser, cycle model.StudioGoalCycle) (StudioGoalCycle, error) {
	status, needsReview := goalCycleStatus(cycle)
	response := StudioGoalCycle{
		ID: cycle.ID, ChannelID: cycle.ChannelID, StartDate: goalDate(cycle.StartDate), EndDate: goalDate(cycle.EndDate),
		Timezone: cycle.Timezone, Status: status, NeedsReview: needsReview, Goals: make([]StudioGoal, 0, len(cycle.Goals)),
	}
	if cycle.Review != nil {
		response.Review = &StudioGoalReview{ID: cycle.Review.ID, Result: cycle.Review.Result, Learning: cycle.Review.Learning, NextAction: cycle.Review.NextAction, CreatedAt: cycle.Review.CreatedAt}
	}
	for _, goal := range cycle.Goals {
		item, err := s.goalResponse(user, cycle, goal)
		if err != nil {
			return StudioGoalCycle{}, err
		}
		response.Goals = append(response.Goals, item)
	}
	return response, nil
}

func goalCycleStatus(cycle model.StudioGoalCycle) (string, bool) {
	if cycle.Review != nil {
		return "reviewed", false
	}
	location, err := time.LoadLocation(cycle.Timezone)
	if err != nil {
		location = time.UTC
	}
	today := time.Now().In(location).Format("2006-01-02")
	if today < goalDate(cycle.StartDate) {
		return "planned", false
	}
	if today > goalDate(cycle.EndDate) {
		return "needs_review", true
	}
	return "active", false
}

func (s *Service) goalResponse(user authctx.CurrentUser, cycle model.StudioGoalCycle, goal model.StudioGoal) (StudioGoal, error) {
	current := int64(0)
	if goal.ActualValue != nil {
		current = *goal.ActualValue
	} else {
		value, err := s.calculateGoalValue(user.ID, cycle.ChannelID, cycle, Module(goal.Module), goal.Metric, time.Now().UTC())
		if err != nil {
			return StudioGoal{}, err
		}
		current = value
	}
	return StudioGoal{
		ID: goal.ID, CycleID: goal.CycleID, Name: goal.Name, Module: Module(goal.Module), Metric: goal.Metric,
		BaselineValue: goal.BaselineValue, TargetValue: goal.TargetValue, CurrentValue: current, ActualValue: goal.ActualValue,
		Progress: goalProgress(current, goal.TargetValue), Actions: studioGoalActionResponses(goal.Actions),
	}, nil
}

func goalProgress(current, target int64) int {
	if target <= 0 {
		return 0
	}
	progress := int(current * 100 / target)
	if progress > 100 {
		return 100
	}
	if progress < 0 {
		return 0
	}
	return progress
}

func studioGoalActionResponses(actions []model.StudioGoalAction) []StudioGoalAction {
	responses := make([]StudioGoalAction, 0, len(actions))
	for _, action := range actions {
		responses = append(responses, studioGoalActionResponse(action))
	}
	return responses
}

func studioGoalActionResponse(action model.StudioGoalAction) StudioGoalAction {
	return StudioGoalAction{ID: action.ID, GoalID: action.GoalID, Title: action.Title, Status: action.Status, DueDate: action.DueDate, ContentID: action.ContentID, ContentModule: Module(action.ContentModule)}
}

func (s *Service) calculateGoalValue(userID, channelID uuid.UUID, cycle model.StudioGoalCycle, module Module, metric string, until time.Time) (int64, error) {
	from, to, err := goalCycleBounds(cycle)
	if err != nil {
		return 0, err
	}
	if until.Before(to) {
		to = until
	}
	if to.Before(from) {
		return 0, nil
	}
	if metric == "published" {
		var count int64
		err := s.db.Model(&model.ContentEntry{}).Where("author_id = ? AND channel_id = ? AND kind = ? AND status = ? AND published_at >= ? AND published_at < ?", userID, channelID, string(module), "published", from, to).Count(&count).Error
		return count, err
	}
	analyticsService := &Service{db: s.db}
	titles, _, err := analyticsService.analyticsContentMetrics(userID, channelID, module)
	if err != nil {
		return 0, err
	}
	contentIDs := make([]uuid.UUID, 0, len(titles))
	for id := range titles {
		contentIDs = append(contentIDs, id)
	}
	totals, err := analyticsService.analyticsTotals(channelID, module, from, to, titles, contentIDs, false)
	if err != nil {
		return 0, err
	}
	return totals[metric], nil
}
