package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/indexnow"
	"atoman/internal/testdb"

	"github.com/stretchr/testify/require"
)

func TestMusicIndexNowOnlyDeliversAppliedPublicRevision(t *testing.T) {
	db := testdb.OpenPostgres(t, "music_indexnow")
	testdb.Migrate(t, db, &model.User{}, &model.Artist{}, &model.Revision{}, &model.IndexNowSubmission{})
	actor := createRevisionTestUser(t, db)
	public := model.Artist{Name: "Public", LifecycleStatus: model.MusicLifecycleActive, CreatedBy: &actor}
	draft := model.Artist{Name: "Draft", LifecycleStatus: model.MusicLifecycleDraft, CreatedBy: &actor}
	for _, artist := range []*model.Artist{&public, &draft} {
		require.NoError(t, db.Create(artist).Error)
		raw, err := json.Marshal(map[string]string{"name": artist.Name})
		require.NoError(t, err)
		require.NoError(t, db.Create(&model.Revision{
			ContentType: "artist", ContentID: artist.ID, VersionNumber: 1,
			ContentSnapshot: raw, EditorID: actor, Status: "approved", IsCurrent: true, EditType: "creation",
		}).Error)
	}
	delivered := make(chan []string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ URLList []string }
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		delivered <- body.URLList
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("INDEXNOW_KEY", "local-test-key-1234")
	t.Setenv("INDEXNOW_SITE_URL", "https://www.atoman.org")
	t.Setenv("INDEXNOW_ENDPOINT", server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := indexnow.StartWorker(ctx, db)
	defer func() { cancel(); <-done }()
	service := NewRevisionService(db)
	_, _, err := service.CreateRevision("artist", draft.ID, actor, map[string]interface{}{"name": "Changed private draft"}, "draft edit", 1, true)
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Model(&model.IndexNowSubmission{}).Count(&count).Error)
	require.Zero(t, count)
	_, _, err = service.CreateRevision("artist", public.ID, actor, map[string]interface{}{"name": "Updated public artist"}, "public edit", 1, true)
	require.NoError(t, err)
	select {
	case urls := <-delivered:
		require.ElementsMatch(t, []string{"https://www.atoman.org/music", "https://www.atoman.org/music/artists", "https://www.atoman.org/music/artist/" + public.ID.String()}, urls)
	case <-time.After(5 * time.Second):
		t.Fatal("expected a public artist notification")
	}
}
