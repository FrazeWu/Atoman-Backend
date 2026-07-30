package migrationrunner

import (
	"fmt"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/service"

	"gorm.io/gorm"
)

func Run(db *gorm.DB) error {
	if err := preparePostgresExtensions(db); err != nil {
		return err
	}
	steps := []struct {
		name string
		run  func(*gorm.DB) error
	}{
		{"deduplicate subscriptions", migrations.DeduplicateSubscriptions},
		{"deduplicate subscription groups", migrations.DeduplicateSubscriptionGroups},
		{"feed source management mvp migration", migrations.Migrate20260603FeedSourceManagementMVP},
		{"blog collection post order migration", migrations.RunBlogCollectionPostOrderMigration},
		{"deduplicate blog interactions", migrations.DeduplicateBlogInteractions},
		{"auth password reset migration", migrations.RunAuthPasswordResetMigration},
		{"auth oauth migration", migrations.RunAuthOAuthMigration},
		{"unified reading list migration", migrations.RunUnifiedReadingListMigration},
		{"debate wiki migration", migrations.RunDebateWikiMigration},
	}
	for _, step := range steps {
		if err := step.run(db); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	if err := MigrateSchema(db); err != nil {
		return err
	}
	postSchemaSteps := []struct {
		name string
		run  func(*gorm.DB) error
	}{
		{"auth security migration", migrations.RunAuthSecurityMigration},
		{"unified reading list post-schema migration", migrations.RunUnifiedReadingListMigration},
		{"blog single collection migration", migrations.RunBlogSingleCollectionMigration},
		{"blog bookmark folder migration", migrations.RunBlogBookmarkFolderMigration},
		{"unified comment indexes migration", migrations.RunUnifiedCommentIndexes},
		{"legacy forum replies migration", migrations.MigrateLegacyForumReplies},
		{"blog interaction unique indexes migration", migrations.RunBlogInteractionUniqueIndexes},
		{"content protection live unique index migration", migrations.RunContentProtectionLiveUniqueIndex},
		{"subscription unique index migration", migrations.RunSubscriptionUniqueIndex},
		{"subscription group unique index migration", migrations.RunSubscriptionGroupUniqueIndex},
		{"feed item unique index migration", migrations.RunFeedItemUniqueIndex},
		{"forum draft unique index migration", migrations.RunForumDraftUniqueIndex},
		{"forum search indexes migration", migrations.RunForumSearchIndexes},
		{"revision unique indexes migration", migrations.RunRevisionUniqueIndexes},
		{"music album import v2 migration", migrations.RunMusicAlbumImportV2Migration},
		{"music artist extended fields migration", migrations.RunMusicArtistExtendedFieldsMigration},
		{"music bookmarks playlists migration", migrations.RunMusicBookmarksPlaylistsMigration},
		{"music favorite playlist migration", migrations.RunMusicFavoritePlaylistMigration},
		{"music play counts migration", migrations.RunMusicPlayCountsMigration},
		{"music lyrics migration", migrations.RunMusicLyricsMigration},
		{"music listening migration", migrations.RunMusicListeningMigration},
		{"unified studio migration", migrations.RunUnifiedStudioMigration},
		{"user default resources migration", backfillUserDefaultResources},
	}
	for _, step := range postSchemaSteps {
		if err := step.run(db); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
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

func MigrateSchema(db *gorm.DB) error {
	models := []any{
		&model.User{}, &model.UserSettings{}, &model.AuthSession{}, &model.EmailVerificationCode{},
		&model.ExternalIdentity{}, &model.OAuthFlow{}, &model.Follow{}, &model.Channel{}, &model.Collection{},
		&model.UserStudioState{}, &model.StudioModuleSettings{}, &model.ContentLifecycleEvent{},
		&model.ContentProgress{}, &model.ContentNotificationPreference{}, &model.ContentPublicationEvent{},
		&model.Post{}, &model.BlogPostVersion{}, &model.PostCollection{}, &model.BlogDraft{}, &model.Like{},
		&model.AuditLog{}, &model.ActivityLog{}, &model.MediaAsset{},
		&model.Artist{}, &model.Album{}, &model.Song{}, &model.SongCorrection{}, &model.AlbumCorrection{},
		&model.ArtistCorrection{}, &model.ArtistAlias{}, &model.ArtistMerge{},
		&model.MusicEdit{}, &model.MusicEditVote{}, &model.MusicEditDecision{}, &model.MusicEditChange{},
		&model.AlbumImportSession{}, &model.AlbumImportFile{}, &model.AlbumImportJob{},
		&model.ArtistBookmark{}, &model.AlbumBookmark{}, &model.SongBookmark{}, &model.PlaylistBookmark{},
		&model.Playlist{}, &model.PlaylistSong{}, &model.MusicListeningHistory{},
		&model.Bookmark{}, &model.BookmarkFolder{}, &model.ChannelBookmark{}, &model.SiteSetting{},
		&model.FeedSource{}, &model.OnboardingFeedRecommendation{}, &model.Subscription{},
		&model.FeedSubscriptionRule{}, &model.FeedPreference{}, &model.SubscriptionGroup{}, &model.FeedItem{},
		&model.FeedItemRead{}, &model.FeedStarGroup{}, &model.FeedItemStar{}, &model.ReadingListItem{},
		&model.SourceReadEvent{}, &model.Notification{}, &model.Revision{}, &model.EditConflict{},
		&model.ContentProtection{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.ForumLike{},
		&model.ForumBookmark{}, &model.ForumFollow{}, &model.ForumReport{}, &model.ForumUserModerationAction{},
		&model.ForumUserTrust{}, &model.CategoryRequest{}, &model.ForumModeratorAssignment{},
		&model.ForumGroup{}, &model.ForumGroupMember{}, &model.ForumCategoryPermission{},
		&model.Video{}, &model.VideoBookmark{}, &model.VideoProcessingJob{}, &model.VideoTag{},
		&model.VideoCollection{}, &model.VideoTagRelation{}, &model.PodcastEpisode{}, &model.PodcastEpisodeBookmark{},
		&model.Debate{}, &model.DebateConclusionEvent{}, &model.DebateRevisionReference{},
		&model.DebateRelation{}, &model.DebateVote{}, &model.DiscussionTarget{}, &model.CommentEntry{},
		&model.CommentMention{}, &model.CommentAttachment{}, &model.CommentLike{}, &model.CommentReport{},
		&model.CommentTimeAnchor{}, &model.CommentPublishRecord{}, &model.ContentReference{},
		&model.TimelineEvent{}, &model.TimelinePerson{}, &model.PersonLocation{},
		&model.TimelineRevisionProposal{}, &model.TimelineRevision{},
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
	steps := []struct {
		name string
		run  func(*gorm.DB) error
	}{
		{"user blocks migration", migrations.RunUserBlocksMigration},
		{"notification/dm index migration", migrations.RunNotificationDMIndexes},
		{"content publication event index migration", migrations.RunContentPublicationEventIndexes},
		{"dm v2 migration", migrations.RunDMV2Migration},
		{"content references migration", migrations.RunContentReferencesMigration},
	}
	for _, step := range steps {
		if err := step.run(db); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}
