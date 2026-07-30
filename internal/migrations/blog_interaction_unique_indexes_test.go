package migrations

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

type legacyPodcastBookmarkAutoMigration struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
	UserID    uuid.UUID      `gorm:"type:uuid;not null"`
	EpisodeID uuid.UUID      `gorm:"type:uuid;not null"`
	Kind      *string
}

func (legacyPodcastBookmarkAutoMigration) TableName() string {
	return "podcast_episode_bookmarks"
}

type podcastBookmarkAutoMigration struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
	UserID    uuid.UUID      `gorm:"type:uuid;not null"`
	EpisodeID uuid.UUID      `gorm:"type:uuid;not null"`
	Kind      string         `gorm:"not null;default:'favorite'"`
}

func (podcastBookmarkAutoMigration) TableName() string {
	return "podcast_episode_bookmarks"
}

func TestRunBlogInteractionUniqueIndexesCreatesExpectedIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Post{},
		&model.Video{},
		&model.PodcastEpisode{},
		&model.Like{},
		&model.Bookmark{},
		&model.VideoBookmark{},
		&model.PodcastEpisodeBookmark{},
		&model.ChannelBookmark{},
	)

	if err := RunBlogInteractionUniqueIndexes(db); err != nil {
		t.Fatalf("run blog interaction unique indexes migration: %v", err)
	}

	assertIndexExists(t, db, "likes", "idx_likes_user_target")
	assertIndexExists(t, db, "bookmarks", "idx_bookmarks_user_post")
	assertIndexExists(t, db, "video_bookmarks", "idx_video_bookmarks_user_video")
	assertIndexExists(t, db, "podcast_episode_bookmarks", "idx_podcast_episode_bookmarks_user_episode_kind")
	assertIndexExists(t, db, "channel_bookmarks", "idx_channel_bookmarks_user_channel_kind")
}

func TestDeduplicateBlogInteractionsSkipsMissingTables(t *testing.T) {
	db := testdb.Open(t)

	if err := DeduplicateBlogInteractions(db); err != nil {
		t.Fatalf("expected missing tables to be skipped, got %v", err)
	}
}

func TestDeduplicateBlogInteractionsSupportsLegacyPodcastBookmarksWithoutKind(t *testing.T) {
	db := testdb.Open(t)
	if err := db.Exec(`
CREATE TABLE podcast_episode_bookmarks (
	id text PRIMARY KEY,
	created_at datetime,
	updated_at datetime,
	deleted_at datetime,
	user_id text NOT NULL,
	episode_id text NOT NULL
);
`).Error; err != nil {
		t.Fatalf("create legacy podcast bookmarks table: %v", err)
	}
	userID := uuid.NewString()
	episodeID := uuid.NewString()
	if err := db.Exec(`INSERT INTO podcast_episode_bookmarks (id, user_id, episode_id) VALUES (?, ?, ?), (?, ?, ?)`,
		uuid.NewString(), userID, episodeID,
		uuid.NewString(), userID, episodeID,
	).Error; err != nil {
		t.Fatalf("seed legacy duplicate podcast bookmarks: %v", err)
	}

	if err := DeduplicateBlogInteractions(db); err != nil {
		t.Fatalf("deduplicate legacy podcast bookmarks: %v", err)
	}
	var count int
	if err := db.Raw(`SELECT COUNT(*) FROM podcast_episode_bookmarks WHERE user_id = ? AND episode_id = ?`, userID, episodeID).Scan(&count).Error; err != nil {
		t.Fatalf("count legacy podcast bookmarks: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one legacy podcast bookmark after dedupe, got %d", count)
	}
}

