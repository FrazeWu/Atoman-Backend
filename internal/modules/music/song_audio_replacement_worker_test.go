package music

import (
	"context"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunSongAudioReplacementOnceCreatesRevisionBeforeSwitchingAudio(t *testing.T) {
	db := testdb.OpenPostgres(t, "music_audio_replacement")
	testdb.Migrate(t, db,
		&model.User{}, &model.Song{}, &model.SongAudioReplacement{}, &model.Revision{}, &model.EditConflict{},
		&model.MusicSongLyric{}, &model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{}, &model.MusicLyricAnnotationVote{},
	)
	requester := model.User{Username: "audio-replacement", Email: "audio-replacement@example.test", Password: "hash", IsActive: true}
	if err := db.Create(&requester).Error; err != nil {
		t.Fatalf("create requester: %v", err)
	}
	song := model.Song{Title: "Track", AudioURL: "/old.mp3", Status: "open", TrackNumber: 1, DiscNumber: 1}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	job := model.SongAudioReplacement{
		SongID: song.ID, RequestedBy: requester.UUID, AudioURL: "/new.mp3",
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

func TestSongAudioReplacementPromotesUploadIntoAlbumRevisionDirectory(t *testing.T) {
	db := testdb.OpenPostgres(t, "music_audio_replacement_promotion")
	testdb.Migrate(t, db,
		&model.User{}, &model.Album{}, &model.Song{}, &model.SongAudioReplacement{}, &model.Revision{}, &model.EditConflict{},
		&model.MusicSongLyric{}, &model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{}, &model.MusicLyricAnnotationVote{},
	)
	requester := model.User{Username: "audio-promotion", Email: "audio-promotion@example.test", Password: "hash", IsActive: true}
	if err := db.Create(&requester).Error; err != nil {
		t.Fatal(err)
	}
	album := model.Album{Title: "Album", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatal(err)
	}
	song := model.Song{Title: "Track", AlbumID: &album.ID, AudioURL: "https://cdn.atoman.test/music/albums/old/track.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	uploadKey := "music/audio/uploads/users/" + requester.UUID.String() + "/replacement.flac"
	job := model.SongAudioReplacement{
		SongID: song.ID, RequestedBy: requester.UUID, AudioURL: "https://cdn.atoman.test/" + uploadKey,
		PreviousAudioURL: song.AudioURL, Status: "pending",
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	var copiedSources, copiedDestinations, deletedKeys []string
	svc := NewServiceWithS3(db, fakeMusicPromotionS3Client(t, &copiedSources, &copiedDestinations, &deletedKeys))
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.atoman.test")

	processed, err := runSongAudioReplacementOnce(context.Background(), db, "test-worker", svc)
	if err != nil || !processed {
		t.Fatalf("run replacement: processed=%v err=%v", processed, err)
	}
	if err := db.First(&song, "id = ?", song.ID).Error; err != nil {
		t.Fatal(err)
	}
	wantPrefix := "https://cdn.atoman.test/music/albums/" + album.ID.String() + "/tracks/" + song.ID.String() + "/"
	if !strings.HasPrefix(song.AudioURL, wantPrefix) || !strings.HasSuffix(song.AudioURL, ".flac") {
		t.Fatalf("audio was not promoted into album directory: %s", song.AudioURL)
	}
	if len(copiedDestinations) != 1 || !containsString(deletedKeys, uploadKey) {
		t.Fatalf("unexpected storage operations: copied=%v deleted=%v", copiedDestinations, deletedKeys)
	}
}
