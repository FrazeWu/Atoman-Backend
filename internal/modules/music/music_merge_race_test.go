package music

import (
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"context"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
	"sync"
	"testing"
	"time"
)

func TestPlaybackProgressWaitsForSongMergeAndRejectsStaleWrite(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	song := model.Song{Title: "progress race", AudioURL: "/race.mp3", AudioStatus: "ready", LifecycleStatus: model.MusicLifecycleActive}
	require.NoError(t, db.Create(&song).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	lock := db.Begin()
	require.NoError(t, lock.Error)
	defer lock.Rollback()
	var held model.Song
	require.NoError(t, lock.Clauses(clause.Locking{Strength: "UPDATE"}).First(&held, "id = ?", song.ID).Error)
	result := make(chan error, 1)
	go func() {
		_, err := svc.SavePlaybackProgress(user, SavePlaybackProgressRequest{SongID: song.ID, PositionSeconds: 40, DurationSeconds: 180})
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("progress write returned while song row was locked: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	require.NoError(t, lock.Model(&model.Song{}).Where("id = ?", song.ID).Update("lifecycle_status", model.MusicLifecycleMerged).Error)
	require.NoError(t, lock.Commit().Error)
	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(8 * time.Second):
		t.Fatal("progress write did not finish after merge")
	}
	var count int64
	require.NoError(t, db.Model(&model.MusicPlaybackProgress{}).Where("song_id = ?", song.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestConcurrentAlbumMergesSerializeOnLockedEntries(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	source := model.Album{Title: "concurrent source"}
	target := model.Album{Title: "concurrent target"}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&target).Error)
	admin := authctx.CurrentUser{ID: user.ID, Role: authctx.RoleAdmin}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	lock := db.Begin()
	require.NoError(t, lock.Error)
	defer lock.Rollback()
	var held model.Album
	require.NoError(t, lock.Clauses(clause.Locking{Strength: "UPDATE"}).First(&held, "id = ?", target.ID).Error)
	results := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; results <- svc.MergeAlbums(admin, target.ID, source.ID, nil) }()
	}
	close(start)
	// Both requests encounter the held row lock before completing.
	select {
	case err := <-results:
		t.Fatalf("merge returned while target row was locked: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	require.NoError(t, lock.Commit().Error)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("concurrent merges did not finish")
	}
	close(results)
	successes, failures := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, failures)
	var updated model.Album
	require.NoError(t, db.WithContext(context.Background()).First(&updated, "id = ?", source.ID).Error)
	require.Equal(t, model.MusicLifecycleMerged, updated.LifecycleStatus)
}
