package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicRecommendationEventsMigrationCreatesTable(t *testing.T) {
	db := testdb.Open(t)
	if err := RunMusicRecommendationEventsMigration(db); err != nil {
		t.Fatalf("run recommendation events migration: %v", err)
	}
	if !db.Migrator().HasTable(&model.MusicRecommendationEvent{}) {
		t.Fatal("expected music_recommendation_events table to exist")
	}
	if err := RunMusicRecommendationEventsMigration(db); err != nil {
		t.Fatalf("run idempotent recommendation events migration: %v", err)
	}
}
