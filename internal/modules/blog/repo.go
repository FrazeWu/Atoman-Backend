package blog

import (
	"atoman/internal/model"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

type sitemapPostRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	Title     string
	Content   string
	Summary   string
}

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) GetChannel(id uuid.UUID) (model.Channel, error) {
	var channel model.Channel
	err := r.db.Preload("User").First(&channel, "id = ?", id).Error
	return channel, err
}

func (r *Repo) GetBlogContent(id uuid.UUID) (BlogContent, error) {
	return loadCanonicalBlogContent(r.db, id)
}

func (r *Repo) GetPublicPublishedPost(id uuid.UUID) (BlogContent, error) {
	contents, err := LoadCanonicalBlogContents(r.db, canonicalBlogPostsQuery(r.db).
		Where("posts.status = ? AND (posts.visibility = ? OR posts.visibility = ?)", "published", "", "public").
		Where("posts.id = ?", id))
	if err != nil {
		return BlogContent{}, err
	}
	if len(contents) == 0 {
		return BlogContent{}, gorm.ErrRecordNotFound
	}
	return contents[0], nil
}

func (r *Repo) ListPublicPublishedPosts() ([]sitemapPostRow, error) {
	posts := make([]sitemapPostRow, 0)
	err := r.db.Table("content_entries AS posts").
		Select("posts.id, posts.updated_at").
		Where("posts.kind = ? AND posts.deleted_at IS NULL", "blog").
		Where("posts.status = ? AND (posts.visibility = ? OR posts.visibility = ?)", "published", "", "public").
		Order("COALESCE(posts.published_at, posts.created_at) DESC").Order("posts.created_at DESC").Order("posts.id DESC").
		Scan(&posts).Error
	return posts, err
}

func (r *Repo) ListChannels(userID *uuid.UUID) ([]model.Channel, error) {
	var channels []model.Channel
	query := r.db.Preload("User")
	if userID != nil && *userID != uuid.Nil {
		query = query.Where("user_id = ?", *userID)
	}
	err := query.Find(&channels).Error
	return channels, err
}

func (r *Repo) GetChannelBySlug(slug string) (model.Channel, error) {
	var channel model.Channel
	err := r.db.Preload("User").First(&channel, "slug = ?", slug).Error
	return channel, err
}

func (r *Repo) ListCollectionsByChannel(channelID uuid.UUID) ([]BlogCollection, error) {
	var collections []model.ContentCollection
	if err := r.db.Where("channel_id = ?", channelID).Order("created_at ASC, id ASC").Find(&collections).Error; err != nil {
		return nil, err
	}
	result := make([]BlogCollection, 0, len(collections))
	for _, collection := range collections {
		result = append(result, blogCollectionFromContentCollection(collection))
	}
	return result, nil
}

func (r *Repo) GetCollection(id uuid.UUID) (BlogCollection, error) {
	var collection model.ContentCollection
	if err := r.db.Preload("Channel").First(&collection, "id = ?", id).Error; err != nil {
		return BlogCollection{}, err
	}
	return blogCollectionFromContentCollection(collection), nil
}

func (r *Repo) SaveChannel(channel *model.Channel) error { return r.db.Save(channel).Error }

func (r *Repo) DeleteChannel(id uuid.UUID) error {
	return r.db.Delete(&model.Channel{}, "id = ?", id).Error
}

func (r *Repo) CreateCollection(collection *model.ContentCollection) error {
	return r.db.Create(collection).Error
}

func (r *Repo) SaveCollection(collection *model.ContentCollection) error {
	return r.db.Save(collection).Error
}

func (r *Repo) DeleteCollection(id uuid.UUID) error {
	return r.db.Delete(&model.ContentCollection{}, "id = ?", id).Error
}

