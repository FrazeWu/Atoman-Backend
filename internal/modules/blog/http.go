package blog

import (
	"atoman/internal/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
}

type channelInput struct {
	Name        string `json:"name" binding:"required"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
}

type collectionInput struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
}

type bookmarkInput struct {
	ContentID        uuid.UUID  `json:"content_id" binding:"required"`
	BookmarkFolderID *uuid.UUID `json:"bookmark_folder_id"`
}

type bookmarkFolderInput struct {
	Name string `json:"name" binding:"required"`
}

type postInput struct {
	Title         string  `json:"title" binding:"required"`
	Content       string  `json:"content" binding:"required"`
	Summary       string  `json:"summary"`
	CoverURL      string  `json:"cover_url"`
	Visibility    string  `json:"visibility"`
	AllowComments *bool   `json:"allow_comments"`
	Status        string  `json:"status"`
	ChannelID     *string `json:"channel_id"`
	CollectionID  *string `json:"collection_id"`
}

type postRatingInput struct {
	Score int `json:"score" binding:"required,min=1,max=10"`
}

type reorderCollectionPostsInput struct {
	PostIDs []string `json:"post_ids"`
}

type blogDraftInput struct {
	ContextKey      string `json:"context_key" binding:"required"`
	SourceContentID string `json:"source_content_id"`
	Title           string `json:"title"`
	Content         string `json:"content"`
	Summary         string `json:"summary"`
	CoverURL        string `json:"cover_url"`
	Visibility      string `json:"visibility"`
	AllowComments   *bool  `json:"allow_comments"`
	ChannelID       string `json:"channel_id"`
	CollectionID    string `json:"collection_id"`
}

type blogDraftResponse struct {
	ID              uuid.UUID `json:"id"`
	UserID          uuid.UUID `json:"user_id"`
	ContextKey      string    `json:"context_key"`
	SourceContentID *string   `json:"source_content_id,omitempty"`
	Title           string    `json:"title"`
	Content         string    `json:"content"`
	Summary         string    `json:"summary"`
	CoverURL        string    `json:"cover_url"`
	Visibility      string    `json:"visibility"`
	ChannelID       *string   `json:"channel_id,omitempty"`
	CollectionID    *string   `json:"collection_id,omitempty"`
	CreatedAt       any       `json:"created_at"`
	UpdatedAt       any       `json:"updated_at"`
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	canPublish := middleware.RequireSiteFeature(service.db, "blog", "post.create")
	group.GET("/seo/posts/:id", h.getSEOPost)
	group.GET("/seo/sitemap", h.listSEOSitemap)
	group.GET("/channels", h.listChannels)
	group.GET("/channels/:id", h.getChannel)
	group.GET("/channels/:id/collections", h.getChannelCollections)
	group.GET("/channels/slug/:slug", h.getChannelBySlug)
	group.GET("/channels/slug/:slug/collections", h.getChannelCollectionsBySlug)
	group.GET("/collections", h.listUserCollections)
	group.GET("/collections/:id", h.getCollection)
	group.POST("/channels/ensure-default", h.ensureDefaultChannel)
	group.POST("/channels", h.createChannel)
	group.PUT("/channels/:id", h.updateChannel)
	group.DELETE("/channels/:id", h.deleteChannel)
	group.POST("/channels/:id/collections", h.createCollection)
	group.PUT("/collections/:id", h.updateCollection)
	group.DELETE("/collections/:id", h.deleteCollection)
	group.GET("/posts/:id/likes/count", h.getPostLikesCount)
	group.POST("/likes", h.createLike)
	group.DELETE("/likes", h.deleteLike)
	group.PUT("/posts/:id/rating", h.setPostRating)
	group.DELETE("/posts/:id/rating", h.deletePostRating)
	group.GET("/bookmarks", h.listBookmarks)
	group.POST("/bookmarks", h.createBookmark)
	group.DELETE("/bookmarks/:id", h.deleteBookmark)
	group.GET("/bookmark-folders", h.listBookmarkFolders)
	group.POST("/bookmark-folders", h.createBookmarkFolder)
	group.DELETE("/bookmark-folders/:id", h.deleteBookmarkFolder)
	group.GET("/posts", h.listPosts)
	group.GET("/recommend/posts", h.listRecommendedPosts)
	group.GET("/posts/drafts", h.getDrafts)
	group.GET("/posts/:id/versions", h.listPostVersions)
	group.POST("/posts/:id/versions/:version/restore", h.restorePostVersion)
	group.GET("/posts/:id", h.getPost)
	group.POST("/posts", canPublish, h.createPost)
	group.PUT("/posts/:id", canPublish, h.updatePost)
	group.DELETE("/posts/:id", canPublish, h.deletePost)
	group.POST("/posts/:id/publish", canPublish, h.publishPost)
	group.POST("/posts/:id/unpublish", canPublish, h.unpublishPost)
	group.POST("/posts/:id/pin", h.pinPost)
	group.POST("/posts/:id/unpin", h.unpinPost)
	group.PUT("/collections/:id/posts/order", h.reorderCollectionPosts)
	group.GET("/drafts", h.getBlogDraft)
	group.PUT("/drafts", canPublish, h.putBlogDraft)
	group.DELETE("/drafts", canPublish, h.deleteBlogDraft)
}
