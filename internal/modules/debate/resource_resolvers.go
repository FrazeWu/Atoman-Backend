package debate

import (
	"context"
	"strings"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/resourceref"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func NewResourceRegistry(db *gorm.DB) *resourceref.Registry {
	registry := resourceref.NewRegistry()
	register := func(kind string, resolver resourceref.Resolver) {
		if err := registry.Register(kind, resolver); err != nil {
			panic(err)
		}
	}
	register(resourceref.KindPost, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.Post
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindPost, id, item.Title), err == nil && item.Status == "published" && item.Visibility == "public"), err
	})
	register(resourceref.KindThread, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.ForumTopic
		if err := db.First(&item, "id = ?", id).Error; err != nil {
			return resourceref.Resolved{}, err
		}
		var restricted int64
		err := db.Model(&model.ForumCategoryPermission{}).Where("category_id = ?", item.CategoryID).Count(&restricted).Error
		return resolved(kindTitle(resourceref.KindThread, id, item.Title), err == nil && restricted == 0), err
	})
	register(resourceref.KindDebate, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.Debate
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindDebate, id, item.Title), err == nil), err
	})
	register(resourceref.KindFeed, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.FeedSource
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindFeed, id, item.Title), err == nil && !item.Hidden), err
	})
	register(resourceref.KindArticle, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.FeedItem
		err := db.Preload("FeedSource").First(&item, "id = ?", id).Error
		visible := err == nil && item.FeedSource != nil && !item.FeedSource.Hidden
		return resolved(kindTitle(resourceref.KindArticle, id, item.Title), visible), err
	})
	register(resourceref.KindArtist, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.Artist
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindArtist, id, item.Name), err == nil && item.EntryStatus != "closed"), err
	})
	register(resourceref.KindAlbum, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.Album
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindAlbum, id, item.Title), err == nil && item.EntryStatus != "closed" && item.Status != "closed"), err
	})
	register(resourceref.KindSong, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.Song
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindSong, id, item.Title), err == nil && item.Status != "closed"), err
	})
	register(resourceref.KindPlaylist, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.Playlist
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindPlaylist, id, item.Name), err == nil && item.IsPublic), err
	})
	// A podcast show is represented by its Channel; PodcastEpisode is the
	// one-to-one extension used by individual episode references.
	register(resourceref.KindPodcast, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.Channel
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindPodcast, id, item.Name), err == nil), err
	})
	register(resourceref.KindEpisode, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.PodcastEpisode
		err := db.Preload("Post").First(&item, "id = ?", id).Error
		visible := err == nil && item.Post != nil && item.Post.Status == "published" && item.Post.Visibility == "public"
		title := ""
		if item.Post != nil {
			title = item.Post.Title
		}
		return resolved(kindTitle(resourceref.KindEpisode, id, title), visible), err
	})
	register(resourceref.KindVideo, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.Video
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindVideo, id, item.Title), err == nil && item.Status == "published" && item.Visibility == "public"), err
	})
	register(resourceref.KindPerson, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.TimelinePerson
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindPerson, id, item.Name), err == nil && item.IsPublic), err
	})
	register(resourceref.KindEvent, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.TimelineEvent
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindEvent, id, item.Title), err == nil && item.IsPublic), err
	})
	register(resourceref.KindChannel, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.Channel
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindChannel, id, item.Name), err == nil), err
	})
	register(resourceref.KindCollection, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.Collection
		err := db.First(&item, "id = ?", id).Error
		return resolved(kindTitle(resourceref.KindCollection, id, item.Name), err == nil), err
	})
	register(resourceref.KindComment, func(_ context.Context, _ resourceref.Viewer, id uuid.UUID) (resourceref.Resolved, error) {
		var item model.CommentEntry
		if err := db.First(&item, "id = ? AND status IN ?", id, []string{"active", "auto_folded"}).Error; err != nil {
			return resourceref.Resolved{}, err
		}
		var target model.DiscussionTarget
		if err := db.First(&target, "id = ?", item.TargetID).Error; err != nil {
			return resourceref.Resolved{}, err
		}
		visible, err := publicCommentTargetVisible(db, target)
		if err != nil {
			return resourceref.Resolved{}, err
		}
		return resolved(kindTitle(resourceref.KindComment, id, conciseTitle(item.Content, 80)), visible), nil
	})
	return registry
}

func resolved(item resourceref.Resolved, visible bool) resourceref.Resolved {
	item.Visible, item.Referenceable = visible, visible
	return item
}

func kindTitle(kind string, id uuid.UUID, title string) resourceref.Resolved {
	return resourceref.Resolved{Kind: kind, ID: id, Title: strings.TrimSpace(title)}
}

func publicCommentTargetVisible(db *gorm.DB, target model.DiscussionTarget) (bool, error) {
	switch target.Kind {
	case "blog_post":
		var item model.Post
		err := db.First(&item, "id = ?", target.ResourceID).Error
		return err == nil && item.Status == "published" && item.Visibility == "public", err
	case "video":
		var item model.Video
		err := db.First(&item, "id = ?", target.ResourceID).Error
		return err == nil && item.Status == "published" && item.Visibility == "public", err
	case "podcast_episode":
		var item model.PodcastEpisode
		err := db.Preload("Post").First(&item, "id = ?", target.ResourceID).Error
		return err == nil && item.Post != nil && item.Post.Status == "published" && item.Post.Visibility == "public", err
	case "feed_article":
		var item model.FeedItem
		err := db.Preload("FeedSource").First(&item, "id = ?", target.ResourceID).Error
		return err == nil && item.FeedSource != nil && !item.FeedSource.Hidden, err
	case "music_artist":
		var item model.Artist
		err := db.First(&item, "id = ?", target.ResourceID).Error
		return err == nil && item.EntryStatus != "closed", err
	case "music_album":
		var item model.Album
		err := db.First(&item, "id = ?", target.ResourceID).Error
		return err == nil && item.EntryStatus != "closed" && item.Status != "closed", err
	case "music_song":
		var item model.Song
		err := db.First(&item, "id = ?", target.ResourceID).Error
		return err == nil && item.Status != "closed", err
	case "forum_topic":
		var item model.ForumTopic
		if err := db.First(&item, "id = ?", target.ResourceID).Error; err != nil {
			return false, err
		}
		var restricted int64
		err := db.Model(&model.ForumCategoryPermission{}).Where("category_id = ?", item.CategoryID).Count(&restricted).Error
		return err == nil && restricted == 0, err
	case "debate":
		var count int64
		err := db.Model(&model.Debate{}).Where("id = ?", target.ResourceID).Count(&count).Error
		return count > 0, err
	case "timeline_event":
		var item model.TimelineEvent
		err := db.First(&item, "id = ?", target.ResourceID).Error
		return err == nil && item.IsPublic, err
	case "timeline_person":
		var item model.TimelinePerson
		err := db.First(&item, "id = ?", target.ResourceID).Error
		return err == nil && item.IsPublic, err
	default:
		return false, gorm.ErrRecordNotFound
	}
}

func conciseTitle(content string, limit int) string {
	content = strings.TrimSpace(content)
	if utf8.RuneCountInString(content) <= limit {
		return content
	}
	runes := []rune(content)
	return string(runes[:limit])
}
