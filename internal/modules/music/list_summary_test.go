package music

import (
	"atoman/internal/model"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestSongSummaryListKeepsPlaybackAndOmitsHeavyFields(t *testing.T) {
	svc, db, _ := newMusicHTTPTestService(t)
	song := model.Song{Title: "Summary song", Lyrics: "full lyrics", AudioURL: "https://media.example/song.mp3", AudioStatus: "ready", WaveformPeaks: json.RawMessage(`[1,2,3]`)}
	require.NoError(t, db.Create(&song).Error)
	r := gin.New()
	RegisterRoutes(r.Group("/api/v1/music"), svc)
	for _, path := range []string{"/api/v1/music/songs?view=summary", "/api/v1/music/search?q=Summary&type=song&view=summary"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		require.Equal(t, 200, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), song.ID.String())
		require.Contains(t, w.Body.String(), song.AudioURL)
		require.NotContains(t, w.Body.String(), `"lyrics"`)
		require.NotContains(t, w.Body.String(), `"waveform_peaks"`)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/music/songs", nil))
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "full lyrics", "旧客户端仍能读取完整数据")
}
