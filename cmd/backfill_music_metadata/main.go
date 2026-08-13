package main

import (
	"context"
	"flag"
	"log"
	"os"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/modules/music"

	"github.com/joho/godotenv"
)

func main() {
	envFile := flag.String("env", ".env.dev", "env file to load")
	apply := flag.Bool("apply", false, "apply safe matches")
	albumID := flag.String("album-id", "", "only process one album")
	songID := flag.String("song-id", "", "only repair one song's lyrics")
	stripTitlePrefix := flag.String("strip-title-prefix", "", "remove an exact prefix from existing song titles")
	stripArtistPrefixes := flag.Bool("strip-artist-prefixes", false, "remove artist credits before the last title separator")
	preferredReleaseID := flag.String("release-id", "", "validate one MusicBrainz release for the selected album")
	repairTimedLyrics := flag.Bool("repair-timed-lyrics", false, "upgrade existing plain lyrics containing LRC timestamps")
	flag.Parse()
	if err := godotenv.Load(*envFile); err != nil {
		log.Fatalf("load %s: %v", *envFile, err)
	}
	db, err := app.OpenDB(config.DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		log.Fatal(err)
	}
	if *stripTitlePrefix != "" {
		updated, err := music.StripCatalogSongTitlePrefix(context.Background(), db, *stripTitlePrefix)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("updated %d song title(s)", updated)
		return
	}
	if *stripArtistPrefixes {
		updated, err := music.StripCatalogSongArtistPrefixes(context.Background(), db)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("updated %d song title(s)", updated)
		return
	}
	if *repairTimedLyrics {
		results, err := music.RepairCatalogTimedLyrics(context.Background(), db, *apply, *songID)
		if err != nil {
			log.Fatal(err)
		}
		for _, result := range results {
			log.Print(music.FormatCatalogLyricsRepairResult(result))
		}
		return
	}
	results, err := music.BackfillCatalogMetadata(context.Background(), db, os.Getenv("MUSICBRAINZ_USER_AGENT"), *apply, *albumID, *preferredReleaseID)
	if err != nil {
		log.Fatal(err)
	}
	for _, result := range results {
		log.Print(music.FormatCatalogMetadataBackfillResult(result))
	}
}
