package content

import (
	"sync"

	"atoman/internal/model"

	"gorm.io/gorm"
)

// MediaSchemaCapabilities describes the canonical columns required by media queries.
// It is cached per *sql.DB because GORM clones preserve the same underlying pool.
type MediaSchemaCapabilities struct {
	ContentEntryTable                bool
	ContentEntryAuthor               bool
	ContentBlogExtensionTable        bool
	ContentVideoExtensionTable       bool
	ContentVideoExtensionMedia       bool
	ContentEpisodeExtensionTable     bool
	ContentEpisodeExtensionData      bool
	ContentEpisodeExtensionLegacyID  bool
	ContentCollectionTable           bool
	ContentCollectionMembershipTable bool
}

var mediaSchemaCache sync.Map // map[*sql.DB]MediaSchemaCapabilities

func CurrentMediaSchema(db *gorm.DB) MediaSchemaCapabilities {
	if sqlDB, err := db.DB(); err == nil {
		if cached, ok := mediaSchemaCache.Load(sqlDB); ok {
			return cached.(MediaSchemaCapabilities)
		}
		capabilities := detectMediaSchema(db)
		actual, _ := mediaSchemaCache.LoadOrStore(sqlDB, capabilities)
		return actual.(MediaSchemaCapabilities)
	}
	return detectMediaSchema(db)
}

// RefreshMediaSchema invalidates the cached capability profile after migrations.
func RefreshMediaSchema(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		mediaSchemaCache.Delete(sqlDB)
	}
}

func detectMediaSchema(db *gorm.DB) MediaSchemaCapabilities {
	contentEntryTable := db.Migrator().HasTable(&model.ContentEntry{})
	videoExtensionTable := db.Migrator().HasTable(&model.ContentVideoExtension{})
	episodeExtensionTable := db.Migrator().HasTable(&model.ContentEpisodeExtension{})
	return MediaSchemaCapabilities{
		ContentEntryTable:                contentEntryTable,
		ContentEntryAuthor:               contentEntryTable && db.Migrator().HasColumn(&model.ContentEntry{}, "AuthorID"),
		ContentBlogExtensionTable:        db.Migrator().HasTable(&model.ContentBlogExtension{}),
		ContentVideoExtensionTable:       videoExtensionTable,
		ContentVideoExtensionMedia:       videoExtensionTable && db.Migrator().HasColumn(&model.ContentVideoExtension{}, "StorageType"),
		ContentEpisodeExtensionTable:     episodeExtensionTable,
		ContentEpisodeExtensionData:      episodeExtensionTable && db.Migrator().HasColumn(&model.ContentEpisodeExtension{}, "Shownotes"),
		ContentEpisodeExtensionLegacyID:  episodeExtensionTable && db.Migrator().HasColumn(&model.ContentEpisodeExtension{}, "LegacyPostID"),
		ContentCollectionTable:           db.Migrator().HasTable(&model.ContentCollection{}),
		ContentCollectionMembershipTable: db.Migrator().HasTable(&model.ContentCollectionMembership{}),
	}
}

func ContentEntryAuthorColumn(db *gorm.DB, alias string) string {
	if CurrentMediaSchema(db).ContentEntryAuthor {
		return alias + ".author_id"
	}
	return "legacy_" + alias + ".user_id"
}

func VideoAuthorColumn(db *gorm.DB) string {
	capabilities := CurrentMediaSchema(db)
	if !capabilities.ContentEntryTable || !capabilities.ContentVideoExtensionTable {
		return "posts.user_id"
	}
	if capabilities.ContentEntryAuthor {
		return "posts.author_id"
	}
	return "legacy_videos.user_id"
}

func PodcastAuthorColumn(db *gorm.DB) string {
	capabilities := CurrentMediaSchema(db)
	if !capabilities.ContentEntryTable || !capabilities.ContentEpisodeExtensionTable {
		return "posts.user_id"
	}
	if capabilities.ContentEntryAuthor {
		return "posts.author_id"
	}
	return "legacy_posts.user_id"
}
