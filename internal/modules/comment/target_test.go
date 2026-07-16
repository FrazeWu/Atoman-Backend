package comment

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTargetTestRegistry(t *testing.T) (*TargetRegistry, *gorm.DB) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Post{},
		&model.Video{},
		&model.PodcastEpisode{},
		&model.FeedSource{},
		&model.Subscription{},
		&model.FeedItem{},
		&model.Artist{},
		&model.Album{},
		&model.Song{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.Debate{},
		&model.TimelineEvent{},
		&model.TimelinePerson{},
	)
	return NewTargetRegistry(db), db
}

func createTargetTestUser(t *testing.T, db *gorm.DB, name string) model.User {
	t.Helper()
	user := model.User{Username: name, Email: name + "@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	return user
}

func TestTargetRegistryRegistersExactlySupportedKinds(t *testing.T) {
	registry, _ := newTargetTestRegistry(t)
	want := []string{
		TargetKindBlogPost,
		TargetKindVideo,
		TargetKindPodcastEpisode,
		TargetKindFeedArticle,
		TargetKindMusicArtist,
		TargetKindMusicAlbum,
		TargetKindMusicSong,
		TargetKindForumTopic,
		TargetKindDebate,
		TargetKindTimelineEvent,
		TargetKindTimelinePerson,
	}
	require.Len(t, registry.resolvers, len(want))
	for _, kind := range want {
		require.Contains(t, registry.resolvers, kind)
	}
}

func TestTargetRegistryRejectsUnknownKindAndNilResource(t *testing.T) {
	registry, _ := newTargetTestRegistry(t)

	_, err := registry.Resolve(Viewer{}, TargetRef{Kind: "unknown", ResourceID: uuid.New()})
	require.ErrorIs(t, err, ErrUnknownTargetKind)

	_, err = registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindBlogPost})
	require.ErrorIs(t, err, ErrInvalidTargetResource)
}

func TestTargetRegistryDistinguishesMissingFromInvisible(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	owner := createTargetTestUser(t, db, "owner")
	post := model.Post{UserID: owner.UUID, Title: "draft", Content: "body", Status: "draft", Visibility: "private"}
	require.NoError(t, db.Create(&post).Error)

	missing, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindBlogPost, ResourceID: uuid.New()})
	require.ErrorIs(t, err, ErrTargetNotFound)
	require.False(t, missing.Visible)

	invisible, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindBlogPost, ResourceID: post.ID})
	require.NoError(t, err)
	require.False(t, invisible.Visible)
}

func TestFeedArticleResolverSharesCanonicalOriginalURL(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	source := model.FeedSource{SourceType: "external_rss", Provider: "rss", Category: "blog", Hash: uuid.NewString()}
	require.NoError(t, db.Create(&source).Error)
	items := []model.FeedItem{
		{FeedSourceID: source.ID, GUID: "a", Link: "https://example.com/post/?utm_source=a"},
		{FeedSourceID: source.ID, GUID: "b", Link: "https://EXAMPLE.com//post#fragment"},
	}
	require.NoError(t, db.Create(&items).Error)

	first, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: items[0].ID})
	require.NoError(t, err)
	second, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: items[1].ID})
	require.NoError(t, err)
	require.Equal(t, "https://example.com/post", first.ResourceKey)
	require.Equal(t, first.ResourceKey, second.ResourceKey)
	require.Nil(t, first.OwnerID)
	require.Empty(t, first.MarkLabel)
}

func TestFeedArticleResolverPreservesEncodedPathSeparators(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	source := model.FeedSource{SourceType: "external_rss", Provider: "rss", Category: "blog", Hash: uuid.NewString()}
	require.NoError(t, db.Create(&source).Error)
	items := []model.FeedItem{
		{FeedSourceID: source.ID, GUID: "duplicate-slashes", Link: "https://example.com/a//b/"},
		{FeedSourceID: source.ID, GUID: "clean-path", Link: "https://example.com/a/b"},
		{FeedSourceID: source.ID, GUID: "encoded-separator", Link: "https://example.com/a%2Fb"},
	}
	require.NoError(t, db.Create(&items).Error)

	duplicateSlashes, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: items[0].ID})
	require.NoError(t, err)
	cleanPath, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: items[1].ID})
	require.NoError(t, err)
	encodedSeparator, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: items[2].ID})
	require.NoError(t, err)
	require.Equal(t, cleanPath.ResourceKey, duplicateSlashes.ResourceKey)
	require.NotEqual(t, cleanPath.ResourceKey, encodedSeparator.ResourceKey)
	require.Equal(t, "https://example.com/a%2Fb", encodedSeparator.ResourceKey)
}

