package migrationrunner

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunCreatesCoreSchema(t *testing.T) {
	db := testdb.Open(t)

	if err := Run(db); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, schemaModel := range []any{
		&model.User{},
		&model.AuthSession{},
		&model.Artist{},
		&model.Album{},
		&model.Song{},
		&model.SongCorrection{},
		&model.AlbumCorrection{},
		&model.ArtistCorrection{},
		&model.ArtistAlias{},
		&model.ArtistMerge{},
		&model.BlogDraft{},
		&model.Like{},
		&model.Follow{},
		&model.ActivityLog{},
		&model.TimelineEvent{},
		&model.TimelinePerson{},
		&model.PersonLocation{},
		&model.FeedSource{},
		&model.DMConversation{},
		&model.DiscussionTarget{},
	} {
		if !db.Migrator().HasTable(schemaModel) {
			t.Fatalf("expected table for %T", schemaModel)
		}
	}
	if !db.Migrator().HasIndex("content_publication_events", "idx_content_publication_events_dispatch_candidates") {
		t.Fatal("expected content publication dispatch candidate index")
	}
}
