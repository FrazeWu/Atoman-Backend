package migrations

import (
	"atoman/internal/model"
	"gorm.io/gorm"
)

// 仅添加版本和历史结构，不删除或重建现有标签、投票及关联。
func RunMusicTagWikiMigration(db *gorm.DB) error {
	return db.AutoMigrate(&model.MusicTag{}, &model.MusicTagAssignment{}, &model.MusicTagRevision{})
}
