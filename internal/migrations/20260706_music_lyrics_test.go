package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"gorm.io/gorm"
)

func TestRunMusicLyricsMigrationCreatesSchema(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Song{})

	if err := RunMusicLyricsMigration(db); err != nil {
		t.Fatalf("run music lyrics migration: %v", err)
	}
	if err := RunMusicLyricsMigration(db); err != nil {
		t.Fatalf("rerun music lyrics migration: %v", err)
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
	assertIndexExists(t, db, "music_song_lyric_lines", "idx_music_song_lyric_lines_line_key")
	assertIndexExists(t, db, "music_song_lyric_versions", "idx_music_song_lyric_versions_song_version")
	assertIndexExists(t, db, "music_lyric_annotations", "idx_music_lyric_annotations_status")
	assertIndexExists(t, db, "music_lyric_annotation_votes", "idx_music_lyric_annotation_votes_annotation_user")
}

func TestRunMusicLyricsMigrationEnforcesEnums(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Song{})

	if err := RunMusicLyricsMigration(db); err != nil {
		t.Fatalf("run music lyrics migration: %v", err)
	}

	_, _, lyric, _, annotation := createMusicLyricTestFixture(t, db)
	vote := model.MusicLyricAnnotationVote{AnnotationID: annotation.ID, UserID: annotation.CreatedBy, Vote: "up"}
	if err := db.Create(&vote).Error; err != nil {
		t.Fatalf("create annotation vote: %v", err)
	}

	if err := db.Model(&lyric).Update("format", "invalid").Error; err == nil {
		t.Fatal("expected invalid lyric format to be rejected")
	}
	if err := db.Model(&annotation).Update("status", "invalid").Error; err == nil {
		t.Fatal("expected invalid annotation status to be rejected")
	}
	if err := db.Model(&vote).Update("vote", "invalid").Error; err == nil {
		t.Fatal("expected invalid annotation vote to be rejected")
	}
}

func TestRunMusicLyricsMigrationAllowsVoteAfterSoftDelete(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Song{})

	if err := RunMusicLyricsMigration(db); err != nil {
		t.Fatalf("run music lyrics migration: %v", err)
	}

	user, _, _, _, annotation := createMusicLyricTestFixture(t, db)
	first := model.MusicLyricAnnotationVote{AnnotationID: annotation.ID, UserID: user.UUID, Vote: "up"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create initial annotation vote: %v", err)
	}
	if err := db.Delete(&first).Error; err != nil {
		t.Fatalf("soft delete annotation vote: %v", err)
	}

	second := model.MusicLyricAnnotationVote{AnnotationID: annotation.ID, UserID: user.UUID, Vote: "down"}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("recreate annotation vote after soft delete: %v", err)
	}
}

func createMusicLyricTestFixture(t *testing.T, db *gorm.DB) (model.User, model.Song, model.MusicSongLyric, model.MusicSongLyricLine, model.MusicLyricAnnotation) {
	t.Helper()

	user := model.User{Username: "lyrics-migration-user", Email: "lyrics-migration@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	song := model.Song{Title: "Lyrics Migration Song", AudioURL: "https://example.com/song.mp3"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	lyric := model.MusicSongLyric{SongID: song.ID, Content: "line", Format: "plain", UpdatedBy: user.UUID}
	if err := db.Create(&lyric).Error; err != nil {
		t.Fatalf("create lyric: %v", err)
	}
	line := model.MusicSongLyricLine{LyricID: lyric.ID, LineKey: "line-1", LineIndex: 0, Text: "line"}
	if err := db.Create(&line).Error; err != nil {
		t.Fatalf("create lyric line: %v", err)
	}
	annotation := model.MusicLyricAnnotation{
		SongID:       song.ID,
		LineID:       line.ID,
		SelectedText: "line",
		StartOffset:  0,
		EndOffset:    4,
		Body:         "annotation",
		CreatedBy:    user.UUID,
		Status:       "active",
	}
	if err := db.Create(&annotation).Error; err != nil {
		t.Fatalf("create annotation: %v", err)
	}

	return user, song, lyric, line, annotation
}
