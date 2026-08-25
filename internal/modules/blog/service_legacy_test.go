package blog

import (
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
)

// CreatePost keeps legacy test fixtures focused on their assertions while production
// callers use CreateBlogContent and canonical ContentEntry identity exclusively.
func (s *Service) CreatePost(user authctx.CurrentUser, req CreatePostRequest) (model.Post, error) {
	content, err := s.CreateBlogContent(user, req)
	if err != nil {
		return model.Post{}, err
	}
	canonical, err := loadCanonicalBlogContent(s.db, content.ID)
	if err != nil {
		return model.Post{}, err
	}
	return model.Post{
		Base: model.Base{ID: canonical.ID, CreatedAt: canonical.CreatedAt, UpdatedAt: canonical.UpdatedAt}, UserID: canonical.UserID, User: canonical.User, ChannelID: canonical.ChannelID,
		Channel: canonical.Channel, CollectionID: canonical.CollectionID, CollectionPosition: canonical.CollectionPosition,
		CollectionConflict: canonical.CollectionConflict, Title: canonical.Title, Content: canonical.Content,
		Summary: canonical.Summary, LanguageCode: canonical.LanguageCode, CoverURL: canonical.CoverURL,
		Status: canonical.Status, Visibility: canonical.Visibility, Pinned: canonical.Pinned,
		ScheduledAt: canonical.ScheduledAt, PublishedAt: canonical.PublishedAt, ViewCount: canonical.ViewCount,
	}, nil
}
