package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunMusicBrainzMatchMigration adds the durable MusicBrainz match audit fields
// used by catalog backfills and future album imports.
func RunMusicBrainzMatchMigration(db *gorm.DB) error {
	return db.AutoMigrate(&model.Album{})
}
