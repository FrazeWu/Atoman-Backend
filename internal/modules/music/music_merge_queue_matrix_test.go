package music

import (
	"atoman/internal/model"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestSongMergePreservesCurrentTrackWithUnexpectedQueueShape(t *testing.T) {
	_, db, user := newMusicTestService(t)
	source := model.Song{Title: "Merge source", AudioURL: "/merge-source.mp3"}
	target := model.Song{Title: "Merge target", AudioURL: "/merge-target.mp3"}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&target).Error)
	session := model.MusicPlaybackSession{UserID: user.ID, CurrentSongID: &source.ID, QueueJSON: json.RawMessage(`{"unexpected":"shape"}`)}
	require.NoError(t, db.Create(&session).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return mergeSongPlayback(tx, source.ID, target.ID) }))
	var updated model.MusicPlaybackSession
	require.NoError(t, db.First(&updated, "id = ?", session.ID).Error)
	require.NotNil(t, updated.CurrentSongID)
	require.Equal(t, target.ID, *updated.CurrentSongID)
	require.JSONEq(t, `{"unexpected":"shape"}`, string(updated.QueueJSON))
}