func TestRunBlogInteractionUniqueIndexesDeduplicatesExistingRows(t *testing.T) {
	db := testdb.Open(t)

	if err := db.Exec(`
CREATE TABLE likes (
	id text PRIMARY KEY,
	created_at datetime,
	updated_at datetime,
	deleted_at datetime,
	user_id text NOT NULL,
	target_type text NOT NULL,
	target_id text NOT NULL
);
`).Error; err != nil {
		t.Fatalf("create legacy likes table: %v", err)
	}
	if err := db.Exec(`
CREATE TABLE bookmarks (
	id text PRIMARY KEY,
	created_at datetime,
	updated_at datetime,
	deleted_at datetime,
	user_id text NOT NULL,
	post_id text NOT NULL,
	bookmark_folder_id text
);
`).Error; err != nil {
		t.Fatalf("create legacy bookmarks table: %v", err)
	}

	userID := uuid.NewString()
	postID := uuid.NewString()
	if err := db.Exec(`INSERT INTO likes (id, user_id, target_type, target_id) VALUES (?, ?, 'post', ?), (?, ?, 'post', ?)`,
		uuid.NewString(), userID, postID,
		uuid.NewString(), userID, postID,
	).Error; err != nil {
		t.Fatalf("seed duplicate likes: %v", err)
	}
	if err := db.Exec(`INSERT INTO bookmarks (id, user_id, post_id) VALUES (?, ?, ?), (?, ?, ?)`,
		uuid.NewString(), userID, postID,
		uuid.NewString(), userID, postID,
	).Error; err != nil {
		t.Fatalf("seed duplicate bookmarks: %v", err)
	}

	if err := RunBlogInteractionUniqueIndexes(db); err != nil {
		t.Fatalf("run blog interaction unique indexes migration: %v", err)
	}

	var likeCount int64
	if err := db.Model(&model.Like{}).Where("user_id = ? AND target_type = ? AND target_id = ?", userID, "post", postID).Count(&likeCount).Error; err != nil {
		t.Fatalf("count likes: %v", err)
	}
	if likeCount != 1 {
		t.Fatalf("expected 1 like after dedupe, got %d", likeCount)
	}

	var bookmarkCount int64
	if err := db.Model(&model.Bookmark{}).Where("user_id = ? AND post_id = ?", userID, postID).Count(&bookmarkCount).Error; err != nil {
		t.Fatalf("count bookmarks: %v", err)
	}
	if bookmarkCount != 1 {
		t.Fatalf("expected 1 bookmark after dedupe, got %d", bookmarkCount)
	}
}

func TestRunBlogInteractionUniqueIndexesMigratesPodcastBookmarkKinds(t *testing.T) {
	db := testdb.Open(t)
	if err := db.Exec(`
CREATE TABLE podcast_episode_bookmarks (
	id text PRIMARY KEY,
	created_at datetime,
	updated_at datetime,
	deleted_at datetime,
	user_id text NOT NULL,
	episode_id text NOT NULL
);
CREATE UNIQUE INDEX idx_podcast_episode_bookmarks_user_episode
	ON podcast_episode_bookmarks (user_id, episode_id)
	WHERE deleted_at IS NULL;
`).Error; err != nil {
		t.Fatalf("create legacy podcast bookmarks table: %v", err)
	}

	userID := uuid.NewString()
	episodeID := uuid.NewString()
	if err := db.Exec(`INSERT INTO podcast_episode_bookmarks (id, user_id, episode_id) VALUES (?, ?, ?)`, uuid.NewString(), userID, episodeID).Error; err != nil {
		t.Fatalf("seed legacy podcast bookmark: %v", err)
	}
	if err := RunBlogInteractionUniqueIndexes(db); err != nil {
		t.Fatalf("run blog interaction unique indexes migration: %v", err)
	}
	if err := RunBlogInteractionUniqueIndexes(db); err != nil {
		t.Fatalf("rerun blog interaction unique indexes migration: %v", err)
	}
	if db.Migrator().HasIndex("podcast_episode_bookmarks", "idx_podcast_episode_bookmarks_user_episode") {
		t.Fatal("expected legacy podcast bookmark unique index to be removed")
	}
	assertIndexExists(t, db, "podcast_episode_bookmarks", "idx_podcast_episode_bookmarks_user_episode_kind")

	var bookmark model.PodcastEpisodeBookmark
	if err := db.Where("user_id = ? AND episode_id = ?", userID, episodeID).First(&bookmark).Error; err != nil {
		t.Fatalf("load migrated bookmark: %v", err)
	}
	if bookmark.Kind != "favorite" {
		t.Fatalf("expected legacy bookmark kind favorite, got %q", bookmark.Kind)
	}
	if err := db.Create(&model.PodcastEpisodeBookmark{UserID: uuid.MustParse(userID), EpisodeID: uuid.MustParse(episodeID), Kind: "listen_later"}).Error; err != nil {
		t.Fatalf("create listen later bookmark alongside favorite: %v", err)
	}
}

