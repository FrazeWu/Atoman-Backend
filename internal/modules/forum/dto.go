package forum

import "github.com/google/uuid"

type ListTopicsQuery struct {
	CategoryID uuid.UUID `json:"category_id" form:"category_id"`
	Search     string    `json:"search" form:"search"`
	Page       int       `json:"page" form:"page"`
	PageSize   int       `json:"page_size" form:"page_size"`
}

type CreateTopicRequest struct {
	CategoryID uuid.UUID `json:"category_id"`
	Title      string    `json:"title"`
	Content    string    `json:"content"`
}

type UpdateTopicRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type SaveDraftRequest struct {
	ContextKey string `json:"context_key"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	Tags       string `json:"tags"`
}

type CreateCategoryRequestRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Reason      string `json:"reason"`
}
