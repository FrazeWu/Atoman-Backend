package studio

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
)

func createStudioBlogPost(t *testing.T, fixture studioQueryFixture, collection model.Collection, title, status, visibility string, updatedAt time.Time) model.Post {
	t.Helper()
	post := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &collection.ID,
		Title: title, Content: "body", Summary: "summary " + title, Status: status, Visibility: visibility,
	}
	if err := fixture.db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&post).Update("updated_at", updatedAt).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.ContentEntry{}).Where("id = ?", post.ID).Update("updated_at", updatedAt).Error; err != nil {
		t.Fatal(err)
	}
	return post
}

func TestStudioContentsFilterByChannelModuleStatusVisibilityAndCollection(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	secondCollection := model.Collection{ChannelID: fixture.channel.ID, ContentType: string(ModuleBlog), Name: "second blog collection"}
	if err := fixture.db.Create(&secondCollection).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	wanted := createStudioBlogPost(t, fixture, fixture.collections[ModuleBlog], "Needle published", "published", "followers", now)
	createStudioBlogPost(t, fixture, fixture.collections[ModuleBlog], "Needle draft", "draft", "followers", now.Add(time.Minute))
	createStudioBlogPost(t, fixture, secondCollection, "Needle wrong collection", "published", "followers", now.Add(2*time.Minute))
	createStudioBlogPost(t, fixture, fixture.collections[ModuleBlog], "Wrong visibility", "published", "private", now.Add(3*time.Minute))

	items, total, err := fixture.service.ListContents(fixture.user, ModuleBlog, ContentQuery{
		ChannelID: fixture.channel.ID, Search: "needle", Status: "published", Visibility: "subscribers",
		CollectionID: fixture.collections[ModuleBlog].ID, Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("list contents: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != wanted.ID {
		t.Fatalf("expected only matching blog post, total=%d items=%#v", total, items)
	}
	if items[0].Module != ModuleBlog || items[0].Visibility != "subscribers" {
		t.Fatalf("expected canonical Studio fields, got %#v", items[0])
	}
}

func TestStudioContentsExposeScheduledPublicationTime(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	scheduledAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	post := createStudioBlogPost(t, fixture, fixture.collections[ModuleBlog], "Scheduled", "scheduled", "public", time.Now())
	if err := fixture.db.Model(&post).Update("scheduled_at", scheduledAt).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.ContentEntry{}).Where("id = ?", post.ID).Update("scheduled_at", scheduledAt).Error; err != nil {
		t.Fatal(err)
	}

	items, total, err := fixture.service.ListContents(fixture.user, ModuleBlog, ContentQuery{
		ChannelID: fixture.channel.ID, Status: "scheduled", Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ScheduledAt == nil || !items[0].ScheduledAt.Equal(scheduledAt) {
		t.Fatalf("expected scheduled content and time, total=%d items=%#v", total, items)
	}
}

func TestStudioBlogContentsFilterAndRenderManyToManyCollection(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	secondary := model.Collection{ChannelID: fixture.channel.ID, ContentType: string(ModuleBlog), Name: "secondary blog collection"}
	if err := fixture.db.Create(&secondary).Error; err != nil {
		t.Fatal(err)
	}
	post := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID,
		Title: "Multi collection", Content: "body", Status: "draft", Visibility: "public",
	}
	if err := fixture.db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&post).Association("Collections").Replace([]model.Collection{secondary}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("content_id = ?", post.ID).Delete(&model.ContentCollectionMembership{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.ContentCollectionMembership{ContentID: post.ID, CollectionID: secondary.ID}).Error; err != nil {
		t.Fatal(err)
	}

	items, total, err := fixture.service.ListContents(fixture.user, ModuleBlog, ContentQuery{
		ChannelID: fixture.channel.ID, CollectionID: secondary.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != post.ID {
		t.Fatalf("expected post from many-to-many collection, total=%d items=%#v", total, items)
	}
	if len(items[0].Collections) != 1 || items[0].Collections[0].ID != secondary.ID {
		t.Fatalf("expected secondary collection in response, got %#v", items[0].Collections)
	}
}

func TestStudioVideoContentsUseScalarCollectionAndExposeLegacyConflict(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	videoCollection := fixture.collections[ModuleVideo]
	secondCollection := model.Collection{ChannelID: fixture.channel.ID, ContentType: string(ModuleVideo), Name: "second video collection"}
	if err := fixture.db.Create(&secondCollection).Error; err != nil {
		t.Fatal(err)
	}
	scalar := model.Video{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &videoCollection.ID,
		Title: "Scalar collection", VideoURL: "https://example.com/scalar.mp4", Status: "draft", Visibility: "public",
	}
	missing := model.Video{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID,
		Title: "No collection", VideoURL: "https://example.com/missing.mp4", Status: "draft", Visibility: "public",
	}
	conflicted := model.Video{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionConflict: true,
		Title: "Legacy conflict", VideoURL: "https://example.com/conflict.mp4", Status: "draft", Visibility: "public",
	}
	for _, video := range []*model.Video{&scalar, &missing, &conflicted} {
		if err := fixture.db.Create(video).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.db.Model(&conflicted).Association("Collections").Replace([]model.Collection{videoCollection, secondCollection}); err != nil {
		t.Fatal(err)
	}

	items, total, err := fixture.service.ListContents(fixture.user, ModuleVideo, ContentQuery{
		ChannelID: fixture.channel.ID, CollectionID: videoCollection.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected scalar and legacy video matches, total=%d items=%#v", total, items)
	}
	var foundScalar, foundConflict bool
	for _, item := range items {
		switch item.ID {
		case scalar.ID:
			foundScalar = item.Collection != nil && item.Collection.ID == videoCollection.ID && !item.CollectionConflict
		case conflicted.ID:
			foundConflict = item.CollectionConflict && len(item.Collections) == 2
		}
	}
	if !foundScalar || !foundConflict {
		t.Fatalf("expected scalar collection and conflict marker, got %#v", items)
	}

	missingItems, missingTotal, err := fixture.service.ListContents(fixture.user, ModuleVideo, ContentQuery{
		ChannelID: fixture.channel.ID, Issue: "missing_collection",
	})
	if err != nil {
		t.Fatal(err)
	}
	if missingTotal != 1 || len(missingItems) != 1 || missingItems[0].ID != missing.ID {
		t.Fatalf("expected only video without scalar or legacy collection, total=%d items=%#v", missingTotal, missingItems)
	}
}

func TestStudioReorderCollectionContentsPersistsPodcastAndVideoPositions(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	podcastCollection, videoCollection := fixture.collections[ModulePodcast], fixture.collections[ModuleVideo]
	firstPost := model.Post{UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &podcastCollection.ID, Title: "first", Content: "notes", Status: "draft", Visibility: "public"}
	secondPost := model.Post{UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &podcastCollection.ID, Title: "second", Content: "notes", Status: "draft", Visibility: "public"}
	for _, post := range []*model.Post{&firstPost, &secondPost} {
		if err := fixture.db.Create(post).Error; err != nil {
			t.Fatal(err)
		}
	}
	firstEpisode := model.PodcastEpisode{PostID: firstPost.ID, ChannelID: fixture.channel.ID, AudioURL: "first.mp3"}
	secondEpisode := model.PodcastEpisode{PostID: secondPost.ID, ChannelID: fixture.channel.ID, AudioURL: "second.mp3"}
	for _, episode := range []*model.PodcastEpisode{&firstEpisode, &secondEpisode} {
		if err := fixture.db.Create(episode).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.service.ReorderCollectionContents(fixture.user, ModulePodcast, podcastCollection.ID, []uuid.UUID{secondEpisode.ID, firstEpisode.ID}); err != nil {
		t.Fatal(err)
	}
	var posts []model.Post
	if err := fixture.db.Where("id IN ?", []uuid.UUID{firstPost.ID, secondPost.ID}).Order("collection_position ASC").Find(&posts).Error; err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 || posts[0].ID != secondPost.ID {
		t.Fatalf("unexpected podcast order: %#v", posts)
	}

	firstVideo := model.Video{UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &videoCollection.ID, Title: "first video", VideoURL: "https://example.com/one.mp4"}
	secondVideo := model.Video{UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &videoCollection.ID, Title: "second video", VideoURL: "https://example.com/two.mp4"}
	for _, video := range []*model.Video{&firstVideo, &secondVideo} {
		if err := fixture.db.Create(video).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.service.ReorderCollectionContents(fixture.user, ModuleVideo, videoCollection.ID, []uuid.UUID{secondVideo.ID, firstVideo.ID}); err != nil {
		t.Fatal(err)
	}
	var videos []model.Video
	if err := fixture.db.Where("id IN ?", []uuid.UUID{firstVideo.ID, secondVideo.ID}).Order("collection_position ASC").Find(&videos).Error; err != nil {
		t.Fatal(err)
	}
	if len(videos) != 2 || videos[0].ID != secondVideo.ID {
		t.Fatalf("unexpected video order: %#v", videos)
	}
	if err := fixture.service.ReorderCollectionContents(fixture.user, ModuleVideo, videoCollection.ID, []uuid.UUID{firstVideo.ID}); apperr.FromError(err) == nil {
		t.Fatalf("expected incomplete order to be rejected, got %v", err)
	}
}

func TestStudioContentsDefaultToUpdatedDescending(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	now := time.Now().UTC()
	older := createStudioBlogPost(t, fixture, fixture.collections[ModuleBlog], "Older", "draft", "public", now)
	newer := createStudioBlogPost(t, fixture, fixture.collections[ModuleBlog], "Newer", "draft", "public", now.Add(time.Hour))

	items, total, err := fixture.service.ListContents(fixture.user, ModuleBlog, ContentQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 || items[0].ID != newer.ID || items[1].ID != older.ID {
		t.Fatalf("expected updated descending, total=%d items=%#v", total, items)
	}
}

func TestStudioContentsIncludeModuleMetrics(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	post := createStudioBlogPost(t, fixture, fixture.collections[ModuleBlog], "Measured", "published", "public", time.Now())
	if err := fixture.db.Model(&post).Update("view_count", 7).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.ContentBlogExtension{}).Where("content_id = ?", post.ID).Update("view_count", 7).Error; err != nil {
		t.Fatal(err)
	}
	target := model.DiscussionTarget{Kind: "blog_post", ResourceID: post.ID, ResourceKey: post.ID.String(), OwnerID: &fixture.user.ID}
	if err := fixture.db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.CommentEntry{TargetID: target.ID, AuthorID: fixture.foreignUser.UUID, Content: "comment", ContentHash: uuid.NewString(), Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.Like{UserID: fixture.foreignUser.UUID, TargetType: "post", TargetID: post.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.Bookmark{UserID: fixture.foreignUser.UUID, PostID: post.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.StudioMetricEvent{ChannelID: fixture.channel.ID, ContentType: "blog", ContentID: post.ID, Metric: "share"}).Error; err != nil {
		t.Fatal(err)
	}

	items, _, err := fixture.service.ListContents(fixture.user, ModuleBlog, ContentQuery{ChannelID: fixture.channel.ID})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"view": 7, "comment": 1, "like": 1, "bookmark": 1, "share": 1}
	for metric, count := range want {
		if items[0].Metrics[metric] != count {
			t.Fatalf("expected %s=%d, got %#v", metric, count, items[0].Metrics)
		}
	}
}

func TestStudioPodcastContentMetricsCountFavoritesOnly(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	post := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID,
		Title: "Measured episode", Content: "shownotes", Status: "published", Visibility: "public",
	}
	if err := fixture.db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}
	episode := model.PodcastEpisode{PostID: post.ID, ChannelID: fixture.channel.ID, AudioURL: "episode.mp3"}
	if err := fixture.db.Create(&episode).Error; err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"favorite", "listen_later"} {
		if err := fixture.db.Create(&model.PodcastEpisodeBookmark{UserID: fixture.foreignUser.UUID, EpisodeID: episode.ID, Kind: kind}).Error; err != nil {
			t.Fatal(err)
		}
	}

	items, _, err := fixture.service.ListContents(fixture.user, ModulePodcast, ContentQuery{ChannelID: fixture.channel.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != episode.ID {
		t.Fatalf("expected measured episode, got %#v", items)
	}
	if got := items[0].Metrics["bookmark"]; got != 1 {
		t.Fatalf("expected favorite bookmark count 1, got %d", got)
	}
}

func TestStudioContentsNeverReturnAnotherOwnersContent(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	blogCollection := fixture.collections[ModuleBlog]
	owned := createStudioBlogPost(t, fixture, blogCollection, "Owned", "draft", "public", time.Now())
	foreign := model.Post{
		UserID: fixture.foreignUser.UUID, ChannelID: &fixture.channel.ID, CollectionID: &blogCollection.ID,
		Title: "Foreign", Content: "body", Status: "draft", Visibility: "public",
	}
	if err := fixture.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}

	items, total, err := fixture.service.ListContents(fixture.user, ModuleBlog, ContentQuery{ChannelID: fixture.channel.ID})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != owned.ID {
		t.Fatalf("expected only owned content, total=%d items=%#v", total, items)
	}
}

func TestStudioContentsAllowUnifiedCollectionAcrossKinds(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	items, total, err := fixture.service.ListContents(fixture.user, ModuleBlog, ContentQuery{
		ChannelID: fixture.channel.ID, CollectionID: fixture.collections[ModuleVideo].ID,
	})
	if err != nil {
		t.Fatalf("expected unified collection scope to be accepted: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("expected empty unified collection result, total=%d items=%#v", total, items)
	}
}

func TestStudioContentsFilterByDashboardIssue(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	missing := createStudioBlogPost(t, fixture, fixture.collections[ModuleBlog], "Missing cover", "draft", "public", time.Now())
	covered := createStudioBlogPost(t, fixture, fixture.collections[ModuleBlog], "Covered", "draft", "public", time.Now().Add(time.Minute))
	if err := fixture.db.Model(&covered).Update("cover_url", "cover.jpg").Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.ContentEntry{}).Where("id = ?", covered.ID).Update("cover_url", "cover.jpg").Error; err != nil {
		t.Fatal(err)
	}

	items, total, err := fixture.service.ListContents(fixture.user, ModuleBlog, ContentQuery{Issue: "missing_cover"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != missing.ID {
		t.Fatalf("expected only missing cover issue, total=%d items=%#v", total, items)
	}
}

func TestStudioContentsRejectForeignChannel(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	foreignChannel := model.Channel{UserID: &fixture.foreignUser.UUID, Name: "Foreign", Slug: "foreign-" + uuid.NewString()[:8]}
	if err := fixture.db.Create(&foreignChannel).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err := fixture.service.ListContents(fixture.user, ModuleBlog, ContentQuery{ChannelID: foreignChannel.ID})
	if err == nil {
		t.Fatal("expected foreign channel to be rejected")
	}
}

func TestStudioContentsRejectForeignCollectionWithForbidden(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	foreignChannel := model.Channel{UserID: &fixture.foreignUser.UUID, Name: "Foreign Collection Channel", Slug: "foreign-collection-" + uuid.NewString()[:8]}
	if err := fixture.db.Create(&foreignChannel).Error; err != nil {
		t.Fatal(err)
	}
	foreignCollection := model.Collection{ChannelID: foreignChannel.ID, ContentType: string(ModuleBlog), Name: "Foreign Collection"}
	if err := fixture.db.Create(&foreignCollection).Error; err != nil {
		t.Fatal(err)
	}

	_, _, err := fixture.service.ListContents(fixture.user, ModuleBlog, ContentQuery{
		ChannelID: fixture.channel.ID, CollectionID: foreignCollection.ID,
	})
	appErr := apperr.FromError(err)
	if appErr == nil || appErr.HTTPStatus != 403 {
		t.Fatalf("expected foreign collection 403, got %v", err)
	}
}
