package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/model"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

const appleCatalogProvider = "apple_music"

type cleanupSummary struct {
	Albums int64
	Songs  int64
}

func main() {
	envFile := flag.String("env", ".env.dev", "environment file")
	userID := flag.String("user-id", "", "UUID of the catalog owner")
	all := flag.Bool("all", false, "remove every matching hip-hop consensus catalog record")
	apply := flag.Bool("apply", false, "remove matching single, EP, and misattributed catalog records")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Printf("WARN: load %s: %v", *envFile, err)
	}
	db, err := app.OpenDB(config.DBConfig{Type: strings.TrimSpace(os.Getenv("DATABASE_TYPE")), URL: strings.TrimSpace(os.Getenv("DATABASE_URL"))})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	ownerID, err := catalogOwnerID(db, *userID)
	if err != nil {
		log.Fatal(err)
	}

	summary, err := cleanup(context.Background(), db, ownerID, *all, *apply)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("albums=%d songs=%d applied=%t", summary.Albums, summary.Songs, *apply)
	if !*apply {
		log.Print("dry run only; rerun with -apply after reviewing the totals")
	}
}

func cleanup(ctx context.Context, db *gorm.DB, ownerID uuid.UUID, all bool, apply bool) (cleanupSummary, error) {
	var albums []model.Album
	query := db.WithContext(ctx).Distinct().
		Table(`"Albums"`).
		Joins("JOIN album_artists ON album_artists.album_id = \"Albums\".id").
		Joins("JOIN music_catalog_links artist_links ON artist_links.entity_id = album_artists.artist_id").
		Joins("JOIN music_catalog_links album_links ON album_links.entity_id = \"Albums\".id").
		Where("artist_links.provider = ? AND artist_links.entity_type = ? AND artist_links.metadata_json ? 'editorial_sources'", appleCatalogProvider, "artist").
		Where("album_links.provider = ? AND album_links.entity_type = ?", appleCatalogProvider, "album").
		Where(`"Albums".uploaded_by = ?`, ownerID)
	if !all {
		query = query.Where(`"Albums".album_type IN ? OR COALESCE(album_links.metadata_json->>'artistId', '') <> artist_links.external_id`, []string{"single", "ep"})
	}
	query = query.Find(&albums)
	if query.Error != nil {
		return cleanupSummary{}, query.Error
	}
	if len(albums) == 0 {
		return cleanupSummary{}, nil
	}

	albumIDs := make([]uuid.UUID, 0, len(albums))
	for _, album := range albums {
		albumIDs = append(albumIDs, album.ID)
	}
	var songIDs []uuid.UUID
	if err := db.WithContext(ctx).Model(&model.Song{}).Where("album_id IN ?", albumIDs).Pluck("id", &songIDs).Error; err != nil {
		return cleanupSummary{}, err
	}
	summary := cleanupSummary{Albums: int64(len(albumIDs)), Songs: int64(len(songIDs))}
	if !apply {
		return summary, nil
	}

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(songIDs) > 0 {
			if err := tx.Unscoped().Where("provider = ? AND entity_type = ? AND entity_id IN ?", appleCatalogProvider, "song", songIDs).Delete(&model.MusicCatalogLink{}).Error; err != nil {
				return err
			}
			if err := tx.Where("song_id IN ?", songIDs).Delete(&model.SongArtist{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", songIDs).Delete(&model.Song{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Unscoped().Where("provider = ? AND entity_type = ? AND entity_id IN ?", appleCatalogProvider, "album", albumIDs).Delete(&model.MusicCatalogLink{}).Error; err != nil {
			return err
		}
		if err := tx.Where("album_id IN ?", albumIDs).Delete(&model.AlbumArtist{}).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", albumIDs).Delete(&model.Album{}).Error
	})
	return summary, err
}

func catalogOwnerID(db *gorm.DB, rawID string) (uuid.UUID, error) {
	ownerQuery := db
	if value := strings.TrimSpace(rawID); value != "" {
		parsed, err := uuid.Parse(value)
		if err != nil {
			return uuid.Nil, err
		}
		ownerQuery = ownerQuery.Where("uuid = ?", parsed)
	} else {
		username := strings.TrimSpace(os.Getenv("OWNER_USERNAME"))
		if username == "" {
			return uuid.Nil, fmt.Errorf("-user-id or OWNER_USERNAME is required")
		}
		ownerQuery = ownerQuery.Where("username = ?", username)
	}
	var owner model.User
	if err := ownerQuery.First(&owner).Error; err != nil {
		return uuid.Nil, err
	}
	return owner.UUID, nil
}
