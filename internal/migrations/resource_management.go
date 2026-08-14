package migrations

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var studioDefaultCollectionNames = map[string]string{
	"blog":    "未分类文章",
	"podcast": "未分类单集",
	"video":   "未分类视频",
}

// RunResourceManagementMigration establishes the additive single-collection model.
// Legacy join tables remain available during the compatibility window.
func RunResourceManagementMigration(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		defaults, err := ensureStudioDefaultCollections(tx)
		if err != nil {
			return err
		}
		if err := migrateStudioSettingsScope(tx); err != nil {
			return err
		}
		if err := migratePodcastCollections(tx, defaults); err != nil {
			return err
		}
		if err := migrateVideoCollections(tx, defaults); err != nil {
			return err
		}
		if err := createResourceNameIndexes(tx); err != nil {
			return err
		}
		return createDefaultCollectionProtection(tx)
	})
}

func ensureStudioDefaultCollections(tx *gorm.DB) (map[string]uuid.UUID, error) {
	var channels []model.Channel
	if err := tx.Order("created_at ASC, id ASC").Find(&channels).Error; err != nil {
		return nil, err
	}
	defaults := make(map[string]uuid.UUID, len(channels)*len(studioDefaultCollectionNames))
	for _, channel := range channels {
		for _, contentType := range []string{"blog", "podcast", "video"} {
			var collection model.Collection
			err := tx.Where("channel_id = ? AND content_type = ? AND is_default = ?", channel.ID, contentType, true).
				Order("created_at ASC, id ASC").First(&collection).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				name := studioDefaultCollectionNames[contentType]
				err = tx.Where("channel_id = ? AND content_type = ? AND LOWER(name) = LOWER(?)", channel.ID, contentType, name).
					First(&collection).Error
				if errors.Is(err, gorm.ErrRecordNotFound) {
					collection = model.Collection{
						ChannelID: channel.ID, ContentType: contentType, CreatedBy: channel.UserID,
						Name: name, IsDefault: true,
					}
					err = tx.Create(&collection).Error
				} else if err == nil {
					err = tx.Model(&collection).Update("is_default", true).Error
				}
			}
			if err != nil {
				return nil, fmt.Errorf("ensure %s default collection for channel %s: %w", contentType, channel.ID, err)
			}
			defaults[defaultCollectionKey(channel.ID, contentType)] = collection.ID
		}
	}
	return defaults, nil
}