func TestFeedArticleResolverNormalizesPercentEncodedPath(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	source := model.FeedSource{SourceType: "external_rss", Provider: "rss", Category: "blog", Hash: uuid.NewString()}
	require.NoError(t, db.Create(&source).Error)
	items := []model.FeedItem{
		{FeedSourceID: source.ID, GUID: "reserved-upper", Link: "https://example.com/a%2Fb"},
		{FeedSourceID: source.ID, GUID: "reserved-lower", Link: "https://example.com/a%2fb"},
		{FeedSourceID: source.ID, GUID: "unreserved-encoded", Link: "https://example.com/%7Euser"},
		{FeedSourceID: source.ID, GUID: "unreserved-plain", Link: "https://example.com/~user"},
		{FeedSourceID: source.ID, GUID: "unicode-literal", Link: "https://example.com/café/%7euser/"},
		{FeedSourceID: source.ID, GUID: "unicode-encoded", Link: "https://example.com/caf%C3%A9/~user"},
	}
	require.NoError(t, db.Create(&items).Error)

	keys := make([]string, len(items))
	for i, item := range items {
		resolved, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: item.ID})
		require.NoError(t, err)
		keys[i] = resolved.ResourceKey
	}
	require.Equal(t, "https://example.com/a%2Fb", keys[0])
	require.Equal(t, keys[0], keys[1])
	require.Equal(t, "https://example.com/~user", keys[2])
	require.Equal(t, keys[2], keys[3])
	require.Equal(t, "https://example.com/caf%C3%A9/~user", keys[4])
	require.Equal(t, keys[4], keys[5])
}

func TestFeedArticleResolverNormalizesTrackingAndStableQueryOrder(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	source := model.FeedSource{SourceType: "external_rss", Provider: "rss", Category: "blog", Hash: uuid.NewString()}
	require.NoError(t, db.Create(&source).Error)
	items := []model.FeedItem{{
		FeedSourceID: source.ID,
		GUID:         "query",
		Link:         "HTTP://Example.COM/a/../post/?z=2&utm_medium=x&a=1&fbclid=y&gclid=z&MC_CID=a&mc_eid=b&Ref=c&REF_SRC=d&Source=e",
	}, {
		FeedSourceID: source.ID,
		GUID:         "query-clean",
		Link:         "http://example.com/post?a=1&z=2",
	}}
	require.NoError(t, db.Create(&items).Error)

	resolved, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: items[0].ID})
	require.NoError(t, err)
	require.Equal(t, "http://example.com/post?a=1&z=2", resolved.ResourceKey)
	clean, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: items[1].ID})
	require.NoError(t, err)
	require.Equal(t, clean.ResourceKey, resolved.ResourceKey)
}

func TestFeedArticleResolverRejectsInvalidOriginalURL(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	source := model.FeedSource{SourceType: "external_rss", Provider: "rss", Category: "blog", Hash: uuid.NewString()}
	require.NoError(t, db.Create(&source).Error)
	item := model.FeedItem{FeedSourceID: source.ID, GUID: "bad", Link: "/relative/path"}
	require.NoError(t, db.Create(&item).Error)

	_, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: item.ID})
	require.ErrorIs(t, err, ErrInvalidTargetResource)
}

func TestFeedArticleResolverRejectsMalformedQuery(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	source := model.FeedSource{SourceType: "external_rss", Provider: "rss", Category: "blog", Hash: uuid.NewString()}
	require.NoError(t, db.Create(&source).Error)
	items := []model.FeedItem{
		{FeedSourceID: source.ID, GUID: "bad-percent-query", Link: "https://example.com/post?key=%zz"},
		{FeedSourceID: source.ID, GUID: "bad-semicolon-query", Link: "https://example.com/post?key=value;other=value"},
	}
	require.NoError(t, db.Create(&items).Error)

	for _, item := range items {
		_, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: item.ID})
		require.ErrorIs(t, err, ErrInvalidTargetResource)
	}
}

