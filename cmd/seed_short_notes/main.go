package main

import (
	"fmt"
	"log"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/model"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

const seedPrefix = "[演示短话]"

type seedNote struct {
	content  string
	media    []string
	likes    int
	comments int
}

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

	var author model.User
	if err := db.Order("created_at ASC").First(&author).Error; err != nil {
		log.Fatal("seed short notes requires at least one user: ", err)
	}
	users := []model.User{author}
	for index := 1; index <= 8; index++ {
		username := fmt.Sprintf("short-note-demo-%d", index)
		user := model.User{Username: username, Email: username + "@local.test", Password: "seed", DisplayName: fmt.Sprintf("短话读者 %d", index), IsActive: true}
		if err := db.Where("username = ?", username).FirstOrCreate(&user).Error; err != nil {
			log.Fatal("create seed user: ", err)
		}
		users = append(users, user)
	}

	items := []seedNote{
		{content: "今天把阅读器里的未读清到只剩三篇。比起多读一点，更想留下些什么。", likes: 6, comments: 6},
		{content: "午后的光落在键盘上，像一段尚未写完的句子。", media: []string{"https://images.unsplash.com/photo-1515879218367-8466d910aaa4?auto=format&fit=crop&w=900&q=80"}, likes: 8, comments: 9},
		{content: "在一个好界面里，用户不该被提醒系统正在努力工作。", likes: 4, comments: 3},
		{content: "最近常听的三张专辑，适合雨天慢慢走。", media: []string{
			"https://images.unsplash.com/photo-1493225457124-a3eb161ffa5f?auto=format&fit=crop&w=700&q=80",
			"https://images.unsplash.com/photo-1506157786151-b8491531f063?auto=format&fit=crop&w=700&q=80",
			"https://images.unsplash.com/photo-1524368535928-5b5e00ddc76b?auto=format&fit=crop&w=700&q=80",
		}, likes: 7, comments: 14},
		{content: "把复杂留给系统，把确定留给正在做事的人。", likes: 5, comments: 4},
		{content: "此刻的天空。", media: []string{"https://images.unsplash.com/photo-1499346030926-9a72daac6c63?auto=format&fit=crop&w=900&q=80"}, likes: 3, comments: 2},
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		var oldNotes []model.ShortNote
		if err := tx.Where("content LIKE ?", seedPrefix+"%").Find(&oldNotes).Error; err != nil {
			return err
		}
		for _, note := range oldNotes {
			if err := tx.Where("target_type = ? AND target_id = ?", "short_note", note.ID).Delete(&model.Like{}).Error; err != nil {
				return err
			}
			if err := tx.Where("kind = ? AND resource_id = ?", "short_note", note.ID).Delete(&model.DiscussionTarget{}).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Where("short_note_id = ?", note.ID).Delete(&model.ShortNoteMedia{}).Error; err != nil {
				return err
			}
		}
		if len(oldNotes) > 0 {
			if err := tx.Unscoped().Delete(&oldNotes).Error; err != nil {
				return err
			}
		}

		for _, item := range items {
			note := model.ShortNote{UserID: author.UUID, Content: seedPrefix + item.content}
			if err := tx.Create(&note).Error; err != nil {
				return err
			}
			for position, url := range item.media {
				if err := tx.Create(&model.ShortNoteMedia{ShortNoteID: note.ID, URL: url, Position: position}).Error; err != nil {
					return err
				}
			}
			if err := tx.Create(&model.DiscussionTarget{Kind: "short_note", ResourceID: note.ID, ResourceKey: note.ID.String(), OwnerID: &author.UUID, CommentCount: item.comments, RootCount: item.comments, NextFloor: item.comments + 1}).Error; err != nil {
				return err
			}
			for index := 0; index < item.likes; index++ {
				viewer := users[index%len(users)]
				like := model.Like{UserID: viewer.UUID, TargetType: "short_note", TargetID: note.ID}
				like.ID = uuid.New()
				if err := tx.Create(&like).Error; err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("seeded %d short notes for %s\n", len(items), author.Username)
}
