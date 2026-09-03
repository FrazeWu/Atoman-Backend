package migrationrunner

import (
	"encoding/json"
	"fmt"
	"strings"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/service"

	"github.com/google/uuid"
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
		{"feed source fetch state migration", migrations.RunFeedSourceFetchStateMigration},
		{"feed crawl optimization migration", migrations.RunFeedCrawlOptimizationMigration},
		{"deduplicate subscriptions", migrations.DeduplicateSubscriptions},
		{"deduplicate subscription groups", migrations.DeduplicateSubscriptionGroups},
		{"subscription unique index repair", migrations.RunSubscriptionUniqueIndex},
		{"feed item unique index repair", migrations.RunFeedItemUniqueIndex},
		{"feed source management mvp migration", migrations.Migrate20260603FeedSourceManagementMVP},
		{"blog collection post order migration", migrations.RunBlogCollectionPostOrderMigration},
		{"deduplicate blog interactions", migrations.DeduplicateBlogInteractions},
		{"auth password reset migration", migrations.RunAuthPasswordResetMigration},
		{"auth oauth migration", migrations.RunAuthOAuthMigration},
		{"unified reading list migration", migrations.RunUnifiedReadingListMigration},
		{"debate wiki migration", migrations.RunDebateWikiMigration},
		{"unified content partial-schema repair", migrations.RunUnifiedContentMigrationIfReady},
		{"music standalone songs pre-schema migration", migrations.RunMusicStandaloneSongsPreSchemaMigration},
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
		{"feed reader content migration", migrations.RunFeedReaderContentMigration},
		{"feed language migration", migrations.RunFeedLanguageMigration},
		{"feed recommendation indexes migration", migrations.RunFeedRecommendationIndexes},
		{"forum draft unique index migration", migrations.RunForumDraftUniqueIndex},
		{"forum search indexes migration", migrations.RunForumSearchIndexes},
		{"blog search indexes migration", migrations.RunBlogSearchIndexes},
		{"revision unique indexes migration", migrations.RunRevisionUniqueIndexes},
		{"music album import v2 migration", migrations.RunMusicAlbumImportV2Migration},
		{"music album import media migration", migrations.RunMusicAlbumImportMediaMigration},
		{"music artist extended fields migration", migrations.RunMusicArtistExtendedFieldsMigration},
		{"music bookmarks playlists migration", migrations.RunMusicBookmarksPlaylistsMigration},
		{"music favorite playlist migration", runMusicFavoritePlaylistMigration},
		{"music play counts migration", migrations.RunMusicPlayCountsMigration},
		{"music lyrics migration", migrations.RunMusicLyricsMigration},
		{"global search indexes migration", migrations.RunGlobalSearchIndexes},
		{"search query indexes migration", migrations.RunSearchQueryIndexes},
		{"music listening migration", migrations.RunMusicListeningMigration},
		{"music recommendation events migration", migrations.RunMusicRecommendationEventsMigration},
		{"music album artist credits migration", migrations.RunMusicAlbumArtistCreditsMigration},
		{"music song credits migration", migrations.RunMusicSongCreditsMigration},
		{"books schema migration", migrations.RunBooksMigration},
		{"music catalog v2 migration", migrations.RunMusicCatalogV2Migration},
		{"music catalog indexes migration", migrations.RunMusicCatalogIndexesMigration},
		{"music partial dates migration", migrations.RunMusicPartialDatesMigration},
		{"music artist drafts migration", migrations.RunMusicArtistDraftsMigration},
		{"music artist album visibility migration", migrations.RunMusicArtistAlbumVisibilityMigration},
		{"music Brainz match migration", migrations.RunMusicBrainzMatchMigration},
		{"music standalone songs constraints migration", migrations.RunMusicStandaloneSongsConstraintsMigration},
		{"music revision baselines migration", runMusicRevisionBaselinesMigration},
		{"unified studio migration", migrations.RunUnifiedStudioMigration},
		{"user default resources migration", backfillUserDefaultResources},
		{"resource management migration", migrations.RunResourceManagementMigration},
		{"unified content migration", migrations.RunUnifiedContentMigration},
		{"blog archive removal migration", migrations.RunBlogArchiveRemovalMigration},
		{"blog rating content migration", migrations.RunBlogRatingContentMigration},
		{"blog bookmark content migration", migrations.RunBlogBookmarkContentMigration},
		{"music contribution evidence backfill", runMusicContributionEvidenceBackfill},
	}
	for _, step := range postSchemaSteps {
		if err := step.run(db); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}

