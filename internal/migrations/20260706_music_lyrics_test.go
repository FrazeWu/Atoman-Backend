package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunMusicLyricsMigrationCreatesSchema(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Song{})

	if err := RunMusicLyricsMigration(db); err != nil {
		t.Fatalf("run music lyrics migration: %v", err)
	}

	for _, table := range []string{
		"music_song_lyrics",
		"music_song_lyric_lines",
		"music_song_lyric_versions",
		"music_lyric_annotations",
		"music_lyric_annotation_votes",
	} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table %s to exist", table)
		}
	}

	for table, columns := range map[string][]string{
		"music_song_lyrics":            {"song_id", "content", "translation", "format", "version", "updated_by", "edit_summary"},
		"music_song_lyric_lines":       {"lyric_id", "line_key", "line_index", "time_ms", "text", "translation"},
		"music_song_lyric_versions":    {"song_id", "version", "content", "translation", "format", "edit_summary", "created_by"},
		"music_lyric_annotations":      {"song_id", "line_id", "selected_text", "start_offset", "end_offset", "body", "created_by", "status"},
		"music_lyric_annotation_votes": {"annotation_id", "user_id", "vote"},
	} {
		for _, column := range columns {
			if !db.Migrator().HasColumn(table, column) {
				t.Fatalf("expected table %s to have column %s", table, column)
			}
		}
	}

	assertIndexExists(t, db, "music_song_lyrics", "idx_music_song_lyrics_song")
	assertIndexExists(t, db, "music_song_lyric_versions", "idx_music_song_lyric_versions_song_version")
	assertIndexExists(t, db, "music_lyric_annotation_votes", "idx_music_lyric_annotation_votes_annotation_user")
}

func TestRunMusicLyricsMigrationEnforcesEnums(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Song{})

	if err := RunMusicLyricsMigration(db); err != nil {
		t.Fatalf("run music lyrics migration: %v", err)
	}

	songID := uuid.New()
	userID := uuid.New()
	if err := db.Create(&model.MusicSongLyric{SongID: songID, Format: "invalid", UpdatedBy: userID}).Error; err == nil {
		t.Fatal("expected invalid lyric format to be rejected")
	}
	if err := db.Create(&model.MusicLyricAnnotation{SongID: songID, LineID: uuid.New(), CreatedBy: userID, Status: "invalid"}).Error; err == nil {
		t.Fatal("expected invalid annotation status to be rejected")
	}
	if err := db.Create(&model.MusicLyricAnnotationVote{AnnotationID: uuid.New(), UserID: userID, Vote: "invalid"}).Error; err == nil {
		t.Fatal("expected invalid annotation vote to be rejected")
	}
}
