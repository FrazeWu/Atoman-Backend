package migrations

import (
	"errors"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func RunBlogBookmarkFolderMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.Bookmark{}) || !db.Migrator().HasTable(&model.BookmarkFolder{}) {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var legacyFolders []model.BookmarkFolder
		if err := tx.Where("name = ?", "默认收藏").Find(&legacyFolders).Error; err != nil {
			return err
		}
		for _, legacy := range legacyFolders {
			var canonical model.BookmarkFolder
			err := tx.Where("user_id = ? AND name = ?", legacy.UserID, "默认收藏夹").First(&canonical).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Model(&legacy).Update("name", "默认收藏夹").Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if err := tx.Model(&model.Bookmark{}).Where("bookmark_folder_id = ?", legacy.ID).
				Update("bookmark_folder_id", canonical.ID).Error; err != nil {
				return err
			}
			if err := tx.Delete(&legacy).Error; err != nil {
				return err
			}
		}

		var userIDs []uuid.UUID
		if err := tx.Model(&model.Bookmark{}).Where("bookmark_folder_id IS NULL").Distinct("user_id").Scan(&userIDs).Error; err != nil {
			return err
		}
		for _, userID := range userIDs {
			folder := model.BookmarkFolder{UserID: userID, Name: "默认收藏夹"}
			if err := tx.Where("user_id = ? AND name = ?", userID, folder.Name).FirstOrCreate(&folder).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.Bookmark{}).Where("user_id = ? AND bookmark_folder_id IS NULL", userID).Update("bookmark_folder_id", folder.ID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
