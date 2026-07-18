package studio

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type studioQueryFixture struct {
	db          *gorm.DB
	service     *Service
	user        authctx.CurrentUser
	foreignUser model.User
	channel     model.Channel
	collections map[Module]model.Collection
}

func newStudioQueryFixture(t *testing.T) studioQueryFixture {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.UserStudioState{},
		&model.StudioModuleSettings{},
		&model.StudioMetricEvent{},
		&model.Post{},
		&model.PostCollection{},
		&model.PodcastEpisode{},
		&model.Video{},
		&model.VideoCollection{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
		&model.Like{},
		&model.BookmarkFolder{},
		&model.Bookmark{},
		&model.PodcastEpisodeBookmark{},
		&model.VideoBookmark{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentTimeAnchor{},
	)
	owner := model.User{Username: "studio-query-owner", Email: "studio-query-owner@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	foreign := model.User{Username: "studio-query-foreign", Email: "studio-query-foreign@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &owner.UUID, Name: "Studio Query", Slug: "studio-query-" + uuid.NewString()[:8]}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.UserStudioState{UserID: owner.UUID, ChannelID: &channel.ID}).Error; err != nil {
		t.Fatal(err)
	}
	collections := make(map[Module]model.Collection, 3)
	for _, module := range []Module{ModuleBlog, ModulePodcast, ModuleVideo} {
		collection := model.Collection{ChannelID: channel.ID, ContentType: string(module), Name: string(module) + " collection"}
		if err := db.Create(&collection).Error; err != nil {
			t.Fatal(err)
		}
		collections[module] = collection
	}
	return studioQueryFixture{
		db: db, service: NewService(db),
		user:        authctx.CurrentUser{ID: owner.UUID, Username: owner.Username, Role: authctx.RoleUser},
		foreignUser: foreign, channel: channel, collections: collections,
	}
}

func seedStudioDashboardContent(t *testing.T, fixture studioQueryFixture) {
	t.Helper()
	blogCollection := fixture.collections[ModuleBlog]
	for index := 0; index < 4; index++ {
		post := model.Post{
			UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &blogCollection.ID,
			Title: "Blog", Content: "body", Status: "published", Visibility: "public", ViewCount: int64(index + 1),
		}
		if err := fixture.db.Create(&post).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Model(&post).Update("updated_at", time.Now().Add(time.Duration(index)*time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
	}
	podcastPost := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID,
		Title: "Podcast", Content: "shownotes", Status: "published", Visibility: "public",
	}
	if err := fixture.db.Create(&podcastPost).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&podcastPost).Association("Collections").Replace([]model.Collection{fixture.collections[ModulePodcast]}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.PodcastEpisode{PostID: podcastPost.ID, ChannelID: fixture.channel.ID, AudioURL: "episode.mp3", EpisodeCoverURL: "cover.jpg"}).Error; err != nil {
		t.Fatal(err)
	}
	video := model.Video{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, Title: "Video", VideoURL: "video.mp4",
		StorageType: "local", ThumbnailURL: "cover.jpg", Status: "published", Visibility: "public",
	}
	if err := fixture.db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&video).Association("Collections").Replace([]model.Collection{fixture.collections[ModuleVideo]}); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardReturnsThreeIndependentModuleSections(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	seedStudioDashboardContent(t, fixture)
	source := model.FeedSource{
		SourceType: "internal_channel", SourceID: &fixture.channel.ID,
		Hash: "studio-dashboard-channel", Title: fixture.channel.Name,
	}
	if err := fixture.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	for _, userID := range []uuid.UUID{fixture.user.ID, fixture.foreignUser.UUID} {
		if err := fixture.db.Create(&model.Subscription{UserID: userID, FeedSourceID: source.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}

	dashboard, err := fixture.service.GetDashboard(fixture.user, uuid.Nil)
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	if len(dashboard.Sections) != 3 {
		t.Fatalf("expected three sections, got %#v", dashboard.Sections)
	}
	if dashboard.ChannelSubscriberCount != 2 {
		t.Fatalf("expected one top-level subscriber count of 2, got %d", dashboard.ChannelSubscriberCount)
	}
	wantContents := []int64{4, 1, 1}
	for index, module := range []Module{ModuleBlog, ModulePodcast, ModuleVideo} {
		section := dashboard.Sections[index]
		if section.Module != module || section.Error != "" {
			t.Fatalf("expected healthy %s section, got %#v", module, section)
		}
		if len(section.Recent) == 0 || len(section.Recent) > 3 {
			t.Fatalf("expected one to three recent %s items, got %#v", module, section.Recent)
		}
		if section.Metrics["contents"] != wantContents[index] {
			t.Fatalf("expected independent %s content count %d, got %#v", module, wantContents[index], section.Metrics)
		}
	}
}

func TestDashboardKeepsModuleErrorInsideSection(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	seedStudioDashboardContent(t, fixture)
	if err := fixture.db.Migrator().DropTable(&model.Video{}); err != nil {
		t.Fatal(err)
	}

	dashboard, err := fixture.service.GetDashboard(fixture.user, fixture.channel.ID)
	if err != nil {
		t.Fatalf("expected partial dashboard success, got %v", err)
	}
	if dashboard.Sections[0].Error != "" || dashboard.Sections[1].Error != "" {
		t.Fatalf("expected blog and podcast sections to remain available: %#v", dashboard.Sections)
	}
	if dashboard.Sections[2].Module != ModuleVideo || dashboard.Sections[2].Error == "" {
		t.Fatalf("expected isolated video error, got %#v", dashboard.Sections[2])
	}
}
