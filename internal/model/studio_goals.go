package model

import (
	"time"

	"github.com/google/uuid"
)

// StudioGoalCycle defines a channel-scoped planning period.
type StudioGoalCycle struct {
	Base
	ChannelID uuid.UUID         `json:"channel_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_studio_goal_cycle_range,priority:1"`
	CreatedBy uuid.UUID         `json:"created_by" gorm:"type:uuid;not null;index"`
	StartDate time.Time         `json:"start_date" gorm:"type:date;not null;uniqueIndex:idx_studio_goal_cycle_range,priority:2"`
	EndDate   time.Time         `json:"end_date" gorm:"type:date;not null;uniqueIndex:idx_studio_goal_cycle_range,priority:3"`
	Timezone  string            `json:"timezone" gorm:"type:varchar(64);not null;default:'UTC'"`
	Goals     []StudioGoal      `json:"goals,omitempty" gorm:"foreignKey:CycleID"`
	Review    *StudioGoalReview `json:"review,omitempty" gorm:"foreignKey:CycleID"`
}

func (StudioGoalCycle) TableName() string { return "studio_goal_cycles" }

// StudioGoal stores one measurable target inside a planning period.
type StudioGoal struct {
	Base
	CycleID       uuid.UUID          `json:"cycle_id" gorm:"type:uuid;not null;index"`
	ChannelID     uuid.UUID          `json:"channel_id" gorm:"type:uuid;not null;index"`
	Cycle         *StudioGoalCycle   `json:"cycle,omitempty" gorm:"foreignKey:CycleID"`
	Name          string             `json:"name" gorm:"type:varchar(160);not null"`
	Module        string             `json:"module" gorm:"type:varchar(16);not null"`
	Metric        string             `json:"metric" gorm:"type:varchar(32);not null"`
	BaselineValue int64              `json:"baseline_value" gorm:"not null;default:0"`
	TargetValue   int64              `json:"target_value" gorm:"not null"`
	ActualValue   *int64             `json:"actual_value,omitempty"`
	Actions       []StudioGoalAction `json:"actions,omitempty" gorm:"foreignKey:GoalID"`
}

func (StudioGoal) TableName() string { return "studio_goals" }

// StudioGoalAction is a concrete piece of work connected to a goal.
type StudioGoalAction struct {
	Base
	GoalID        uuid.UUID   `json:"goal_id" gorm:"type:uuid;not null;index"`
	CycleID       uuid.UUID   `json:"cycle_id" gorm:"type:uuid;not null;index"`
	ChannelID     uuid.UUID   `json:"channel_id" gorm:"type:uuid;not null;index"`
	Goal          *StudioGoal `json:"goal,omitempty" gorm:"foreignKey:GoalID"`
	Title         string      `json:"title" gorm:"type:varchar(240);not null"`
	Status        string      `json:"status" gorm:"type:varchar(16);not null;default:'pending';index"`
	DueDate       *time.Time  `json:"due_date,omitempty" gorm:"type:date;index"`
	ContentID     *uuid.UUID  `json:"content_id,omitempty" gorm:"type:uuid;index"`
	ContentModule string      `json:"content_module,omitempty" gorm:"type:varchar(16)"`
}

func (StudioGoalAction) TableName() string { return "studio_goal_actions" }

// StudioGoalReview freezes the measured results and captures the creator's reflection.
type StudioGoalReview struct {
	Base
	CycleID    uuid.UUID `json:"cycle_id" gorm:"type:uuid;not null;uniqueIndex"`
	ChannelID  uuid.UUID `json:"channel_id" gorm:"type:uuid;not null;index"`
	CreatedBy  uuid.UUID `json:"created_by" gorm:"type:uuid;not null;index"`
	Result     string    `json:"result" gorm:"type:text;not null"`
	Learning   string    `json:"learning" gorm:"type:text"`
	NextAction string    `json:"next_action" gorm:"type:text"`
}

func (StudioGoalReview) TableName() string { return "studio_goal_reviews" }
