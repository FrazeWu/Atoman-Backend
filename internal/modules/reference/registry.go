package reference

import (
	"errors"
	"fmt"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Registry struct {
	db *gorm.DB
}

func NewRegistry(db *gorm.DB) *Registry { return &Registry{db: db} }

func (r *Registry) ResolveUsername(viewer Viewer, username string) (Target, error) {
	_ = viewer
	var user model.User
	if err := r.db.Where("LOWER(username) = LOWER(?) AND is_active = ?", strings.TrimSpace(username), true).First(&user).Error; err != nil {
		return Target{}, targetError(err)
	}
	label := strings.TrimSpace(user.DisplayName)
	if label == "" {
		label = user.Username
	}
	return Target{Type: TargetTypeUser, ID: user.UUID, Label: label, Subtitle: "@" + user.Username, Module: "blog", Path: "/users/" + user.Username, Available: true}, nil
}

func (r *Registry) ResolveUserID(viewer Viewer, id uuid.UUID) (Target, error) {
	_ = viewer
	var user model.User
	if err := r.db.Where("is_active = ?", true).First(&user, "uuid = ?", id).Error; err != nil {
		return Target{}, targetError(err)
	}
	label := strings.TrimSpace(user.DisplayName)
	if label == "" {
		label = user.Username
	}
	return Target{Type: TargetTypeUser, ID: user.UUID, Label: label, Subtitle: "@" + user.Username, Module: "blog", Path: "/users/" + user.Username, Available: true}, nil
}

func (r *Registry) Resolve(viewer Viewer, targetType string, id uuid.UUID) (Target, error) {
	if id == uuid.Nil || !IsSupportedResourceType(targetType) {
		return Target{}, ErrTargetUnavailable
	}
	switch targetType {
	case "post":
		var row struct {
			ID      uuid.UUID `gorm:"column:id"`
			Title   string    `gorm:"column:title"`
			Author  uuid.UUID `gorm:"column:author_id"`
			Status  string    `gorm:"column:status"`
			Visible string    `gorm:"column:visibility"`
		}
		query := r.db.Table("content_entries").Where("kind = ? AND status = ? AND deleted_at IS NULL", "blog", "published")
		if viewer.UserID == uuid.Nil {
			query = query.Where("content_entries.visibility = ?", "public")
		} else {
			query = query.
				Joins("LEFT JOIN posts AS legacy_posts ON legacy_posts.id = content_entries.id AND legacy_posts.deleted_at IS NULL").
				Where("content_entries.visibility = ? OR legacy_posts.user_id = ?", "public", viewer.UserID)
		}
		if err := query.Select("content_entries.id, content_entries.title, content_entries.status, content_entries.visibility").First(&row, "content_entries.id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Title, "blog", "/post/"+row.ID.String()), nil
	case "short_note":
		var row model.ShortNote
		if err := r.db.First(&row, "short_notes.id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Content, "blog", "/posts/notes/"+row.ID.String()), nil
	case "thread":
		var row model.ForumTopic
		if err := r.visibleForumTopics(r.db.Model(&model.ForumTopic{}), viewer).First(&row, "forum_topics.id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Title, "forum", "/topic/"+row.ID.String()), nil
	case "debate":
		var row model.Debate
		if err := r.db.First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Title, "debate", "/"+row.ID.String()), nil
	case "feed":
		var row model.FeedSource
		if err := r.db.Where("hidden = ?", false).First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Title, "feed", "/?source_id="+row.ID.String()), nil
	case "article":
		var row model.FeedItem
		if err := r.db.Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id AND feed_sources.deleted_at IS NULL AND feed_sources.hidden = ?", false).First(&row, "feed_items.id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Title, "feed", "/item/"+row.ID.String()), nil
	case "artist":
		var row model.Artist
		query := visibleMusicWikiEntries(r.db.Model(&model.Artist{}), viewer, "created_by").Where("redirect_to IS NULL")
		if err := query.First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Name, "music", "/artist/"+row.ID.String()), nil
	case "album":
		var row model.Album
		if err := visibleMusicWikiEntries(r.db.Model(&model.Album{}), viewer, "uploaded_by").First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Title, "music", "/album/"+row.ID.String()), nil
	case "song":
		var row model.Song
		if err := visibleMusicWikiEntries(r.db.Model(&model.Song{}), viewer, "uploaded_by").First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		path := "/?song_id=" + row.ID.String()
		if row.AlbumID != nil {
			path = "/album/" + row.AlbumID.String() + "?song_id=" + row.ID.String()
		}
		return target(targetType, row.ID, row.Title, "music", path), nil
	case "playlist":
		var row model.Playlist
		query := r.db.Where("is_public = ?", true)
		if viewer.UserID != uuid.Nil {
			query = r.db.Where("is_public = ? OR user_id = ?", true, viewer.UserID)
		}
		if err := query.First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Name, "music", "/playlist/"+row.ID.String()), nil
	case "podcast":
		var row model.Channel
		query := visiblePodcastChannels(r.db, viewer)
		if err := query.First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Name, "podcast", "/show/"+row.Slug), nil
	case "episode":
		var row model.PodcastEpisode
		if err := visiblePodcastEpisodes(r.db.Preload("Post"), viewer).First(&row, "podcast_episodes.id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Post.Title, "podcast", "/episode/"+row.ID.String()), nil
	case "video":
		var row model.Video
		query := visibleOwned(r.db.Where("status = ?", "published"), viewer, "user_id", "visibility")
		if err := query.First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Title, "video", "/videos/watch/"+row.ID.String()), nil
	case "person":
		var row model.TimelinePerson
		query := visiblePublicOwned(r.db, viewer)
		if err := query.First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Name, "timeline", "/person/"+row.ID.String()), nil
	case "event":
		var row model.TimelineEvent
		query := visiblePublicOwned(r.db, viewer)
		if err := query.First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Title, "timeline", "/?event="+row.ID.String()), nil
	case "channel":
		var row model.Channel
		if err := r.db.First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Name, "blog", "/channel/"+row.Slug), nil
	case "collection":
		var row model.ContentCollection
		if err := r.db.First(&row, "id = ?", id).Error; err != nil {
			return Target{}, targetError(err)
		}
		return target(targetType, row.ID, row.Name, "blog", "/collection/"+row.ID.String()), nil
	case "comment":
		return r.resolveComment(viewer, id)
	default:
		return Target{}, ErrTargetUnavailable
	}
}