func TestFeedArticleResolverNormalizesHostCredentialsAndPorts(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	source := model.FeedSource{SourceType: "external_rss", Provider: "rss", Category: "blog", Hash: uuid.NewString()}
	require.NoError(t, db.Create(&source).Error)
	items := []model.FeedItem{
		{FeedSourceID: source.ID, GUID: "userinfo", Link: "https://user:password@EXAMPLE.com/post"},
		{FeedSourceID: source.ID, GUID: "http-default", Link: "http://EXAMPLE.com:80/post"},
		{FeedSourceID: source.ID, GUID: "https-default", Link: "https://EXAMPLE.com:443/post"},
		{FeedSourceID: source.ID, GUID: "non-default", Link: "https://EXAMPLE.com:8443/post"},
		{FeedSourceID: source.ID, GUID: "ipv6-default", Link: "https://[2001:DB8::1]:443/post"},
	}
	require.NoError(t, db.Create(&items).Error)

	_, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: items[0].ID})
	require.ErrorIs(t, err, ErrInvalidTargetResource)
	want := []string{
		"http://example.com/post",
		"https://example.com/post",
		"https://example.com:8443/post",
		"https://[2001:db8::1]/post",
	}
	for i, expected := range want {
		resolved, err := registry.Resolve(Viewer{}, TargetRef{Kind: TargetKindFeedArticle, ResourceID: items[i+1].ID})
		require.NoError(t, err)
		require.Equal(t, expected, resolved.ResourceKey)
	}
}

func TestContentResolversRespectPublicationVisibilityAndOwnership(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	owner := createTargetTestUser(t, db, "content-owner")
	ownerViewer := Viewer{UserID: &owner.UUID}

	post := model.Post{UserID: owner.UUID, Title: "post", Content: "body", Status: "draft", Visibility: "private"}
	video := model.Video{UserID: owner.UUID, Title: "video", VideoURL: "video.mp4", Status: "draft", Visibility: "private", DurationSec: 75}
	require.NoError(t, db.Create(&post).Error)
	require.NoError(t, db.Create(&video).Error)
	episodePost := model.Post{UserID: owner.UUID, Title: "episode", Content: "body", Status: "draft", Visibility: "private"}
	require.NoError(t, db.Create(&episodePost).Error)
	episode := model.PodcastEpisode{PostID: episodePost.ID, ChannelID: uuid.New(), AudioURL: "episode.mp3", DurationSec: 125}
	require.NoError(t, db.Create(&episode).Error)

	refs := []TargetRef{
		{Kind: TargetKindBlogPost, ResourceID: post.ID},
		{Kind: TargetKindVideo, ResourceID: video.ID},
		{Kind: TargetKindPodcastEpisode, ResourceID: episode.ID},
	}
	for _, ref := range refs {
		t.Run(ref.Kind, func(t *testing.T) {
			guest, err := registry.Resolve(Viewer{}, ref)
			require.NoError(t, err)
			require.False(t, guest.Visible)

			author, err := registry.Resolve(ownerViewer, ref)
			require.NoError(t, err)
			require.True(t, author.Visible)
			require.Equal(t, owner.UUID, *author.OwnerID)
			require.Equal(t, "置顶", author.MarkLabel)
		})
	}
}

func TestContentResolversAllowChannelSubscribersToSeePublishedFollowersContent(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	owner := createTargetTestUser(t, db, "subscriber-owner")
	subscriber := createTargetTestUser(t, db, "subscriber")
	channel := model.Channel{UserID: &owner.UUID, Name: "followers", Slug: "followers", ContentType: model.ChannelContentTypeBlog}
	require.NoError(t, db.Create(&channel).Error)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("internal_channel:"+channel.ID.String())))
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &channel.ID, Provider: "internal", Category: "blog", Hash: hash}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&model.Subscription{UserID: subscriber.UUID, FeedSourceID: source.ID}).Error)

	post := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, Title: "post", Content: "body", Status: "published", Visibility: "followers"}
	video := model.Video{UserID: owner.UUID, ChannelID: &channel.ID, Title: "video", VideoURL: "video.mp4", Status: "published", Visibility: "followers"}
	require.NoError(t, db.Create(&post).Error)
	require.NoError(t, db.Create(&video).Error)

	for _, ref := range []TargetRef{
		{Kind: TargetKindBlogPost, ResourceID: post.ID},
		{Kind: TargetKindVideo, ResourceID: video.ID},
	} {
		guest, err := registry.Resolve(Viewer{}, ref)
		require.NoError(t, err)
		require.False(t, guest.Visible)

		resolved, err := registry.Resolve(Viewer{UserID: &subscriber.UUID}, ref)
		require.NoError(t, err)
		require.True(t, resolved.Visible)
	}
}

