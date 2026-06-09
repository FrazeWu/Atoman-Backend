package main

import (
	"flag"
	"log"
	"os"

	"github.com/joho/godotenv"

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

	if err := migrations.DeduplicateSubscriptions(db); err != nil {
		log.Fatalf("deduplicate subscriptions: %v", err)
	}

	if err := migrations.RunBlogGuestCommentsMigration(db); err != nil {
		log.Fatalf("blog guest comments migration: %v", err)
	}

	if err := migrations.Migrate20260603FeedSourceManagementMVP(db); err != nil {
		log.Fatalf("feed source management mvp migration: %v", err)
	}

	if err := db.AutoMigrate(
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
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
		log.Fatalf("run migrations: %v", err)
	}

	log.Println("migrations completed")
}
