package books

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func prepareBookNotePublicationTables(t *testing.T, service *Service, user authctx.CurrentUser) model.Channel {
	t.Helper()
	testdb.Migrate(t, service.db, &model.Channel{}, &model.Collection{}, &model.Post{}, &model.PodcastEpisode{},
		&model.ContentEntry{}, &model.ContentPostExtension{}, &model.ContentBlogExtension{}, &model.ContentBlogTag{},
		&model.ContentBlogVersion{}, &model.ContentCollection{}, &model.ContentCollectionMembership{}, &model.LegacyCollectionMapping{},
		&model.ContentPublicationEvent{}, &model.ContentReference{}, &model.ContentMediaAsset{})
	channel := model.Channel{UserID: &user.ID, Name: "读书频道", Slug: uuid.NewString()}
	require.NoError(t, service.db.Create(&channel).Error)
	return channel
}

func TestBookPrivateNotePublicationIsAnIndependentCanonicalSnapshot(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	channel := prepareBookNotePublicationTables(t, service, user)
	noteID := uuid.New()
	noteInput := SaveBookPrivateNoteInput{WorkID: uuid.MustParse(work.ID), Title: "公开的读书心得", Body: "仅这段正文应被公开", Tags: []string{"不公开的私人标签"}}
	_, err := service.SaveBookPrivateNote(ctx, user, noteID, noteInput)
	require.NoError(t, err)
	input := PublishBookPrivateNoteInput{RequestID: uuid.New(), BaseVersion: 1, ChannelID: channel.ID, ConfirmPublic: true}
	result, err := service.PublishBookPrivateNote(ctx, user, noteID, input)
	require.NoError(t, err)
	require.NotEqual(t, noteID, result.ContentID)
	require.Equal(t, "/post/"+result.ContentID.String(), result.TargetPath)
	var entry model.ContentEntry
	require.NoError(t, service.db.First(&entry, "id = ?", result.ContentID).Error)
	require.Equal(t, "blog", entry.Kind)
	require.Equal(t, "published", entry.Status)
	require.Equal(t, "public", entry.Visibility)
	var content model.ContentBlogExtension
	require.NoError(t, service.db.First(&content, "content_id = ?", result.ContentID).Error)
	require.Equal(t, noteInput.Body, content.Content)
	var count int64
	require.NoError(t, service.db.Model(&model.ContentBlogTag{}).Where("content_id = ?", result.ContentID).Count(&count).Error)
	require.Zero(t, count, "private note tags must not become public topics")
	require.NoError(t, service.db.Model(&model.ContentBlogVersion{}).Where("content_id = ?", result.ContentID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, service.db.Model(&model.ContentPublicationEvent{}).Where("content_id = ?", result.ContentID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, service.db.Model(&model.Post{}).Count(&count).Error)
	require.Zero(t, count, "the snapshot must not create a legacy Post authority")
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), noteID.String())
	require.NotContains(t, string(encoded), "不公开的私人标签")
	noteInput.BaseVersion = 1
	noteInput.Body = "发布之后新增的私人内容"
	_, err = service.SaveBookPrivateNote(ctx, user, noteID, noteInput)
	require.NoError(t, err)
	_, err = service.DeleteBookPrivateNote(ctx, user, noteID, 2)
	require.NoError(t, err)
	require.NoError(t, service.db.Unscoped().Model(&model.BookPrivateNote{}).Where("id = ?", noteID).Update("deleted_at", time.Now().UTC().Add(-31*24*time.Hour)).Error)
	require.NoError(t, service.purgeBookPrivateNotes(ctx, &user.ID, time.Now().UTC()))
	retry, err := service.PublishBookPrivateNote(ctx, user, noteID, input)
	require.NoError(t, err)
	require.True(t, retry.Duplicate)
	require.Equal(t, result.ContentID, retry.ContentID)
	require.NoError(t, service.db.First(&content, "content_id = ?", result.ContentID).Error)
	require.Equal(t, "仅这段正文应被公开", content.Content)
	require.NoError(t, service.db.Model(&entry).Update("status", "draft").Error)
	_, err = service.PublishBookPrivateNote(ctx, user, noteID, input)
	require.NoError(t, err)
	require.NoError(t, service.db.First(&entry, "id = ?", result.ContentID).Error)
	require.Equal(t, "draft", entry.Status, "retry must not republish a withdrawn copy")
}

