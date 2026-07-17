package model

import "github.com/google/uuid"

type ForumGroup struct {
	Base
	Name        string             `json:"name" gorm:"not null;uniqueIndex"`
	Description string             `json:"description" gorm:"type:text"`
	Members     []ForumGroupMember `json:"members,omitempty" gorm:"foreignKey:GroupID"`
}

func (ForumGroup) TableName() string { return "forum_groups" }

type ForumGroupMember struct {
	Base
	GroupID uuid.UUID `json:"group_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_forum_group_members_group_user,priority:1"`
	UserID  uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_forum_group_members_group_user,priority:2"`
	User    *User     `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
}

func (ForumGroupMember) TableName() string { return "forum_group_members" }

type ForumCategoryPermission struct {
	Base
	CategoryID     uuid.UUID      `json:"category_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_forum_category_permissions_category_group,priority:1"`
	Category       *ForumCategory `json:"category,omitempty" gorm:"foreignKey:CategoryID"`
	GroupID        uuid.UUID      `json:"group_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_forum_category_permissions_category_group,priority:2"`
	Group          *ForumGroup    `json:"group,omitempty" gorm:"foreignKey:GroupID"`
	CanView        bool           `json:"can_view" gorm:"not null;default:false"`
	CanCreateTopic bool           `json:"can_create_topic" gorm:"not null;default:false"`
	CanComment     bool           `json:"can_comment" gorm:"not null;default:false"`
}

func (ForumCategoryPermission) TableName() string { return "forum_category_permissions" }
