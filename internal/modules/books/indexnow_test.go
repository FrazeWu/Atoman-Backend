package books

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

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookIndexNowPublicationBoundary(t *testing.T) {
	service, user, work, store := newBookResourceTestService(t)
	testdb.Migrate(t, service.db, &model.IndexNowSubmission{})

	delivered := make(chan []string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URLList []string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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
	done := indexnow.StartWorker(ctx, service.db)
	defer func() { cancel(); <-done }()

	id := uuid.New()
	input := bookListTestInput(uuid.MustParse(work.ID))
	draft, err := service.SaveBookListDraft(ctx, user, id, input)
	require.NoError(t, err)
	draft, err = service.PublishBookList(ctx, user, id, PublishBookListInput{BaseVersion: draft.Version, Visibility: "private"})
	require.NoError(t, err)
	assertNoNotification := func() {
		t.Helper()
		var count int64
		require.NoError(t, service.db.Model(&model.IndexNowSubmission{}).Count(&count).Error)
		require.Zero(t, count, "private or unchanged content must not enter durable queue")
		select {
		case urls := <-delivered:
			t.Fatalf("unexpected IndexNow delivery: %v", urls)
		default:
		}
	}
	assertNoNotification()
	draft, err = service.PublishBookList(ctx, user, id, PublishBookListInput{BaseVersion: draft.Version, Visibility: "public"})
	require.NoError(t, err)
	expectDelivery := func(expected ...string) {
		t.Helper()
		select {
		case urls := <-delivered:
			require.ElementsMatch(t, expected, urls)
		case <-time.After(5 * time.Second):
			t.Fatal("no IndexNow delivery for committed publication")
		}
		require.Eventually(t, func() bool {
			var count int64
			return service.db.Model(&model.IndexNowSubmission{}).Count(&count).Error == nil && count == 0
		}, 3*time.Second, 20*time.Millisecond)
	}
	expectDelivery("https://www.atoman.org/books/lists", "https://www.atoman.org/books/lists/"+id.String())
	// Identical retry must not create another notification.
	_, err = service.PublishBookList(ctx, user, id, PublishBookListInput{BaseVersion: draft.Version - 1, Visibility: "public"})
	require.NoError(t, err)
	assertNoNotification()
	input.BaseVersion = draft.Version
	input.Description = "private draft edit"
	draft, err = service.SaveBookListDraft(ctx, user, id, input)
	require.NoError(t, err)
	assertNoNotification()
	require.NoError(t, service.WithdrawBookList(ctx, user, id, draft.Version, false))
	expectDelivery("https://www.atoman.org/books/lists", "https://www.atoman.org/books/lists/"+id.String())
	workID := uuid.MustParse(work.ID)
	edition, err := service.CreateBookEditionDraftIdempotent(ctx, user, workID,
		CreateBookEditionInput{Language: "zh", Publisher: "Test Publisher", EditionStatement: "First"}, uuid.Nil)
	require.NoError(t, err)
	editionID := uuid.MustParse(edition.ID)
	assertNoNotification()
	asset, err := service.CreateBookResource(ctx, user, workID, CreateBookResourceInput{EditionID: &editionID, Language: "zh", Format: "txt"})
	require.NoError(t, err)
	queueBookTestFile(t, service, user, store, asset, "draft.txt", "text/plain", []byte("a valid book"))
	_, err = service.ProcessBookResourceUploads(ctx)
	require.NoError(t, err)
	expectDelivery("https://www.atoman.org/books", "https://www.atoman.org/books/work/"+work.ID, "https://www.atoman.org/books/edition/"+edition.ID)
}
