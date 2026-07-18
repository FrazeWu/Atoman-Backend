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

func TestStudioContentsRejectCollectionOutsideModule(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	_, _, err := fixture.service.ListContents(fixture.user, ModuleBlog, ContentQuery{
		ChannelID: fixture.channel.ID, CollectionID: fixture.collections[ModuleVideo].ID,
	})
	if err == nil {
		t.Fatal("expected module-mismatched collection to be rejected")
	}
}

func TestStudioContentsFilterByDashboardIssue(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	missing := createStudioBlogPost(t, fixture, fixture.collections[ModuleBlog], "Missing cover", "draft", "public", time.Now())
	covered := createStudioBlogPost(t, fixture, fixture.collections[ModuleBlog], "Covered", "draft", "public", time.Now().Add(time.Minute))
	if err := fixture.db.Model(&covered).Update("cover_url", "cover.jpg").Error; err != nil {
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
