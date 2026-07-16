package blog

import (
	"atoman/internal/model"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) GetChannel(id uuid.UUID) (model.Channel, error) {
	var channel model.Channel
	err := r.db.Preload("User").First(&channel, "id = ?", id).Error
	return channel, err
}

func (r *Repo) GetPost(id uuid.UUID) (model.Post, error) {
	var post model.Post
	err := r.db.First(&post, "id = ?", id).Error
	return post, err
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

func (r *Repo) ListCollectionsByChannel(channelID uuid.UUID) ([]model.Collection, error) {
	var collections []model.Collection
	err := r.db.Where("channel_id = ?", channelID).Find(&collections).Error
	return collections, err
}

func (r *Repo) GetCollection(id uuid.UUID) (model.Collection, error) {
	var collection model.Collection
	err := r.db.Preload("Channel").First(&collection, "id = ?", id).Error
	return collection, err
}

func (r *Repo) SaveChannel(channel *model.Channel) error { return r.db.Save(channel).Error }

func (r *Repo) DeleteChannel(id uuid.UUID) error {
	return r.db.Delete(&model.Channel{}, "id = ?", id).Error
}

func (r *Repo) CreateCollection(collection *model.Collection) error {
	return r.db.Create(collection).Error
}

func (r *Repo) SaveCollection(collection *model.Collection) error { return r.db.Save(collection).Error }

func (r *Repo) DeleteCollection(id uuid.UUID) error {
	return r.db.Delete(&model.Collection{}, "id = ?", id).Error
}

func (r *Repo) ListUserCollections(userID uuid.UUID) ([]model.Collection, error) {
	var channels []model.Channel
	if err := r.db.Where("user_id = ?", userID).Find(&channels).Error; err != nil {
		return nil, err
	}
	channelIDs := make([]uuid.UUID, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, channel.ID)
	}
	if len(channelIDs) == 0 {
		return []model.Collection{}, nil
	}
	var collections []model.Collection
	err := r.db.Where("channel_id IN ?", channelIDs).Order("created_at DESC").Find(&collections).Error
	return collections, err
}

func (r *Repo) CountPostLikes(postID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.Model(&model.Like{}).Where("target_type = ? AND target_id = ?", "post", postID).Count(&count).Error
	return count, err
}

func (r *Repo) ListBookmarks(userID uuid.UUID, folderID *uuid.UUID, sort string) ([]model.Bookmark, error) {
	var bookmarks []model.Bookmark
	query := r.db.Preload("Post").Preload("Post.User").Where("user_id = ?", userID)
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
			Joins("LEFT JOIN (?) AS post_likes ON post_likes.target_id = bookmarks.post_id", likesSubquery).
			Joins("LEFT JOIN (?) AS post_comments ON post_comments.target_id = bookmarks.post_id", commentsSubquery).
			Order("COALESCE(post_likes.likes_count, 0) DESC").
			Order("COALESCE(post_comments.comments_count, 0) DESC").
			Order("bookmarks.created_at DESC")
	default:
		query = query.Order("bookmarks.created_at DESC")
	}

	err := query.Find(&bookmarks).Error
	return bookmarks, err
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
