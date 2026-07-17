package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	"gorm.io/gorm"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/service"
)

func main() {
	envFile := flag.String("env", ".env.dev", "env file to load before migrating")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Printf("WARN: load %s: %v", *envFile, err)
	}

	dbType := os.Getenv("DATABASE_TYPE")
	if dbType == "" {
		log.Fatal("DATABASE_TYPE is required")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	db, err := app.OpenDB(config.DBConfig{Type: dbType, URL: dbURL})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	if err := runMigrations(db); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	log.Println("migrations completed")
}

func runMigrations(db *gorm.DB) error {
	if err := preparePostgresExtensions(db); err != nil {
		return err
	}

	if err := migrations.DeduplicateSubscriptions(db); err != nil {
		return fmt.Errorf("deduplicate subscriptions: %w", err)
	}

	if err := migrations.DeduplicateSubscriptionGroups(db); err != nil {
		return fmt.Errorf("deduplicate subscription groups: %w", err)
	}

	if err := migrations.Migrate20260603FeedSourceManagementMVP(db); err != nil {
		return fmt.Errorf("feed source management mvp migration: %w", err)
	}

	if err := migrations.RunBlogCollectionPostOrderMigration(db); err != nil {
		return fmt.Errorf("blog collection post order migration: %w", err)
	}

	if err := migrations.DeduplicateBlogInteractions(db); err != nil {
		return fmt.Errorf("deduplicate blog interactions: %w", err)
	}
	if err := migrations.RunUnifiedReadingListMigration(db); err != nil {
		return fmt.Errorf("unified reading list migration: %w", err)
	}

	if err := migrateSchema(db); err != nil {
		return err
	}
	if err := migrations.RunUnifiedReadingListMigration(db); err != nil {
		return fmt.Errorf("unified reading list post-schema migration: %w", err)
	}
	if err := migrations.RunBlogSingleCollectionMigration(db); err != nil {
		return fmt.Errorf("blog single collection migration: %w", err)
	}
	if err := migrations.RunBlogBookmarkFolderMigration(db); err != nil {
		return fmt.Errorf("blog bookmark folder migration: %w", err)
	}

	if err := migrations.RunUnifiedCommentIndexes(db); err != nil {
		return fmt.Errorf("unified comment indexes migration: %w", err)
	}
	if err := migrations.MigrateLegacyForumReplies(db); err != nil {
		return fmt.Errorf("legacy forum replies migration: %w", err)
	}

	if err := migrations.RunChannelDefaultSelectionMigration(db); err != nil {
		return fmt.Errorf("channel default selection migration: %w", err)
	}

	if err := migrations.RunBlogInteractionUniqueIndexes(db); err != nil {
		return fmt.Errorf("blog interaction unique indexes migration: %w", err)
	}

	if err := migrations.RunBlogDefaultUniqueIndexes(db); err != nil {
		return fmt.Errorf("blog default unique indexes migration: %w", err)
	}

	if err := migrations.RunContentProtectionLiveUniqueIndex(db); err != nil {
		return fmt.Errorf("content protection live unique index migration: %w", err)
	}

	if err := migrations.RunSubscriptionUniqueIndex(db); err != nil {
		return fmt.Errorf("subscription unique index migration: %w", err)
	}

	if err := migrations.RunSubscriptionGroupUniqueIndex(db); err != nil {
		return fmt.Errorf("subscription group unique index migration: %w", err)
	}

	if err := migrations.RunFeedItemUniqueIndex(db); err != nil {
		return fmt.Errorf("feed item unique index migration: %w", err)
	}

	if err := migrations.RunForumDraftUniqueIndex(db); err != nil {
		return fmt.Errorf("forum draft unique index migration: %w", err)
	}

	if err := migrations.RunForumSearchIndexes(db); err != nil {
		return fmt.Errorf("forum search indexes migration: %w", err)
	}

	if err := migrations.RunRevisionUniqueIndexes(db); err != nil {
		return fmt.Errorf("revision unique indexes migration: %w", err)
	}

	if err := migrations.RunMusicAlbumImportsMigration(db); err != nil {
		return fmt.Errorf("music album imports migration: %w", err)
	}

	if err := migrations.RunMusicArtistExtendedFieldsMigration(db); err != nil {
		return fmt.Errorf("music artist extended fields migration: %w", err)
	}

	if err := migrations.RunMusicBookmarksPlaylistsMigration(db); err != nil {
		return fmt.Errorf("music bookmarks playlists migration: %w", err)
	}
	if err := migrations.RunMusicFavoritePlaylistMigration(db); err != nil {
		return fmt.Errorf("music favorite playlist migration: %w", err)
	}

	if err := migrations.RunMusicPlayCountsMigration(db); err != nil {
		return fmt.Errorf("music play counts migration: %w", err)
	}
	if err := backfillUserDefaultResources(db); err != nil {
		return fmt.Errorf("user default resources migration: %w", err)
	}

	return nil
}

