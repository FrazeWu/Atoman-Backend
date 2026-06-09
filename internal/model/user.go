package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type User struct {
	UUID        uuid.UUID      `json:"uuid" gorm:"type:uuid;primaryKey"`
	ID          uint           `json:"id" gorm:"unique;autoIncrement"` // Frontend identifier
	Username    string         `json:"username" gorm:"unique;not null;column:username"`
	Email       string         `json:"email" gorm:"unique;not null;column:email"`
	Password    string         `json:"-" gorm:"not null;column:password"`
	Role        string         `json:"role" gorm:"default:'user';column:role"` // user / moderator / admin
	DisplayName string         `json:"display_name" gorm:"column:display_name"`
	AvatarURL   string         `json:"avatar_url" gorm:"column:avatar_url"`
	Bio         string         `json:"bio" gorm:"type:text;column:bio"`
	Website     string         `json:"website" gorm:"column:website"`
	Location    string         `json:"location" gorm:"column:location"`
	IsActive    bool           `json:"is_active" gorm:"default:true;column:is_active"`
	OnboardingCompletedAt *time.Time     `json:"onboarding_completed_at" gorm:"column:onboarding_completed_at"`
	CreatedAt   time.Time      `json:"created_at" gorm:"column:created_at"`
	UpdatedAt   time.Time      `json:"updated_at" gorm:"column:updated_at"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.UUID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		u.UUID = id
	}
	return nil
}

func (User) TableName() string {
	return "Users"
}

type Follow struct {
	FollowerID  uuid.UUID `json:"follower_id" gorm:"type:uuid;primaryKey"`
	FollowingID uuid.UUID `json:"following_id" gorm:"type:uuid;primaryKey"`
	CreatedAt   time.Time `json:"created_at"`
}

func (Follow) TableName() string {
	return "follows"
}

type UserSettings struct {
	UserID         uuid.UUID `json:"user_id" gorm:"type:uuid;primaryKey"`
	PrivateProfile bool      `json:"private_profile" gorm:"default:false"`
	DMPermission   string    `json:"dm_permission" gorm:"default:'anyone'"`
}

func (UserSettings) TableName() string {
	return "user_settings"
}