func TestRunBlogInteractionUniqueIndexesDeduplicatesNormalizedPodcastBookmarkKinds(t *testing.T) {
	db := testdb.Open(t)
	if err := db.Exec(`
CREATE TABLE podcast_episode_bookmarks (
	id text PRIMARY KEY,
	created_at datetime,
	updated_at datetime,
	deleted_at datetime,
	user_id text NOT NULL,
	episode_id text NOT NULL,
	kind text
);
`).Error; err != nil {
		t.Fatalf("create podcast bookmarks table: %v", err)
	}

	userID := uuid.NewString()
	episodeID := uuid.NewString()
	if err := db.Exec(`INSERT INTO podcast_episode_bookmarks (id, user_id, episode_id, kind) VALUES (?, ?, ?, NULL), (?, ?, ?, ''), (?, ?, ?, 'favorite')`,
		uuid.NewString(), userID, episodeID,
		uuid.NewString(), userID, episodeID,
		uuid.NewString(), userID, episodeID,
	).Error; err != nil {
		t.Fatalf("seed normalized duplicate podcast bookmarks: %v", err)
	}

	if err := RunBlogInteractionUniqueIndexes(db); err != nil {
		t.Fatalf("run blog interaction unique indexes migration: %v", err)
	}

	var count int
	if err := db.Raw(`SELECT COUNT(*) FROM podcast_episode_bookmarks WHERE user_id = ? AND episode_id = ? AND kind = 'favorite'`, userID, episodeID).Scan(&count).Error; err != nil {
		t.Fatalf("count normalized podcast bookmarks: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one favorite podcast bookmark after normalization, got %d", count)
	}
	assertIndexExists(t, db, "podcast_episode_bookmarks", "idx_podcast_episode_bookmarks_user_episode_kind")
}

func TestPodcastBookmarkMigrationNormalizesKindsBeforeAutoMigrate(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&legacyPodcastBookmarkAutoMigration{}); err != nil {
		t.Fatalf("create legacy podcast bookmarks table: %v", err)
	}

	userID := uuid.New()
	episodeID := uuid.New()
	if err := db.Exec(`INSERT INTO podcast_episode_bookmarks (id, user_id, episode_id, kind) VALUES (?, ?, ?, NULL), (?, ?, ?, ''), (?, ?, ?, 'favorite')`,
		uuid.New(), userID, episodeID,
		uuid.New(), userID, episodeID,
		uuid.New(), userID, episodeID,
	).Error; err != nil {
		t.Fatalf("seed legacy podcast bookmarks: %v", err)
	}

	runMigrationSequence := func() error {
		if err := DeduplicateBlogInteractions(db); err != nil {
			return err
		}
		if err := db.AutoMigrate(&podcastBookmarkAutoMigration{}); err != nil {
			return err
		}
		return RunBlogInteractionUniqueIndexes(db)
	}
	if err := runMigrationSequence(); err != nil {
		t.Fatalf("first migration sequence: %v", err)
	}
	if err := runMigrationSequence(); err != nil {
		t.Fatalf("second migration sequence: %v", err)
	}

	var count int64
	if err := db.Table("podcast_episode_bookmarks").
		Where("user_id = ? AND episode_id = ? AND kind = ?", userID, episodeID, "favorite").
		Count(&count).Error; err != nil {
		t.Fatalf("count migrated podcast bookmarks: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one favorite podcast bookmark, got %d", count)
	}

	var columns []struct {
		Name       string
		NotNull    int    `gorm:"column:notnull"`
		DefaultSQL string `gorm:"column:dflt_value"`
	}
	if err := db.Raw(`PRAGMA table_info('podcast_episode_bookmarks')`).Scan(&columns).Error; err != nil {
		t.Fatalf("inspect podcast bookmark schema: %v", err)
	}
	for _, column := range columns {
		if column.Name != "kind" {
			continue
		}
		if column.NotNull != 1 {
			t.Fatalf("expected kind to be NOT NULL, got notnull=%d", column.NotNull)
		}
		if column.DefaultSQL != "\"favorite\"" && column.DefaultSQL != "'favorite'" {
			t.Fatalf("expected kind default favorite, got %q", column.DefaultSQL)
		}
		return
	}
	t.Fatal("expected kind column")
}
