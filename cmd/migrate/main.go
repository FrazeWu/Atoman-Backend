package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	"gorm.io/gorm"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/migrations"
	"atoman/internal/model"
)

func main() {
	envFile := flag.String("env", ".env.dev", "env file to load before migrating")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Printf("WARN: load %s: %v", *envFile, err)
	}

	dbType := os.Getenv("DATABASE_TYPE")
	if dbType == "" {
		log.Fatal("DATABASE_TYPE is required")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	db, err := app.OpenDB(config.DBConfig{Type: dbType, URL: dbURL})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	if err := runMigrations(db); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	log.Println("migrations completed")
}

func runMigrations(db *gorm.DB) error {
	if err := preparePostgresExtensions(db); err != nil {
		return err
	}

	if err := migrations.DeduplicateSubscriptions(db); err != nil {
		return fmt.Errorf("deduplicate subscriptions: %w", err)
	}

	if err := migrations.RunBlogGuestCommentsMigration(db); err != nil {
		return fmt.Errorf("blog guest comments migration: %w", err)
	}

	if err := migrations.Migrate20260603FeedSourceManagementMVP(db); err != nil {
		return fmt.Errorf("feed source management mvp migration: %w", err)
	}

	if err := migrations.RunBlogCollectionPostOrderMigration(db); err != nil {
		return fmt.Errorf("blog collection post order migration: %w", err)
	}

	if err := migrateSchema(db); err != nil {
		return err
	}

	if err := migrations.RunBlogInteractionUniqueIndexes(db); err != nil {
		return fmt.Errorf("blog interaction unique indexes migration: %w", err)
	}

	if err := migrations.RunContentProtectionLiveUniqueIndex(db); err != nil {
		return fmt.Errorf("content protection live unique index migration: %w", err)
	}

	if err := migrations.RunSubscriptionUniqueIndex(db); err != nil {
		return fmt.Errorf("subscription unique index migration: %w", err)
	}

	if err := migrations.RunFeedItemUniqueIndex(db); err != nil {
		return fmt.Errorf("feed item unique index migration: %w", err)
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

func migrateSchema(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.PostCollection{},
		&model.Comment{},
		&model.AuditLog{},
		&model.MediaAsset{},
		&model.MusicEdit{},
		&model.MusicEditVote{},
		&model.MusicEditDecision{},
		&model.MusicEditChange{},
		&model.BlogPostRating{},
		&model.FeedSource{},
		&model.Subscription{},
		&model.SubscriptionGroup{},
		&model.FeedItem{},
		&model.FeedItemRead{},
		&model.FeedStarGroup{},
		&model.FeedItemStar{},
		&model.ReadingListItem{},
		&model.Notification{},
		&model.DMConversation{},
		&model.DMMessage{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumReply{},
		&model.ForumDraft{},
		&model.ForumLike{},
		&model.ForumBookmark{},
		&model.ForumReport{},
		&model.CategoryRequest{},
		&model.ForumModeratorAssignment{},
		&model.Debate{},
		&model.Argument{},
		&model.DebateVote{},
		&model.VoteHistory{},
		&model.DebateConcludeVote{},
	); err != nil {
		return err
	}

	if err := migrations.RunNotificationDMIndexes(db); err != nil {
		return fmt.Errorf("notification/dm index migration: %w", err)
	}

	return nil
}