func TestResolversCoverAllKindsDurationsOwnersAndMarkLabels(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	owner := createTargetTestUser(t, db, "all-owner")
	category := model.ForumCategory{Name: "general"}
	require.NoError(t, db.Create(&category).Error)
	source := model.FeedSource{SourceType: "external_rss", Provider: "rss", Category: "blog", Hash: uuid.NewString()}
	require.NoError(t, db.Create(&source).Error)

	post := model.Post{UserID: owner.UUID, Title: "post", Content: "body", Status: "published", Visibility: "public"}
	video := model.Video{UserID: owner.UUID, Title: "video", VideoURL: "video.mp4", Status: "published", Visibility: "public", DurationSec: 91}
	episodePost := model.Post{UserID: owner.UUID, Title: "episode", Content: "body", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&post).Error)
	require.NoError(t, db.Create(&video).Error)
	require.NoError(t, db.Create(&episodePost).Error)
	episode := model.PodcastEpisode{PostID: episodePost.ID, ChannelID: uuid.New(), AudioURL: "episode.mp3", DurationSec: 181}
	feedItem := model.FeedItem{FeedSourceID: source.ID, GUID: "all", Link: "https://example.com/article"}
	artist := model.Artist{Name: "artist", EntryStatus: "open"}
	album := model.Album{Title: "album", Status: "open", EntryStatus: "open"}
	song := model.Song{Title: "song", AudioURL: "song.mp3", Status: "open", DurationSec: 241}
	topic := model.ForumTopic{UserID: owner.UUID, CategoryID: category.ID, Title: "topic", Content: "body"}
	debate := model.Debate{UserID: owner.UUID, Title: "debate", Status: "open"}
	event := model.TimelineEvent{UserID: owner.UUID, Title: "event", IsPublic: true}
	person := model.TimelinePerson{UserID: owner.UUID, Name: "person", IsPublic: true}
	for _, entity := range []any{&episode, &feedItem, &artist, &album, &song, &topic, &debate, &event, &person} {
		require.NoError(t, db.Create(entity).Error)
	}

	tests := []struct {
		kind      string
		id        uuid.UUID
		owner     bool
		duration  int
		markLabel string
	}{
		{TargetKindBlogPost, post.ID, true, 0, "置顶"},
		{TargetKindVideo, video.ID, true, 91, "置顶"},
		{TargetKindPodcastEpisode, episode.ID, true, 181, "置顶"},
		{TargetKindFeedArticle, feedItem.ID, false, 0, ""},
		{TargetKindMusicArtist, artist.ID, false, 0, ""},
		{TargetKindMusicAlbum, album.ID, false, 0, ""},
		{TargetKindMusicSong, song.ID, false, 241, ""},
		{TargetKindForumTopic, topic.ID, true, 0, "最佳回答"},
		{TargetKindDebate, debate.ID, true, 0, "置顶"},
		{TargetKindTimelineEvent, event.ID, true, 0, "置顶"},
		{TargetKindTimelinePerson, person.ID, true, 0, "置顶"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			resolved, err := registry.Resolve(Viewer{}, TargetRef{Kind: tt.kind, ResourceID: tt.id})
			require.NoError(t, err)
			require.Equal(t, tt.kind, resolved.Kind)
			require.Equal(t, tt.id, resolved.ResourceID)
			require.NotEmpty(t, resolved.ResourceKey)
			require.True(t, resolved.Visible)
			require.Equal(t, tt.duration, resolved.DurationSec)
			require.Equal(t, tt.markLabel, resolved.MarkLabel)
			if tt.owner {
				require.NotNil(t, resolved.OwnerID)
				require.Equal(t, owner.UUID, *resolved.OwnerID)
			} else {
				require.Nil(t, resolved.OwnerID)
			}
		})
	}
}