func migrateStudioSettingsScope(tx *gorm.DB) error {
	var settings []model.StudioModuleSettings
	if err := tx.Order("channel_id ASC, content_type ASC, updated_at DESC, id DESC").Find(&settings).Error; err != nil {
		return err
	}
	var channels []model.Channel
	if err := tx.Find(&channels).Error; err != nil {
		return err
	}
	owners := make(map[uuid.UUID]uuid.UUID, len(channels))
	for _, channel := range channels {
		if channel.UserID != nil {
			owners[channel.ID] = *channel.UserID
		}
	}
	kept := make(map[string]model.StudioModuleSettings, len(settings))
	for _, setting := range settings {
		key := defaultCollectionKey(setting.ChannelID, setting.ContentType)
		current, exists := kept[key]
		if !exists {
			kept[key] = setting
			continue
		}
		ownerID := owners[setting.ChannelID]
		toDelete := setting
		if setting.UserID == ownerID && current.UserID != ownerID {
			toDelete = current
			kept[key] = setting
		}
		if err := tx.Delete(&toDelete).Error; err != nil {
			return fmt.Errorf("remove duplicate studio settings %s: %w", toDelete.ID, err)
		}
	}
	if err := tx.Exec("DROP INDEX IF EXISTS idx_studio_settings_scope").Error; err != nil {
		return err
	}
	return tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_studio_settings_channel_type
		ON studio_module_settings (channel_id, content_type) WHERE deleted_at IS NULL`).Error
}

func migratePodcastCollections(tx *gorm.DB, defaults map[string]uuid.UUID) error {
	var episodes []model.PodcastEpisode
	if err := tx.Preload("Post").Order("season_number ASC, episode_number ASC, created_at ASC, id ASC").Find(&episodes).Error; err != nil {
		return err
	}
	positions := make(map[uuid.UUID]int)
	for _, episode := range episodes {
		if episode.Post == nil {
			return fmt.Errorf("podcast episode %s has no post", episode.ID)
		}
		post := episode.Post
		collectionID, conflict, err := resolveLegacyPostCollection(tx, post, "podcast", defaults)
		if err != nil {
			return err
		}
		updates := map[string]any{"collection_conflict": conflict}
		if collectionID != nil {
			updates["collection_id"] = *collectionID
			updates["collection_position"] = positions[*collectionID]
			positions[*collectionID]++
		}
		if err := tx.Model(&model.Post{}).Where("id = ?", post.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("migrate podcast post %s collection: %w", post.ID, err)
		}
		if collectionID != nil && !conflict {
			if err := syncMigratedPostCollection(tx, post.ID, *collectionID, positions[*collectionID]-1); err != nil {
				return fmt.Errorf("sync podcast post %s collection: %w", post.ID, err)
			}
		}
	}
	return nil
}

func syncMigratedPostCollection(tx *gorm.DB, postID, collectionID uuid.UUID, position int) error {
	if err := tx.Where("post_id = ? AND collection_id <> ?", postID, collectionID).Delete(&model.PostCollection{}).Error; err != nil {
		return err
	}
	link := model.PostCollection{PostID: postID, CollectionID: collectionID, Position: position}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "post_id"}, {Name: "collection_id"}},
		DoUpdates: clause.Assignments(map[string]any{"position": position}),
	}).Create(&link).Error
}

func resolveLegacyPostCollection(tx *gorm.DB, post *model.Post, contentType string, defaults map[string]uuid.UUID) (*uuid.UUID, bool, error) {
	if post.ChannelID == nil {
		return nil, false, fmt.Errorf("published %s post %s has no channel", contentType, post.ID)
	}
	if post.CollectionID != nil {
		if err := validateMigratedCollection(tx, *post.CollectionID, *post.ChannelID, contentType); err != nil {
			return nil, false, err
		}
		return post.CollectionID, false, nil
	}
	var links []model.PostCollection
	if tx.Migrator().HasTable(&model.PostCollection{}) {
		if err := tx.Table("post_collections").
			Joins("JOIN collections ON collections.id = post_collections.collection_id AND collections.deleted_at IS NULL").
			Where("post_collections.post_id = ? AND collections.channel_id = ? AND collections.content_type = ?", post.ID, *post.ChannelID, contentType).
			Order("post_collections.position ASC, post_collections.collection_id ASC").Find(&links).Error; err != nil {
			return nil, false, err
		}
	}
	if len(links) == 1 {
		return &links[0].CollectionID, false, nil
	}
	if len(links) > 1 {
		return nil, true, nil
	}
	if post.Status == "published" {
		id := defaults[defaultCollectionKey(*post.ChannelID, contentType)]
		return &id, false, nil
	}
	return nil, false, nil
}

func migrateVideoCollections(tx *gorm.DB, defaults map[string]uuid.UUID) error {
	var videos []model.Video
	if err := tx.Order("published_at ASC, created_at ASC, id ASC").Find(&videos).Error; err != nil {
		return err
	}
	positions := make(map[uuid.UUID]int)
	for _, video := range videos {
		if video.ChannelID == nil {
			if video.Status == "published" {
				return fmt.Errorf("published video %s has no channel", video.ID)
			}
			continue
		}
		collectionID := video.CollectionID
		conflict := false
		if collectionID != nil {
			if err := validateMigratedCollection(tx, *collectionID, *video.ChannelID, "video"); err != nil {
				return err
			}
		} else {
			var links []model.VideoCollection
			if tx.Migrator().HasTable(&model.VideoCollection{}) {
				if err := tx.Table("video_collections").
					Joins("JOIN collections ON collections.id = video_collections.collection_id AND collections.deleted_at IS NULL").
					Where("video_collections.video_id = ? AND collections.channel_id = ? AND collections.content_type = ?", video.ID, *video.ChannelID, "video").
					Order("video_collections.collection_id ASC").Find(&links).Error; err != nil {
					return err
				}
			}
			switch len(links) {
			case 1:
				collectionID = &links[0].CollectionID
			case 0:
				if video.Status == "published" {
					id := defaults[defaultCollectionKey(*video.ChannelID, "video")]
					collectionID = &id
				}
			default:
				conflict = true
			}
		}
		updates := map[string]any{"collection_conflict": conflict}
		if collectionID != nil {
			updates["collection_id"] = *collectionID
			updates["collection_position"] = positions[*collectionID]
			positions[*collectionID]++
		}
		if err := tx.Model(&model.Video{}).Where("id = ?", video.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("migrate video %s collection: %w", video.ID, err)
		}
		if collectionID != nil && !conflict {
			if err := syncMigratedVideoCollection(tx, video.ID, *collectionID); err != nil {
				return fmt.Errorf("sync video %s collection: %w", video.ID, err)
			}
		}
	}
	return nil
}

func syncMigratedVideoCollection(tx *gorm.DB, videoID, collectionID uuid.UUID) error {
	if err := tx.Where("video_id = ? AND collection_id <> ?", videoID, collectionID).Delete(&model.VideoCollection{}).Error; err != nil {
		return err
	}
	link := model.VideoCollection{VideoID: videoID, CollectionID: collectionID}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error
}

func validateMigratedCollection(tx *gorm.DB, collectionID, channelID uuid.UUID, contentType string) error {
	var count int64
	if err := tx.Model(&model.Collection{}).
		Where("id = ? AND channel_id = ? AND content_type = ?", collectionID, channelID, contentType).
		Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("collection %s is outside %s channel %s", collectionID, contentType, channelID)
	}
	return nil
}

func createResourceNameIndexes(tx *gorm.DB) error {
	if err := rejectCaseInsensitiveNameCollisions(tx, "channels", []string{"LOWER(name)"}); err != nil {
		return err
	}
	if err := rejectCaseInsensitiveNameCollisions(tx, "collections", []string{"channel_id", "content_type", "LOWER(name)"}); err != nil {
		return err
	}
	for _, index := range []string{"idx_channels_name", "idx_collection_channel_type_name"} {
		if err := tx.Exec("DROP INDEX IF EXISTS " + index).Error; err != nil {
			return err
		}
	}
	if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_active_name_ci
		ON channels (LOWER(name)) WHERE deleted_at IS NULL`).Error; err != nil {
		return err
	}
	return tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_collections_active_scope_name_ci
		ON collections (channel_id, content_type, LOWER(name)) WHERE deleted_at IS NULL`).Error
}

func rejectCaseInsensitiveNameCollisions(tx *gorm.DB, table string, columns []string) error {
	group := strings.Join(columns, ", ")
	var collisions []struct{ Count int64 }
	query := fmt.Sprintf("SELECT COUNT(*) AS count FROM %s WHERE deleted_at IS NULL GROUP BY %s HAVING COUNT(*) > 1", table, group)
	if err := tx.Raw(query).Scan(&collisions).Error; err != nil {
		return err
	}
	if len(collisions) > 0 {
		sort.Slice(collisions, func(i, j int) bool { return collisions[i].Count > collisions[j].Count })
		return fmt.Errorf("%s contains %d case-insensitive active name collision groups", table, len(collisions))
	}
	return nil
}

func createDefaultCollectionProtection(tx *gorm.DB) error {
	switch tx.Dialector.Name() {
	case "postgres":
		statements := []string{
			`CREATE OR REPLACE FUNCTION protect_active_default_collection() RETURNS trigger AS $$
			BEGIN
				IF OLD.is_default = TRUE
					AND EXISTS (SELECT 1 FROM channels WHERE id = OLD.channel_id AND deleted_at IS NULL)
				THEN
					IF TG_OP = 'DELETE' THEN
						RAISE EXCEPTION 'active system default collection is protected';
					ELSIF NEW.is_default = FALSE OR (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL) THEN
						RAISE EXCEPTION 'active system default collection is protected';
					END IF;
				END IF;
				IF TG_OP = 'DELETE' THEN
					RETURN OLD;
				END IF;
				RETURN NEW;
			END;
			$$ LANGUAGE plpgsql`,
			`DROP TRIGGER IF EXISTS protect_active_default_collection_update ON collections`,
			`CREATE TRIGGER protect_active_default_collection_update
				BEFORE UPDATE OF is_default, deleted_at ON collections
				FOR EACH ROW EXECUTE FUNCTION protect_active_default_collection()`,
			`DROP TRIGGER IF EXISTS protect_active_default_collection_delete ON collections`,
			`CREATE TRIGGER protect_active_default_collection_delete
				BEFORE DELETE ON collections
				FOR EACH ROW EXECUTE FUNCTION protect_active_default_collection()`,
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	case "sqlite":
		statements := []string{
			`DROP TRIGGER IF EXISTS protect_active_default_collection_update`,
			`CREATE TRIGGER protect_active_default_collection_update
				BEFORE UPDATE OF is_default, deleted_at ON collections
				WHEN OLD.is_default = 1
					AND EXISTS (SELECT 1 FROM channels WHERE id = OLD.channel_id AND deleted_at IS NULL)
					AND (NEW.is_default = 0 OR (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL))
				BEGIN SELECT RAISE(ABORT, 'active system default collection is protected'); END`,
			`DROP TRIGGER IF EXISTS protect_active_default_collection_delete`,
			`CREATE TRIGGER protect_active_default_collection_delete
				BEFORE DELETE ON collections
				WHEN OLD.is_default = 1
					AND EXISTS (SELECT 1 FROM channels WHERE id = OLD.channel_id AND deleted_at IS NULL)
				BEGIN SELECT RAISE(ABORT, 'active system default collection is protected'); END`,
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func defaultCollectionKey(channelID uuid.UUID, contentType string) string {
	return channelID.String() + ":" + contentType
}