func backfillUserDefaultResources(db *gorm.DB) error {
	var users []model.User
	if err := db.Find(&users).Error; err != nil {
		return err
	}
	for _, user := range users {
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.FirstOrCreate(&model.UserSettings{UserID: user.UUID}, model.UserSettings{UserID: user.UUID}).Error; err != nil {
				return err
			}
			return service.NewUserBootstrapService(tx).EnsureDefaults(user.UUID, user.Username)
		}); err != nil {
			return err
		}
	}
	return nil
}

func preparePostgresExtensions(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS ltree").Error; err != nil {
		return fmt.Errorf("enable ltree extension: %w", err)
	}
	return nil
}

func migrateSchema(db *gorm.DB) error {
	models := []any{
		&model.User{},
		&model.UserSettings{},
		&model.EmailVerificationCode{},
		&model.Channel{},
		&model.UserDefaultChannel{},
		&model.Collection{},
		&model.Post{},
		&model.BlogPostVersion{},
		&model.PostCollection{},
		&model.AuditLog{},
		&model.MediaAsset{},
		&model.MusicEdit{},
		&model.MusicEditVote{},
		&model.MusicEditDecision{},
		&model.MusicEditChange{},
		&model.ArtistBookmark{},
		&model.AlbumBookmark{},
		&model.SongBookmark{},
		&model.Playlist{},
		&model.PlaylistSong{},
		&model.Bookmark{},
		&model.BookmarkFolder{},
		&model.ChannelBookmark{},
		&model.SiteSetting{},
		&model.FeedSource{},
		&model.Subscription{},
		&model.SubscriptionGroup{},
		&model.FeedItem{},
		&model.FeedItemRead{},
		&model.FeedStarGroup{},
		&model.FeedItemStar{},
		&model.ReadingListItem{},
		&model.SourceReadEvent{},
		&model.Notification{},
		&model.DMConversation{},
		&model.DMMessage{},
		&model.Revision{},
		&model.EditConflict{},
		&model.ContentProtection{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumLike{},
		&model.ForumBookmark{},
		&model.ForumFollow{},
		&model.ForumReport{},
		&model.ForumUserModerationAction{},
		&model.ForumUserTrust{},
		&model.CategoryRequest{},
		&model.ForumModeratorAssignment{},
		&model.ForumGroup{},
		&model.ForumGroupMember{},
		&model.ForumCategoryPermission{},
		&model.Video{},
		&model.VideoBookmark{},
		&model.VideoProcessingJob{},
		&model.VideoTag{},
		&model.VideoCollection{},
		&model.VideoTagRelation{},
		&model.PodcastEpisode{},
		&model.PodcastEpisodeBookmark{},
		&model.Debate{},
		&model.DebateVote{},
		&model.VoteHistory{},
		&model.DebateConcludeVote{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentMention{},
		&model.CommentAttachment{},
		&model.CommentLike{},
		&model.CommentReport{},
		&model.CommentTimeAnchor{},
		&model.CommentPublishRecord{},
		&model.TimelineRevisionProposal{},
		&model.TimelineRevision{},
		&model.DebateArgumentDetail{},
		&model.DebateArgumentReference{},
		&model.DebateArgumentDebateRef{},
	}
	if !db.Migrator().HasTable(&model.ForumDraft{}) {
		models = append(models, &model.ForumDraft{})
	}
	if err := migrations.RunForumReportUniqueIndex(db); err != nil {
		return fmt.Errorf("forum report unique index: %w", err)
	}

	if err := db.AutoMigrate(models...); err != nil {
		return err
	}

	if err := migrations.RunNotificationDMIndexes(db); err != nil {
		return fmt.Errorf("notification/dm index migration: %w", err)
	}

	return nil
}