func TestResolversKeepHiddenModuleResourcesInvisible(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	owner := createTargetTestUser(t, db, "hidden-owner")
	source := model.FeedSource{SourceType: "external_rss", Provider: "rss", Category: "blog", Hash: uuid.NewString(), Hidden: true}
	require.NoError(t, db.Create(&source).Error)
	entities := []struct {
		kind   string
		entity any
	}{
		{TargetKindFeedArticle, &model.FeedItem{FeedSourceID: source.ID, GUID: "hidden", Link: "https://example.com/hidden"}},
		{TargetKindMusicArtist, &model.Artist{Name: "hidden artist", EntryStatus: "closed"}},
		{TargetKindMusicAlbum, &model.Album{Title: "hidden album", Status: "closed", EntryStatus: "open"}},
		{TargetKindMusicSong, &model.Song{Title: "hidden song", AudioURL: "hidden.mp3", Status: "closed"}},
		{TargetKindTimelineEvent, &model.TimelineEvent{UserID: owner.UUID, Title: "hidden event"}},
		{TargetKindTimelinePerson, &model.TimelinePerson{UserID: owner.UUID, Name: "hidden person"}},
	}
	for i := range entities {
		require.NoError(t, db.Create(entities[i].entity).Error)
		value := requireTargetEntityID(t, entities[i].entity)
		if entities[i].kind == TargetKindTimelineEvent || entities[i].kind == TargetKindTimelinePerson {
			require.NoError(t, db.Model(entities[i].entity).Update("is_public", false).Error)
		}
		resolved, err := registry.Resolve(Viewer{}, TargetRef{Kind: entities[i].kind, ResourceID: value})
		require.NoError(t, err)
		require.False(t, resolved.Visible, entities[i].kind)
		if entities[i].kind == TargetKindTimelineEvent || entities[i].kind == TargetKindTimelinePerson {
			ownerResolved, err := registry.Resolve(Viewer{UserID: &owner.UUID}, TargetRef{Kind: entities[i].kind, ResourceID: value})
			require.NoError(t, err)
			require.False(t, ownerResolved.Visible, entities[i].kind)
		}
	}
}

func TestMusicResolversOnlyHideClosedStatus(t *testing.T) {
	registry, db := newTargetTestRegistry(t)
	entities := []struct {
		kind   string
		entity any
	}{
		{TargetKindMusicArtist, &model.Artist{Name: "draft artist", EntryStatus: "draft"}},
		{TargetKindMusicAlbum, &model.Album{Title: "rejected album", Status: "rejected", EntryStatus: "open"}},
		{TargetKindMusicSong, &model.Song{Title: "unknown song", AudioURL: "song.mp3", Status: "custom"}},
	}
	for _, tt := range entities {
		require.NoError(t, db.Create(tt.entity).Error)
		resolved, err := registry.Resolve(Viewer{}, TargetRef{Kind: tt.kind, ResourceID: requireTargetEntityID(t, tt.entity)})
		require.NoError(t, err)
		require.True(t, resolved.Visible, tt.kind)
	}
}

func requireTargetEntityID(t *testing.T, entity any) uuid.UUID {
	t.Helper()
	switch value := entity.(type) {
	case *model.FeedItem:
		return value.ID
	case *model.Artist:
		return value.ID
	case *model.Album:
		return value.ID
	case *model.Song:
		return value.ID
	case *model.TimelineEvent:
		return value.ID
	case *model.TimelinePerson:
		return value.ID
	default:
		require.FailNow(t, "unsupported test entity")
		return uuid.Nil
	}
}

func TestTargetErrorsAreComparableSentinels(t *testing.T) {
	require.True(t, errors.Is(fmt.Errorf("wrapped: %w", ErrUnknownTargetKind), ErrUnknownTargetKind))
	require.True(t, errors.Is(fmt.Errorf("wrapped: %w", ErrTargetNotFound), ErrTargetNotFound))
	require.True(t, errors.Is(fmt.Errorf("wrapped: %w", ErrInvalidTargetResource), ErrInvalidTargetResource))
}
