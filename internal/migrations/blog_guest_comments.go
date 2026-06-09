package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunBlogGuestCommentsMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable("comments") {
		return nil
	}

	if !db.Migrator().HasColumn("comments", "guest_name") {
		if err := db.Exec(`ALTER TABLE comments ADD COLUMN guest_name varchar(80)`).Error; err != nil {
			return fmt.Errorf("add comments.guest_name: %w", err)
		}
	}

	switch db.Dialector.Name() {
	case "postgres":
		if err := db.Exec(`ALTER TABLE comments ALTER COLUMN user_id DROP NOT NULL`).Error; err != nil {
			return fmt.Errorf("allow comments.user_id null: %w", err)
		}
	}

	return nil
}
