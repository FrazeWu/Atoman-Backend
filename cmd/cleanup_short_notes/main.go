package main

import (
	"fmt"
	"log"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/model"

	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

const demoPrefix = "[演示短话]"

func main() {
	if err := godotenv.Load(".env.prod"); err != nil {
		log.Fatalf("load .env.prod: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	db, err := app.OpenDB(cfg.DB)
	if err != nil {
		log.Fatal(err)
	}

	var notes []model.ShortNote
	if err := db.Where("content LIKE ?", demoPrefix+"%").Find(&notes).Error; err != nil {
		log.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		for _, note := range notes {
			if err := tx.Unscoped().Where("target_type = ? AND target_id = ?", "short_note", note.ID).Delete(&model.Like{}).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Where("kind = ? AND resource_id = ?", "short_note", note.ID).Delete(&model.DiscussionTarget{}).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Where("short_note_id = ?", note.ID).Delete(&model.ShortNoteMedia{}).Error; err != nil {
				return err
			}
		}
		if len(notes) > 0 {
			if err := tx.Unscoped().Delete(&notes).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("username LIKE ?", "short-note-demo-%").Delete(&model.User{}).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("removed %d demo short notes\n", len(notes))
}