func (r *Repo) ListUserCollections(userID uuid.UUID) ([]BlogCollection, error) {
	var channels []model.Channel
	if err := r.db.Where("user_id = ?", userID).Find(&channels).Error; err != nil {
		return nil, err
	}
	channelIDs := make([]uuid.UUID, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, channel.ID)
	}
	if len(channelIDs) == 0 {
		return []BlogCollection{}, nil
	}
	var collections []model.ContentCollection
	if err := r.db.Where("channel_id IN ?", channelIDs).Order("created_at DESC").Find(&collections).Error; err != nil {
		return nil, err
	}
	result := make([]BlogCollection, 0, len(collections))
	for _, collection := range collections {
		result = append(result, blogCollectionFromContentCollection(collection))
	}
	return result, nil
}

func (r *Repo) CountPostLikes(postID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.Model(&model.Like{}).Where("target_type = ? AND target_id = ?", "post", postID).Count(&count).Error
	return count, err
}

func (r *Repo) ListBookmarks(userID uuid.UUID, folderID *uuid.UUID, sort string) ([]model.Bookmark, error) {
	var bookmarks []model.Bookmark
	query := r.db.Where("user_id = ?", userID)
	if folderID != nil && *folderID != uuid.Nil {
		query = query.Where("bookmark_folder_id = ?", *folderID)
	}

	switch strings.ToLower(strings.TrimSpace(sort)) {
	case "popular":
		likesSubquery := r.db.
			Table("likes").
			Select("target_id, COUNT(*) AS likes_count").
			Where("target_type = ? AND deleted_at IS NULL", "post").
			Group("target_id")
		commentsSubquery := r.db.
			Table("discussion_targets").
			Select("resource_id AS target_id, comment_count AS comments_count").
			Where("kind = ? AND deleted_at IS NULL", "blog_post")
		query = query.
			Joins("LEFT JOIN (?) AS post_likes ON post_likes.target_id = bookmarks.content_id", likesSubquery).
			Joins("LEFT JOIN (?) AS post_comments ON post_comments.target_id = bookmarks.content_id", commentsSubquery).
			Order("COALESCE(post_likes.likes_count, 0) DESC").
			Order("COALESCE(post_comments.comments_count, 0) DESC").
			Order("bookmarks.created_at DESC")
	default:
		query = query.Order("bookmarks.created_at DESC")
	}

	err := query.Find(&bookmarks).Error
	if err != nil {
		return nil, err
	}
	if len(bookmarks) == 0 {
		return bookmarks, nil
	}
	return bookmarks, nil
}

func (r *Repo) CreateBookmark(bookmark *model.Bookmark) error {
	return r.db.Create(bookmark).Error
}

func (r *Repo) GetBookmark(id uuid.UUID) (model.Bookmark, error) {
	var bookmark model.Bookmark
	err := r.db.First(&bookmark, "id = ?", id).Error
	return bookmark, err
}

func (r *Repo) DeleteBookmark(id uuid.UUID, userID uuid.UUID) error {
	return r.db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Bookmark{}).Error
}

func (r *Repo) ListBookmarkFolders(userID uuid.UUID) ([]model.BookmarkFolder, error) {
	var folders []model.BookmarkFolder
	err := r.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&folders).Error
	return folders, err
}

func (r *Repo) CreateBookmarkFolder(folder *model.BookmarkFolder) error {
	return r.db.Create(folder).Error
}

func (r *Repo) DeleteBookmarkFolder(id uuid.UUID, userID uuid.UUID) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		fallback := model.BookmarkFolder{UserID: userID, Name: "默认收藏夹"}
		if err := tx.Where("user_id = ? AND name = ?", userID, fallback.Name).FirstOrCreate(&fallback).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Bookmark{}).Where("bookmark_folder_id = ? AND user_id = ?", id, userID).UpdateColumn("bookmark_folder_id", fallback.ID).Error; err != nil {
			return err
		}
		return tx.Where("id = ? AND user_id = ?", id, userID).Delete(&model.BookmarkFolder{}).Error
	})
}
