package music

import (
	"context"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunSongAudioReplacementOnceCreatesRevisionBeforeSwitchingAudio(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.Song{}, &model.SongAudioReplacement{}, &model.Revision{}, &model.EditConflict{},
		&model.MusicSongLyric{}, &model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{}, &model.MusicLyricAnnotationVote{},
	)
	song := model.Song{Title: "Track", AudioURL: "/old.mp3", Status: "open", TrackNumber: 1, DiscNumber: 1}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	job := model.SongAudioReplacement{
		SongID: song.ID, RequestedBy: uuid.New(), AudioURL: "/new.mp3",
		PreviousAudioURL: song.AudioURL, Status: "pending",
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("create replacement: %v", err)
	}

	processed, err := RunSongAudioReplacementOnce(context.Background(), db, "test-worker")
	if err != nil || !processed {
		t.Fatalf("run replacement: processed=%v err=%v", processed, err)
	}
	if err := db.First(&song, "id = ?", song.ID).Error; err != nil {
		t.Fatalf("reload song: %v", err)
	}
	if err := db.First(&job, "id = ?", job.ID).Error; err != nil {
		t.Fatalf("reload replacement: %v", err)
	}
	if song.AudioURL != "/new.mp3" || job.Status != "completed" || job.RevisionID == nil {
		t.Fatalf("unexpected replacement result: song=%#v job=%#v", song, job)
	}
	var revisions []model.Revision
	if err := db.Where("content_type = ? AND content_id = ?", "song", song.ID).Order("version_number").Find(&revisions).Error; err != nil {
		t.Fatalf("load revisions: %v", err)
	}
	if len(revisions) != 2 || revisions[0].IsCurrent || !revisions[1].IsCurrent || revisions[1].EditType != "audio_replace" {
		t.Fatalf("unexpected revisions: %#v", revisions)
	}
}