func runMusicContributionEvidenceBackfill(db *gorm.DB) error {
	for _, table := range []any{&model.ReputationRun{}, &model.Revision{}, &model.MusicSongLyricVersion{}, &model.MusicContributionEvent{}, &model.MusicContributionEvidence{}} {
		if !db.Migrator().HasTable(table) {
			return nil
		}
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var revisions []model.Revision
		if err := tx.Where("status = ? AND content_type IN ?", "approved", []string{"artist", "album", "song"}).
			Order("created_at ASC, id ASC").Find(&revisions).Error; err != nil {
			return fmt.Errorf("find music revisions for contribution backfill: %w", err)
		}
		for i := range revisions {
			var existing int64
			if err := tx.Model(&model.MusicContributionEvent{}).
				Where("source_kind = ? AND source_id = ?", "revision", revisions[i].ID).Count(&existing).Error; err != nil {
				return err
			}
			if existing > 0 {
				continue
			}
			if err := service.RecordMusicRevisionContribution(tx, &revisions[i]); err != nil {
				return fmt.Errorf("backfill revision %s: %w", revisions[i].ID, err)
			}
		}

		var versions []model.MusicSongLyricVersion
		if err := tx.Order("created_at ASC, id ASC").Find(&versions).Error; err != nil {
			return fmt.Errorf("find lyric versions for contribution backfill: %w", err)
		}
		for i := range versions {
			var existing int64
			if err := tx.Model(&model.MusicContributionEvent{}).
				Where("source_kind = ? AND source_id = ?", "lyrics", versions[i].ID).Count(&existing).Error; err != nil {
				return err
			}
			if existing > 0 {
				continue
			}

			var previous model.MusicSongLyricVersion
			previousResult := tx.Where("song_id = ? AND version < ?", versions[i].SongID, versions[i].Version).
				Order("version DESC").Limit(1).Find(&previous)
			if previousResult.Error != nil {
				return previousResult.Error
			}
			wasNew := previousResult.RowsAffected == 0
			hadTranslation := !wasNew && strings.TrimSpace(previous.Translation) != ""
			hadTiming := !wasNew && previous.Format == "lrc"
			if err := service.RecordMusicLyricsContribution(tx, &versions[i], wasNew, hadTranslation, hadTiming); err != nil {
				return fmt.Errorf("backfill lyric version %s: %w", versions[i].ID, err)
			}
		}
		return nil
	})
}

func runMusicFavoritePlaylistMigration(db *gorm.DB) error {
	var legacyBookmarks int64
	if db.Migrator().HasTable("music_song_bookmarks") {
		if err := db.Table("music_song_bookmarks").Count(&legacyBookmarks).Error; err != nil {
			return err
		}
	}
	var favoritePlaylists int64
	if db.Migrator().HasTable("music_playlists") {
		if err := db.Table("music_playlists").Where("kind = ?", "favorite").Count(&favoritePlaylists).Error; err != nil {
			return err
		}
	}
	if legacyBookmarks == 0 && favoritePlaylists == 0 {
		return nil
	}
	if err := migrations.RunMusicFavoritePlaylistMigration(db); err != nil {
		return fmt.Errorf("music favorite playlist migration: %w", err)
	}
	return nil
}

