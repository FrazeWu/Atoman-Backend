package reference

import (
	"fmt"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRegistryResolvesEveryResourceType(t *testing.T) {
	db, ids := seedReferenceRegistry(t)
	registry := NewRegistry(db)
	expected := map[string]struct {
		label  string
		module string
	}{
		"post": {"Blog Post", "blog"}, "thread": {"Forum Thread", "forum"}, "debate": {"Debate Topic", "debate"},
		"feed": {"External Feed", "feed"}, "article": {"Feed Article", "feed"},
		"artist": {"Artist Name", "music"}, "album": {"Album Title", "music"}, "song": {"Song Title", "music"},
		"playlist": {"Playlist Name", "music"}, "podcast": {"Podcast Show", "podcast"}, "episode": {"Podcast Episode", "podcast"},
		"video": {"Video Title", "video"}, "person": {"Person Name", "timeline"}, "event": {"Event Title", "timeline"},
		"channel": {"Podcast Show", "blog"}, "collection": {"Collection Name", "blog"}, "comment": {"Comment body", "blog"},
	}

	for _, targetType := range SupportedResourceTypes {
		t.Run(targetType, func(t *testing.T) {
			got, err := registry.Resolve(Viewer{}, targetType, ids[targetType])
			require.NoError(t, err)
			require.Equal(t, targetType, got.Type)
			require.Equal(t, ids[targetType], got.ID)
			require.Equal(t, expected[targetType].label, got.Label)
			require.Equal(t, expected[targetType].module, got.Module)
			require.NotEmpty(t, got.Path)
			require.True(t, got.Available)
		})
	}
}

func TestRegistryResolvesOnlyActiveUsers(t *testing.T) {
	db, _ := seedReferenceRegistry(t)
	registry := NewRegistry(db)

	active, err := registry.ResolveUsername(Viewer{}, "alice")
	require.NoError(t, err)
	require.Equal(t, "Alice", active.Label)
	require.Equal(t, "/users/alice", active.Path)

	inactive := model.User{Username: "inactive", Email: "inactive@example.com", Password: "hash", IsActive: false}
	require.NoError(t, db.Create(&inactive).Error)
	require.NoError(t, db.Model(&inactive).Update("is_active", false).Error)
	_, err = registry.ResolveUsername(Viewer{}, "inactive")
	require.ErrorIs(t, err, ErrTargetUnavailable)
}

func TestRegistrySearchHonorsVisibilityAndLimit(t *testing.T) {
	db, ids := seedReferenceRegistry(t)
	registry := NewRegistry(db)
	owner := model.User{Username: "owner", Email: "owner@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	private := model.Playlist{UserID: owner.UUID, Name: "Private Mix", IsPublic: false}
	require.NoError(t, db.Create(&private).Error)

	guestItems, err := registry.Search(Viewer{}, "playlist", "Playlist", 1)
	require.NoError(t, err)
	require.Len(t, guestItems, 1)
	require.Equal(t, ids["playlist"], guestItems[0].ID)

	ownerItems, err := registry.Search(Viewer{UserID: owner.UUID}, "playlist", "Mix", 10)
	require.NoError(t, err)
	require.Len(t, ownerItems, 1)
	require.Equal(t, private.ID, ownerItems[0].ID)
}

func TestRegistrySearchesVisibleFeedArticlesWithQualifiedColumns(t *testing.T) {
	db, ids := seedReferenceRegistry(t)
	items, err := NewRegistry(db).Search(Viewer{}, "article", "Feed", 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, ids["article"], items[0].ID)
}

func TestRegistryThreadResolutionHonorsForumCategoryVisibility(t *testing.T) {
	db, _ := seedReferenceRegistry(t)
	registry := NewRegistry(db)
	var member model.User
	require.NoError(t, db.Where("username = ?", "alice").First(&member).Error)
	category := model.ForumCategory{Name: "Restricted references"}
	require.NoError(t, db.Create(&category).Error)
	thread := model.ForumTopic{UserID: member.UUID, CategoryID: category.ID, Title: "Restricted thread", Content: "body"}
	require.NoError(t, db.Create(&thread).Error)
	group := model.ForumGroup{Name: "Reference readers"}
	require.NoError(t, db.Create(&group).Error)
	require.NoError(t, db.Create(&model.ForumGroupMember{GroupID: group.ID, UserID: member.UUID}).Error)
	require.NoError(t, db.Create(&model.ForumCategoryPermission{CategoryID: category.ID, GroupID: group.ID, CanView: true}).Error)

	_, err := registry.Resolve(Viewer{}, "thread", thread.ID)
	require.ErrorIs(t, err, ErrTargetUnavailable)
	guestSearch, err := registry.Search(Viewer{}, "thread", "Restricted", 10)
	require.NoError(t, err)
	require.Empty(t, guestSearch)

	resolved, err := registry.Resolve(Viewer{UserID: member.UUID}, "thread", thread.ID)
	require.NoError(t, err)
	require.Equal(t, thread.ID, resolved.ID)
	memberSearch, err := registry.Search(Viewer{UserID: member.UUID}, "thread", "Restricted", 10)
	require.NoError(t, err)
	require.Len(t, memberSearch, 1)
	require.Equal(t, thread.ID, memberSearch[0].ID)
}

func TestRegistryCommentResolutionHonorsDiscussionTargetVisibility(t *testing.T) {
	db, _ := seedReferenceRegistry(t)
	registry := NewRegistry(db)
	var member model.User
	require.NoError(t, db.Where("username = ?", "alice").First(&member).Error)
	category := model.ForumCategory{Name: "Private comments"}
	require.NoError(t, db.Create(&category).Error)
	thread := model.ForumTopic{UserID: member.UUID, CategoryID: category.ID, Title: "Private topic", Content: "body"}
	require.NoError(t, db.Create(&thread).Error)
	group := model.ForumGroup{Name: "Private comment readers"}
	require.NoError(t, db.Create(&group).Error)
	require.NoError(t, db.Create(&model.ForumGroupMember{GroupID: group.ID, UserID: member.UUID}).Error)
	require.NoError(t, db.Create(&model.ForumCategoryPermission{CategoryID: category.ID, GroupID: group.ID, CanView: true}).Error)
	target := model.DiscussionTarget{Kind: "forum_topic", ResourceID: thread.ID, ResourceKey: "forum_topic:" + thread.ID.String()}
	require.NoError(t, db.Create(&target).Error)
	comment := model.CommentEntry{TargetID: target.ID, AuthorID: member.UUID, Content: "Hidden comment reference", ContentHash: "hidden-comment", Status: "active"}
	require.NoError(t, db.Create(&comment).Error)

	_, err := registry.Resolve(Viewer{}, "comment", comment.ID)
	require.ErrorIs(t, err, ErrTargetUnavailable)
	guestSearch, err := registry.Search(Viewer{}, "comment", "Hidden comment", 10)
	require.NoError(t, err)
	require.Empty(t, guestSearch)

	resolved, err := registry.Resolve(Viewer{UserID: member.UUID}, "comment", comment.ID)
	require.NoError(t, err)
	require.Equal(t, comment.ID, resolved.ID)
	memberSearch, err := registry.Search(Viewer{UserID: member.UUID}, "comment", "Hidden comment", 10)
	require.NoError(t, err)
	require.Len(t, memberSearch, 1)
}

func TestRegistryHidesNonPublicPodcastReferencesFromGuests(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Post{}, &model.PodcastEpisode{})

	owner := model.User{Username: "podcast-owner", Email: "podcast-owner@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	channel := model.Channel{UserID: &owner.UUID, Name: "Private Podcast", Slug: "private-podcast"}
	require.NoError(t, db.Create(&channel).Error)
	post := model.Post{
		UserID: owner.UUID, ChannelID: &channel.ID, Title: "Private Episode", Content: "notes",
		Status: "published", Visibility: "private",
	}
	require.NoError(t, db.Create(&post).Error)
	episode := model.PodcastEpisode{PostID: post.ID, ChannelID: channel.ID, AudioURL: "private.mp3"}
	require.NoError(t, db.Create(&episode).Error)

	registry := NewRegistry(db)
	for targetType, id := range map[string]uuid.UUID{"podcast": channel.ID, "episode": episode.ID} {
		_, err := registry.Resolve(Viewer{}, targetType, id)
		require.ErrorIs(t, err, ErrTargetUnavailable)
		guestItems, err := registry.Search(Viewer{}, targetType, "Private", 10)
		require.NoError(t, err)
		require.Empty(t, guestItems)

		_, err = registry.Resolve(Viewer{UserID: owner.UUID}, targetType, id)
		require.NoError(t, err)
		ownerItems, err := registry.Search(Viewer{UserID: owner.UUID}, targetType, "Private", 10)
		require.NoError(t, err)
		require.Len(t, ownerItems, 1)
	}
}

func seedReferenceRegistry(t *testing.T) (*gorm.DB, map[string]uuid.UUID) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{}, &model.PodcastEpisode{},
		&model.ForumCategory{}, &model.ForumTopic{}, &model.Debate{}, &model.FeedSource{}, &model.FeedItem{},
		&model.ForumGroup{}, &model.ForumGroupMember{}, &model.ForumCategoryPermission{},
		&model.Artist{}, &model.Album{}, &model.Song{}, &model.Playlist{}, &model.Video{},
		&model.TimelinePerson{}, &model.TimelineEvent{}, &model.DiscussionTarget{}, &model.CommentEntry{},
	)
	user := model.User{Username: "alice", DisplayName: "Alice", Email: "alice@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	channel := model.Channel{UserID: &user.UUID, Name: "Podcast Show", Slug: "podcast-show"}
	require.NoError(t, db.Create(&channel).Error)
	collection := model.Collection{ChannelID: channel.ID, ContentType: "blog", CreatedBy: &user.UUID, Name: "Collection Name"}
	require.NoError(t, db.Create(&collection).Error)
	post := model.Post{UserID: user.UUID, ChannelID: &channel.ID, CollectionID: &collection.ID, Title: "Blog Post", Content: "body", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&post).Error)
	category := model.ForumCategory{Name: "General"}
	require.NoError(t, db.Create(&category).Error)
	thread := model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: "Forum Thread", Content: "body"}
	require.NoError(t, db.Create(&thread).Error)
	debate := model.Debate{UserID: user.UUID, Title: "Debate Topic", Status: "open"}
	require.NoError(t, db.Create(&debate).Error)
	feed := model.FeedSource{SourceType: "external_rss", Provider: "rss", Hash: "feed-hash", Title: "External Feed"}
	require.NoError(t, db.Create(&feed).Error)
	article := model.FeedItem{FeedSourceID: feed.ID, GUID: "article-guid", Title: "Feed Article", PublishedAt: time.Now(), FetchedAt: time.Now()}
	require.NoError(t, db.Create(&article).Error)
	artist := model.Artist{Name: "Artist Name"}
	require.NoError(t, db.Create(&artist).Error)
	album := model.Album{Title: "Album Title"}
	require.NoError(t, db.Create(&album).Error)
	song := model.Song{Title: "Song Title", AudioURL: "song.mp3", AlbumID: &album.ID}
	require.NoError(t, db.Create(&song).Error)
	playlist := model.Playlist{UserID: user.UUID, Name: "Playlist Name", IsPublic: true}
	require.NoError(t, db.Create(&playlist).Error)
	episodePost := model.Post{UserID: user.UUID, ChannelID: &channel.ID, Title: "Podcast Episode", Content: "notes", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&episodePost).Error)
	episode := model.PodcastEpisode{PostID: episodePost.ID, ChannelID: channel.ID, AudioURL: "episode.mp3"}
	require.NoError(t, db.Create(&episode).Error)
	video := model.Video{UserID: user.UUID, ChannelID: &channel.ID, Title: "Video Title", VideoURL: "video.mp4", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&video).Error)
	person := model.TimelinePerson{UserID: user.UUID, Name: "Person Name", IsPublic: true}
	require.NoError(t, db.Create(&person).Error)
	event := model.TimelineEvent{UserID: user.UUID, Title: "Event Title", EventDate: time.Now(), IsPublic: true}
	require.NoError(t, db.Create(&event).Error)
	target := model.DiscussionTarget{Kind: "blog_post", ResourceID: post.ID, ResourceKey: fmt.Sprintf("blog_post:%s", post.ID)}
	require.NoError(t, db.Create(&target).Error)
	comment := model.CommentEntry{TargetID: target.ID, AuthorID: user.UUID, Content: "Comment body", ContentHash: "comment-hash", Status: "active"}
	require.NoError(t, db.Create(&comment).Error)

	return db, map[string]uuid.UUID{
		"post": post.ID, "thread": thread.ID, "debate": debate.ID, "feed": feed.ID, "article": article.ID,
		"artist": artist.ID, "album": album.ID, "song": song.ID, "playlist": playlist.ID,
		"podcast": channel.ID, "episode": episode.ID, "video": video.ID, "person": person.ID, "event": event.ID,
		"channel": channel.ID, "collection": collection.ID, "comment": comment.ID,
	}
}