func (r *Registry) Search(viewer Viewer, targetType, query string, limit int) ([]Target, error) {
	if limit < 1 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}
	if targetType == TargetTypeUser {
		return r.searchUserTargets(query, limit)
	}
	if !IsSupportedResourceType(targetType) {
		return nil, ErrTargetUnavailable
	}
	if targetType == "comment" {
		ids, err := r.searchResourceIDs(viewer, targetType, strings.TrimSpace(query), limit)
		if err != nil {
			return nil, err
		}
		items := make([]Target, 0, len(ids))
		for _, id := range ids {
			resolved, err := r.Resolve(viewer, targetType, id)
			if err == nil {
				items = append(items, resolved)
			}
		}
		return items, nil
	}
	return r.searchResourceTargets(viewer, targetType, strings.TrimSpace(query), limit)
}

type searchTargetRow struct {
	ID          uuid.UUID  `gorm:"column:id"`
	Label       string     `gorm:"column:label"`
	Username    string     `gorm:"column:username"`
	Slug        string     `gorm:"column:slug"`
	ContentType string     `gorm:"column:content_type"`
	AlbumID     *uuid.UUID `gorm:"column:album_id"`
}

func (r *Registry) searchUserTargets(query string, limit int) ([]Target, error) {
	like := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	var rows []searchTargetRow
	if err := r.db.Model(&model.User{}).
		Select("uuid AS id, display_name AS label, username").
		Where("is_active = ? AND (LOWER(username) LIKE ? OR LOWER(display_name) LIKE ?)", true, like, like).
		Order("LOWER(username) ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]Target, 0, len(rows))
	for _, row := range rows {
		label := strings.TrimSpace(row.Label)
		if label == "" {
			label = row.Username
		}
		items = append(items, target(TargetTypeUser, row.ID, label, "blog", "/users/"+row.Username))
		items[len(items)-1].Subtitle = "@" + row.Username
	}
	return items, nil
}

func (r *Registry) searchResourceTargets(viewer Viewer, targetType, search string, limit int) ([]Target, error) {
	like := "%" + strings.ToLower(search) + "%"
	var query *gorm.DB
	switch targetType {
	case "post":
		query = r.db.Table("content_entries AS posts").
			Where("posts.kind = ? AND posts.status = ? AND posts.deleted_at IS NULL AND LOWER(posts.title) LIKE ?", "blog", "published", like).
			Select("posts.id, posts.title AS label")
		if viewer.UserID == uuid.Nil {
			query = query.Where("posts.visibility = ?", "public")
		} else {
			query = query.
				Joins("LEFT JOIN posts AS legacy_posts ON legacy_posts.id = posts.id AND legacy_posts.deleted_at IS NULL").
				Where("posts.visibility = ? OR legacy_posts.user_id = ?", "public", viewer.UserID)
		}
	case "short_note":
		query = r.db.Model(&model.ShortNote{}).
			Where("LOWER(short_notes.content) LIKE ?", like).
			Select("short_notes.id, short_notes.content AS label")
	case "thread":
		query = r.visibleForumTopics(r.db.Model(&model.ForumTopic{}), viewer).
			Where("LOWER(forum_topics.title) LIKE ?", like).
			Select("forum_topics.id, forum_topics.title AS label")
	case "debate":
		query = r.db.Model(&model.Debate{}).
			Where("LOWER(debates.title) LIKE ?", like).
			Select("debates.id, debates.title AS label")
	case "feed":
		query = r.db.Model(&model.FeedSource{}).
			Where("feed_sources.hidden = ? AND LOWER(feed_sources.title) LIKE ?", false, like).
			Select("feed_sources.id, feed_sources.title AS label")
	case "article":
		query = r.db.Model(&model.FeedItem{}).
			Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id AND feed_sources.deleted_at IS NULL AND feed_sources.hidden = ?", false).
			Where("LOWER(feed_items.title) LIKE ?", like).
			Select("feed_items.id, feed_items.title AS label")
	case "artist":
		query = visibleMusicWikiEntries(r.db.Model(&model.Artist{}), viewer, "created_by").
			Where("redirect_to IS NULL AND LOWER(name) LIKE ?", like).
			Select("id, name AS label")
	case "album":
		query = visibleMusicWikiEntries(r.db.Model(&model.Album{}), viewer, "uploaded_by").
			Where("LOWER(title) LIKE ?", like).
			Select("id, title AS label")
	case "song":
		query = visibleMusicWikiEntries(r.db.Model(&model.Song{}), viewer, "uploaded_by").
			Where("LOWER(title) LIKE ?", like).
			Select("id, title AS label, album_id")
	case "playlist":
		query = r.db.Model(&model.Playlist{}).
			Where("LOWER(music_playlists.name) LIKE ?", like)
		if viewer.UserID == uuid.Nil {
			query = query.Where("music_playlists.is_public = ?", true)
		} else {
			query = query.Where("music_playlists.is_public = ? OR music_playlists.user_id = ?", true, viewer.UserID)
		}
		query = query.Select("music_playlists.id, music_playlists.name AS label")
	case "podcast":
		query = visiblePodcastChannels(r.db.Model(&model.Channel{}).Where("LOWER(channels.name) LIKE ?", like), viewer).
			Select("channels.id, channels.name AS label, channels.slug")
	case "episode":
		query = visiblePodcastEpisodes(r.db.Model(&model.PodcastEpisode{}), viewer).
			Where("LOWER(posts.title) LIKE ?", like).
			Select("podcast_episodes.id, posts.title AS label")
	case "video":
		query = visibleOwned(r.db.Model(&model.Video{}).Where("videos.status = ? AND LOWER(videos.title) LIKE ?", "published", like), viewer, "videos.user_id", "videos.visibility").
			Select("videos.id, videos.title AS label")
	case "person":
		query = visiblePublicOwned(r.db.Model(&model.TimelinePerson{}).Where("LOWER(timeline_persons.name) LIKE ?", like), viewer).
			Select("timeline_persons.id, timeline_persons.name AS label")
	case "event":
		query = visiblePublicOwned(r.db.Model(&model.TimelineEvent{}).Where("LOWER(timeline_events.title) LIKE ?", like), viewer).
			Select("timeline_events.id, timeline_events.title AS label")
	case "channel":
		query = r.db.Model(&model.Channel{}).
			Where("LOWER(channels.name) LIKE ? OR LOWER(channels.slug) LIKE ?", like, like).
			Select("channels.id, channels.name AS label, channels.slug")
	case "collection":
		query = r.db.Model(&model.ContentCollection{}).
			Where("LOWER(content_collections.name) LIKE ?", like).
			Select("content_collections.id, content_collections.name AS label")
	default:
		return nil, ErrTargetUnavailable
	}

	var orderColumn string
	switch targetType {
	case "article":
		orderColumn = "feed_items.created_at"
	case "episode":
		orderColumn = "podcast_episodes.created_at"
	default:
		orderColumn = "created_at"
	}
	var rows []searchTargetRow
	if err := query.Order(orderColumn + " DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]Target, 0, len(rows))
	for _, row := range rows {
		module, path := searchTargetLocation(targetType, row)
		items = append(items, target(targetType, row.ID, row.Label, module, path))
	}
	return items, nil
}

func searchTargetLocation(targetType string, row searchTargetRow) (string, string) {
	switch targetType {
	case "post":
		return "blog", "/post/" + row.ID.String()
	case "short_note":
		return "blog", "/posts/notes/" + row.ID.String()
	case "thread":
		return "forum", "/topic/" + row.ID.String()
	case "debate":
		return "debate", "/" + row.ID.String()
	case "feed":
		return "feed", "/?source_id=" + row.ID.String()
	case "article":
		return "feed", "/item/" + row.ID.String()
	case "artist", "album", "song", "playlist":
		module := "music"
		path := "/" + targetType + "/" + row.ID.String()
		if targetType == "song" {
			path = "/?song_id=" + row.ID.String()
			if row.AlbumID != nil {
				path = "/album/" + row.AlbumID.String() + "?song_id=" + row.ID.String()
			}
		}
		return module, path
	case "podcast":
		return "podcast", "/show/" + row.Slug
	case "episode":
		return "podcast", "/episode/" + row.ID.String()
	case "video":
		return "video", "/videos/watch/" + row.ID.String()
	case "person":
		return "timeline", "/person/" + row.ID.String()
	case "event":
		return "timeline", "/?event=" + row.ID.String()
	case "channel":
		return "blog", "/channel/" + row.Slug
	case "collection":
		return collectionTarget(model.Collection{Base: model.Base{ID: row.ID}, ContentType: row.ContentType})
	default:
		return "", ""
	}
}

func visibleMusicWikiEntries(db *gorm.DB, viewer Viewer, ownerColumn string) *gorm.DB {
	if viewer.UserID != uuid.Nil {
		return db.Where("(lifecycle_status = ? OR (lifecycle_status = ? AND "+ownerColumn+" = ?))", model.MusicLifecycleActive, model.MusicLifecycleDraft, viewer.UserID)
	}
	return db.Where("lifecycle_status = ?", model.MusicLifecycleActive)
}

func (r *Registry) searchResourceIDs(viewer Viewer, targetType, search string, limit int) ([]uuid.UUID, error) {
	like := "%" + strings.ToLower(search) + "%"
	var query *gorm.DB
	idColumn := "id"
	createdAtColumn := "created_at"
	switch targetType {
	case "post":
		query = r.db.Table("content_entries").Where("kind = ? AND status = ? AND deleted_at IS NULL AND LOWER(title) LIKE ?", "blog", "published", like)
		if viewer.UserID == uuid.Nil {
			query = query.Where("visibility = ?", "public")
		} else {
			query = query.
				Joins("LEFT JOIN posts AS legacy_posts ON legacy_posts.id = content_entries.id AND legacy_posts.deleted_at IS NULL").
				Where("content_entries.visibility = ? OR legacy_posts.user_id = ?", "public", viewer.UserID)
		}
		idColumn = "content_entries.id"
		createdAtColumn = "content_entries.created_at"
	case "short_note":
		query = r.db.Model(&model.ShortNote{}).Where("LOWER(content) LIKE ?", like)
	case "thread":
		query = r.visibleForumTopics(r.db.Model(&model.ForumTopic{}), viewer).Where("LOWER(title) LIKE ?", like)
	case "debate":
		query = r.db.Model(&model.Debate{}).Where("LOWER(title) LIKE ?", like)
	case "feed":
		query = r.db.Model(&model.FeedSource{}).Where("hidden = ? AND LOWER(title) LIKE ?", false, like)
	case "article":
		query = r.db.Model(&model.FeedItem{}).Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id AND feed_sources.deleted_at IS NULL AND feed_sources.hidden = ?", false).Where("LOWER(feed_items.title) LIKE ?", like)
		idColumn = "feed_items.id"
		createdAtColumn = "feed_items.created_at"
	case "artist":
		query = visibleMusicWikiEntries(r.db.Model(&model.Artist{}), viewer, "created_by").Where("redirect_to IS NULL AND LOWER(name) LIKE ?", like)
	case "album":
		query = visibleMusicWikiEntries(r.db.Model(&model.Album{}), viewer, "uploaded_by").Where("LOWER(title) LIKE ?", like)
	case "song":
		query = visibleMusicWikiEntries(r.db.Model(&model.Song{}), viewer, "uploaded_by").Where("LOWER(title) LIKE ?", like)
	case "playlist":
		query = r.db.Model(&model.Playlist{}).Where("LOWER(name) LIKE ?", like).Where("is_public = ?", true)
		if viewer.UserID != uuid.Nil {
			query = r.db.Model(&model.Playlist{}).Where("LOWER(name) LIKE ?", like).Where("is_public = ? OR user_id = ?", true, viewer.UserID)
		}
	case "podcast":
		query = visiblePodcastChannels(r.db.Model(&model.Channel{}).Where("LOWER(name) LIKE ?", like), viewer)
	case "episode":
		query = visiblePodcastEpisodes(r.db.Model(&model.PodcastEpisode{}), viewer).Where("LOWER(posts.title) LIKE ?", like)
		idColumn = "podcast_episodes.id"
		createdAtColumn = "podcast_episodes.created_at"
	case "video":
		query = visibleOwned(r.db.Model(&model.Video{}).Where("status = ? AND LOWER(title) LIKE ?", "published", like), viewer, "user_id", "visibility")
	case "person":
		query = visiblePublicOwned(r.db.Model(&model.TimelinePerson{}).Where("LOWER(name) LIKE ?", like), viewer)
	case "event":
		query = visiblePublicOwned(r.db.Model(&model.TimelineEvent{}).Where("LOWER(title) LIKE ?", like), viewer)
	case "channel":
		query = r.db.Model(&model.Channel{}).Where("LOWER(name) LIKE ? OR LOWER(slug) LIKE ?", like, like)
	case "collection":
		query = r.db.Model(&model.ContentCollection{}).Where("LOWER(name) LIKE ?", like)
		idColumn = "content_collections.id"
		createdAtColumn = "content_collections.created_at"
	case "comment":
		query = r.db.Model(&model.CommentEntry{}).Where("status = ? AND LOWER(content) LIKE ?", "active", like)
	}
	if query == nil {
		return nil, ErrTargetUnavailable
	}
	var ids []uuid.UUID
	if err := query.Order(createdAtColumn+" DESC").Limit(limit).Pluck(idColumn, &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *Registry) resolveComment(viewer Viewer, id uuid.UUID) (Target, error) {
	var row model.CommentEntry
	if err := r.db.Where("status IN ?", []string{"active", "auto_folded"}).First(&row, "id = ?", id).Error; err != nil {
		return Target{}, targetError(err)
	}
	var discussion model.DiscussionTarget
	if err := r.db.First(&discussion, "id = ?", row.TargetID).Error; err != nil {
		return Target{}, targetError(err)
	}
	targetType, ok := referenceTypeForDiscussionKind(discussion.Kind)
	if !ok {
		return Target{}, ErrTargetUnavailable
	}
	parent, err := r.Resolve(viewer, targetType, discussion.ResourceID)
	if err != nil {
		return Target{}, err
	}
	path := parent.Path
	rootID := row.ID
	if row.RootID != nil {
		rootID = *row.RootID
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	path += separator + "comment_id=" + row.ID.String() + "#comment-" + rootID.String()
	return target("comment", row.ID, truncatedLabel(row.Content), parent.Module, path), nil
}

func referenceTypeForDiscussionKind(kind string) (string, bool) {
	switch kind {
	case "blog_post":
		return "post", true
	case "short_note":
		return "short_note", true
	case "forum_topic":
		return "thread", true
	case "debate":
		return "debate", true
	case "feed_article":
		return "article", true
	case "music_artist":
		return "artist", true
	case "music_album":
		return "album", true
	case "music_song":
		return "song", true
	case "podcast_episode":
		return "episode", true
	case "video":
		return "video", true
	case "timeline_person":
		return "person", true
	case "timeline_event":
		return "event", true
	default:
		return "", false
	}
}

func visibleOwned(query *gorm.DB, viewer Viewer, ownerColumn, visibilityColumn string) *gorm.DB {
	if viewer.UserID == uuid.Nil {
		return query.Where(visibilityColumn+" = ?", "public")
	}
	return query.Where("("+visibilityColumn+" = ? OR "+ownerColumn+" = ?)", "public", viewer.UserID)
}

func visiblePublicOwned(query *gorm.DB, viewer Viewer) *gorm.DB {
	if viewer.UserID == uuid.Nil {
		return query.Where("is_public = ?", true)
	}
	return query.Where("is_public = ? OR user_id = ?", true, viewer.UserID)
}

func visiblePodcastChannels(query *gorm.DB, viewer Viewer) *gorm.DB {
	visibility := "p.visibility = ?"
	args := []interface{}{"public"}
	if viewer.UserID != uuid.Nil {
		visibility = "(p.visibility = ? OR p.user_id = ?)"
		args = append(args, viewer.UserID)
	}
	return query.Where(`EXISTS (
		SELECT 1 FROM podcast_episodes pe
		JOIN posts p ON p.id = pe.post_id AND p.deleted_at IS NULL AND p.status = 'published'
		WHERE pe.channel_id = channels.id AND pe.deleted_at IS NULL AND `+visibility+`
	)`, args...)
}

func visiblePodcastEpisodes(query *gorm.DB, viewer Viewer) *gorm.DB {
	query = query.Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.deleted_at IS NULL AND posts.status = ?", "published")
	if viewer.UserID == uuid.Nil {
		return query.Where("posts.visibility = ?", "public")
	}
	return query.Where("(posts.visibility = ? OR posts.user_id = ?)", "public", viewer.UserID)
}

func (r *Registry) visibleForumTopics(query *gorm.DB, viewer Viewer) *gorm.DB {
	if viewer.UserID != uuid.Nil {
		var user model.User
		if err := r.db.Select("role").First(&user, "uuid = ?", viewer.UserID).Error; err == nil && (user.Role == "admin" || user.Role == "owner") {
			return query
		}
	}
	base := `NOT EXISTS (
		SELECT 1 FROM forum_category_permissions fcp
		WHERE fcp.category_id = forum_topics.category_id AND fcp.deleted_at IS NULL
	)`
	if viewer.UserID == uuid.Nil {
		return query.Where(base)
	}
	allowed := `EXISTS (
		SELECT 1 FROM forum_category_permissions fcp
		JOIN forum_group_members fgm ON fgm.group_id = fcp.group_id AND fgm.deleted_at IS NULL
		WHERE fcp.category_id = forum_topics.category_id AND fcp.can_view = ?
			AND fcp.deleted_at IS NULL AND fgm.user_id = ?
	)`
	return query.Where("("+base+" OR "+allowed+")", true, viewer.UserID)
}

func collectionTarget(row model.Collection) (string, string) {
	switch row.ContentType {
	case "video":
		return "video", "/?collection_id=" + row.ID.String()
	case "podcast":
		return "podcast", "/?collection_id=" + row.ID.String()
	default:
		return "blog", "/collection/" + row.ID.String()
	}
}

func target(targetType string, id uuid.UUID, label, module, path string) Target {
	return Target{Type: targetType, ID: id, Label: strings.TrimSpace(label), Module: module, Path: path, Available: true}
}

func targetError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrTargetUnavailable
	}
	return err
}

func truncatedLabel(content string) string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= 60 {
		return string(runes)
	}
	return fmt.Sprintf("%s...", string(runes[:60]))
}