func runMusicRevisionBaselinesMigration(db *gorm.DB) error {
	for _, table := range []any{&model.User{}, &model.Artist{}, &model.Album{}, &model.Song{}, &model.Revision{}} {
		if !db.Migrator().HasTable(table) {
			return nil
		}
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		var users []model.User
		if err := tx.Where("is_active = ?", true).
			Order("CASE WHEN role = 'admin' THEN 0 WHEN role = 'moderator' THEN 1 ELSE 2 END").
			Order("created_at ASC, uuid ASC").Find(&users).Error; err != nil {
			return fmt.Errorf("find revision editors: %w", err)
		}
		validEditors := make(map[uuid.UUID]bool, len(users))
		for _, user := range users {
			validEditors[user.UUID] = true
		}

		var artists []model.Artist
		var albums []model.Album
		var songs []model.Song
		if err := tx.Order("created_at ASC, id ASC").Find(&artists).Error; err != nil {
			return fmt.Errorf("find artists for revision baselines: %w", err)
		}
		if err := tx.Order("created_at ASC, id ASC").Find(&albums).Error; err != nil {
			return fmt.Errorf("find albums for revision baselines: %w", err)
		}
		if err := tx.Order("created_at ASC, id ASC").Find(&songs).Error; err != nil {
			return fmt.Errorf("find songs for revision baselines: %w", err)
		}
		if len(artists)+len(albums)+len(songs) == 0 {
			return nil
		}
		if len(users) == 0 {
			return fmt.Errorf("cannot create music revision baselines without a user")
		}

		fallbackEditorID := users[0].UUID
		revisions := service.NewRevisionService(tx)
		for _, artist := range artists {
			editorID := fallbackEditorID
			if artist.CreatedBy != nil && validEditors[*artist.CreatedBy] {
				editorID = *artist.CreatedBy
			}
			if _, err := revisions.EnsureInitialRevision("artist", artist.ID, editorID); err != nil {
				return fmt.Errorf("create artist %s revision baseline: %w", artist.ID, err)
			}
		}
		for _, album := range albums {
			editorID := fallbackEditorID
			if album.UploadedBy != nil && validEditors[*album.UploadedBy] {
				editorID = *album.UploadedBy
			}
			if _, err := revisions.EnsureInitialRevision("album", album.ID, editorID); err != nil {
				return fmt.Errorf("create album %s revision baseline: %w", album.ID, err)
			}
		}
		for _, song := range songs {
			editorID := fallbackEditorID
			if song.UploadedBy != nil && validEditors[*song.UploadedBy] {
				editorID = *song.UploadedBy
			}
			if _, err := revisions.EnsureInitialRevision("song", song.ID, editorID); err != nil {
				return fmt.Errorf("create song %s revision baseline: %w", song.ID, err)
			}
			if song.ReleaseType != nil {
				var current model.Revision
				if err := tx.Where("content_type = ? AND content_id = ? AND is_current = ?", "song", song.ID, true).
					Order("version_number DESC").First(&current).Error; err != nil {
					return fmt.Errorf("load standalone song %s revision: %w", song.ID, err)
				}
				var snapshot map[string]any
				if err := json.Unmarshal(current.ContentSnapshot, &snapshot); err != nil {
					return fmt.Errorf("parse standalone song %s revision: %w", song.ID, err)
				}
				currentType, _ := snapshot["release_type"].(string)
				currentAlbumID, _ := snapshot["album_id"].(string)
				if !strings.EqualFold(strings.TrimSpace(currentType), strings.TrimSpace(*song.ReleaseType)) || strings.TrimSpace(currentAlbumID) != "" {
					if _, err := revisions.CreateCurrentSnapshotRevision("song", song.ID, editorID, "迁移为独立歌曲"); err != nil {
						return fmt.Errorf("refresh standalone song %s revision: %w", song.ID, err)
					}
				}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("music revision baselines migration: %w", err)
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
		&model.User{}, &model.UserSettings{}, &model.AuthSession{}, &model.LoginEvent{}, &model.EmailVerificationCode{},
		&model.ExternalIdentity{}, &model.OAuthFlow{}, &model.Follow{}, &model.Channel{}, &model.Collection{},
		&model.ContentEntry{}, &model.ContentPostExtension{}, &model.ContentBlogExtension{}, &model.ContentBlogVersion{}, &model.ContentBlogDraft{}, &model.BlogMarkdownImport{}, &model.BlogMarkdownImportDiagnostic{}, &model.ContentEpisodeExtension{}, &model.ContentVideoExtension{},
		&model.ContentCollection{}, &model.ContentCollectionMembership{}, &model.LegacyCollectionMapping{},
		&model.UserStudioState{}, &model.StudioModuleSettings{}, &model.StudioMetricEvent{}, &model.StudioInteractionState{}, &model.StudioReplyTemplate{}, &model.StudioGoalCycle{}, &model.StudioGoal{}, &model.StudioGoalAction{}, &model.StudioGoalReview{}, &model.ContentLifecycleEvent{},
		&model.ContentProgress{}, &model.ContentNotificationPreference{}, &model.ContentPublicationEvent{}, &model.BlogPublishSchedule{},
		&model.Post{}, &model.BlogPostVersion{}, &model.PostCollection{}, &model.BlogDraft{}, &model.BlogRecommendationFeedback{}, &model.ShortNote{},
		&model.ShortNoteMedia{}, &model.ShortNoteVote{}, &model.Like{}, &model.PostRating{},
		&model.ReputationRun{}, &model.BlogQualitySnapshot{}, &model.UserReputationSnapshot{},
		&model.MusicContributionEvent{}, &model.MusicContributionEvidence{},
		&model.AuditLog{}, &model.ActivityLog{}, &model.MediaAsset{}, &model.ContentMediaAsset{},
		&model.Artist{}, &model.Album{}, &model.AlbumArtist{}, &model.Song{}, &model.SongArtist{}, &model.SongRating{}, &model.AlbumRating{}, &model.SongCorrection{}, &model.AlbumCorrection{},
		&model.SongAudioReplacement{}, &model.MusicEntryStateRequest{}, &model.MusicEntryStateEvent{},
		&model.ArtistCorrection{}, &model.ArtistAlias{}, &model.ArtistMerge{},
		&model.MusicEdit{}, &model.MusicEditVote{}, &model.MusicEditDecision{}, &model.MusicEditChange{},
		&model.AlbumImportSession{}, &model.AlbumImportFile{}, &model.AlbumImportJob{}, &model.MusicAssetUploadSession{}, &model.MusicExternalImport{},
		&model.ArtistBookmark{}, &model.AlbumBookmark{}, &model.PlaylistBookmark{},
		&model.Playlist{}, &model.PlaylistSong{}, &model.MusicListeningHistory{}, &model.MusicPlaybackSession{}, &model.MusicPlaybackProgress{}, &model.MusicSearchInteraction{}, &model.MusicRecommendationEvent{},
		&model.Bookmark{}, &model.BookmarkFolder{}, &model.ChannelBookmark{}, &model.SiteSetting{}, &model.SiteVisitDaily{},
		&model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookEdit{},
		&model.UserBookImport{}, &model.UserBookAsset{}, &model.UserBookReadingState{}, &model.UserBookShelf{},
		&model.BookPublicationRequest{}, &model.BookRightsDeclaration{}, &model.PublishedBookAsset{}, &model.BookRating{}, &model.BookReview{}, &model.BookPostLink{},
		&model.FeedSource{}, &model.OnboardingFeedRecommendation{}, &model.Subscription{},
		&model.FeedSubscriptionRule{}, &model.FeedPreference{}, &model.SubscriptionGroup{}, &model.SubscriptionHubGroup{}, &model.SubscriptionHubMembership{}, &model.FeedItem{},
		&model.FeedSourceDiagnostic{}, &model.FeedContentFeedback{},
		&model.FeedItemRead{}, &model.FeedStarGroup{}, &model.FeedItemStar{}, &model.ReadingListItem{},
		&model.SourceReadEvent{}, &model.Notification{}, &model.NotificationPreference{}, &model.NotificationMute{}, &model.Revision{}, &model.EditConflict{},
		&model.ContentProtection{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.ForumLike{},
		&model.ForumBookmark{}, &model.ForumFollow{}, &model.ForumReport{}, &model.ForumUserModerationAction{},
		&model.ForumUserTrust{}, &model.CategoryRequest{}, &model.ForumModeratorAssignment{},
		&model.ForumGroup{}, &model.ForumGroupMember{}, &model.ForumCategoryPermission{},
		&model.Video{}, &model.VideoBookmark{}, &model.VideoProcessingJob{}, &model.VideoImportSession{}, &model.VideoTag{},
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
	if db.Dialector.Name() == "postgres" {
		if err := db.Exec("DISCARD PLANS").Error; err != nil {
			return fmt.Errorf("discard PostgreSQL prepared plans after schema migration: %w", err)
		}
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
		{"music wiki state migration", migrations.RunMusicWikiStateMigration},
		{"feed subscription management migration", migrations.RunFeedSubscriptionManagementMigration},
		{"reputation indexes migration", migrations.RunReputationIndexes},
	}
	for _, step := range steps {
		if err := step.run(db); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}