func TestBookPrivateNotePublicationRequiresConfirmationOwnershipAndReviewedVersion(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	channel := prepareBookNotePublicationTables(t, service, user)
	noteID := uuid.New()
	note := SaveBookPrivateNoteInput{WorkID: uuid.MustParse(work.ID), Title: "私有笔记", Body: "正文"}
	_, err := service.SaveBookPrivateNote(ctx, user, noteID, note)
	require.NoError(t, err)
	input := PublishBookPrivateNoteInput{RequestID: uuid.New(), BaseVersion: 1, ChannelID: channel.ID}
	_, err = service.PublishBookPrivateNote(ctx, user, noteID, input)
	require.Equal(t, 400, apperr.FromError(err).HTTPStatus)
	input.ConfirmPublic = true
	other := newBookLibraryTestReader(t, service)
	_, err = service.PublishBookPrivateNote(ctx, other, noteID, input)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	otherChannel := model.Channel{UserID: &other.ID, Name: "其他人频道", Slug: uuid.NewString()}
	require.NoError(t, service.db.Create(&otherChannel).Error)
	input.ChannelID = otherChannel.ID
	_, err = service.PublishBookPrivateNote(ctx, user, noteID, input)
	require.Equal(t, 403, apperr.FromError(err).HTTPStatus)
	input.ChannelID = channel.ID
	note.BaseVersion = 1
	note.Body = "更新的正文"
	_, err = service.SaveBookPrivateNote(ctx, user, noteID, note)
	require.NoError(t, err)
	_, err = service.PublishBookPrivateNote(ctx, user, noteID, input)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	var count int64
	require.NoError(t, service.db.Model(&model.ContentEntry{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, service.db.Model(&model.BookNotePublication{}).Count(&count).Error)
	require.Zero(t, count)
	input.BaseVersion = 2
	_, err = service.PublishBookPrivateNote(ctx, user, noteID, input)
	require.NoError(t, err)
	input.ChannelID = otherChannel.ID
	_, err = service.PublishBookPrivateNote(ctx, user, noteID, input)
	require.Equal(t, "books.note_publication_request_reused", apperr.FromError(err).Code)
}

func TestBookPrivateNotePublicationConcurrentRetries(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	channel := prepareBookNotePublicationTables(t, service, user)
	id := uuid.New()
	_, err := service.SaveBookPrivateNote(ctx, user, id, SaveBookPrivateNoteInput{WorkID: uuid.MustParse(work.ID), Title: "同时发布", Body: "正文"})
	require.NoError(t, err)
	input := PublishBookPrivateNoteInput{RequestID: uuid.New(), BaseVersion: 1, ChannelID: channel.ID, ConfirmPublic: true}
	start := make(chan struct{})
	results := make(chan BookNotePublicationDTO, 2)
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			r, err := service.PublishBookPrivateNote(ctx, user, id, input)
			results <- r
			errors <- err
		}()
	}
	close(start)
	var ids []uuid.UUID
	duplicates := 0
	for range 2 {
		require.NoError(t, <-errors)
		r := <-results
		ids = append(ids, r.ContentID)
		if r.Duplicate {
			duplicates++
		}
	}
	require.Equal(t, ids[0], ids[1])
	require.Equal(t, 1, duplicates)
	var count int64
	require.NoError(t, service.db.Model(&model.ContentEntry{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestBookReadingStatisticsAndPublishedSnapshotsSurviveMergeUndo(t *testing.T) {
	service, user, source, target := newBookMergeTestService(t)
	ctx := context.Background()
	channel := prepareBookNotePublicationTables(t, service, user)
	sourceID, targetID := uuid.MustParse(source.ID), uuid.MustParse(target.ID)
	_, revision := readyBookReadingFile(t, service, user, sourceID, "zh", "txt")
	session := newBookCloudTestSession(t, service, user, revision.ID, "web")
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	_, err := service.recordBookReadingHeartbeatAt(ctx, user, session.Session.ID, BookReadingHeartbeatInput{Sequence: 1, Visible: true, Active: true}, now)
	require.NoError(t, err)
	id := uuid.New()
	_, err = service.SaveBookPrivateNote(ctx, user, id, SaveBookPrivateNoteInput{WorkID: sourceID, Title: "合并后发布", Body: "独立副本"})
	require.NoError(t, err)
	merge := mergeBookTestWorks(t, service, user, source, target)
	_, err = service.recordBookReadingHeartbeatAt(ctx, user, session.Session.ID, BookReadingHeartbeatInput{Sequence: 2, Visible: true, Active: true}, now.Add(15*time.Second))
	require.NoError(t, err)
	published, err := service.PublishBookPrivateNote(ctx, user, id, PublishBookPrivateNoteInput{RequestID: uuid.New(), BaseVersion: 1, ChannelID: channel.ID, ConfirmPublic: true})
	require.NoError(t, err)
	stats, err := service.GetBookReadingStatistics(ctx, user, "2026-10-15", "2026-10-15")
	require.NoError(t, err)
	require.Equal(t, targetID, stats.Works[0].WorkID)
	_, err = service.RevertBookWorkMerge(ctx, user, merge.ID)
	require.NoError(t, err)
	stats, err = service.GetBookReadingStatistics(ctx, user, "2026-10-15", "2026-10-15")
	require.NoError(t, err)
	require.EqualValues(t, 15, stats.TotalSeconds)
	require.Equal(t, sourceID, stats.Works[0].WorkID)
	var content model.ContentBlogExtension
	require.NoError(t, service.db.First(&content, "content_id = ?", published.ContentID).Error)
	require.Equal(t, "独立副本", content.Content)
}

func TestBookPrivateNotePublicationHTTP(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	channel := prepareBookNotePublicationTables(t, service, user)
	id := uuid.New()
	_, err := service.SaveBookPrivateNote(context.Background(), user, id, SaveBookPrivateNoteInput{WorkID: uuid.MustParse(work.ID), Title: "网页发布", Body: "正文"})
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, user); c.Next() })
	RegisterRoutes(router.Group("/api/v1/books"), service)
	body := `{"request_id":"` + uuid.NewString() + `","base_version":1,"channel_id":"` + channel.ID.String() + `","confirm_public":true}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/books/me/private-notes/"+id.String()+"/publish", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Body.String(), `"target_path":"/post/`)
}
