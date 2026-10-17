package shortnote

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newShortNoteHTTPTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.ShortNote{}, &model.ShortNoteMedia{}, &model.ShortNoteVote{}, &model.ShortNoteRead{}, &model.Like{}, &model.DiscussionTarget{}, &model.ContentReference{}, &model.Notification{}, &model.Artist{}, &model.Album{}, &model.Song{}, &model.Video{})
	if err := migrations.RunNotificationDMIndexes(db); err != nil {
		t.Fatalf("create notification indexes: %v", err)
	}
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
}

func newShortNoteHTTPRouter(service *Service, current *authctx.CurrentUser) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if current != nil {
			authctx.SetCurrentUser(c, *current)
		}
		c.Next()
	})
	RegisterRoutes(r.Group("/api/v1/short-notes"), service)
	return r
}

func shortNoteRequest(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func responseID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.ID == "" {
		t.Fatalf("missing id: %s", w.Body.String())
	}
	return payload.Data.ID
}

func TestShortNoteCreateAndDetailIncludesMediaAndCounts(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	r := newShortNoteHTTPRouter(service, &user)
	w := shortNoteRequest(t, r, http.MethodPost, "/api/v1/short-notes", `{"content":"hello","media_urls":["https://example.test/one.jpg","https://example.test/two.jpg"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	id := responseID(t, w)

	var note model.ShortNote
	if err := db.Preload("Media").First(&note, "id = ?", id).Error; err != nil {
		t.Fatalf("load note: %v", err)
	}
	if note.UserID != user.ID || note.Content != "hello" || len(note.Media) != 2 || note.Media[0].Position != 0 || note.Media[1].Position != 1 {
		t.Fatalf("unexpected persisted note: %#v", note)
	}
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "short_note", TargetID: note.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&model.DiscussionTarget{Kind: "short_note", ResourceID: note.ID, ResourceKey: note.ID.String(), CommentCount: 3, NextFloor: 1}).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}
	w = shortNoteRequest(t, r, http.MethodGet, "/api/v1/short-notes/"+id, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"likes_count":1`) || !strings.Contains(w.Body.String(), `"comments_count":3`) || !strings.Contains(w.Body.String(), `"liked":true`) || !strings.Contains(w.Body.String(), `"user":{"`) {
		t.Fatalf("unexpected detail: %d %s", w.Code, w.Body.String())
	}
}

func TestShortNoteVoteReturnsNetScoreAndReplacesLegacyLike(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	note := model.ShortNote{UserID: user.ID, Content: "note"}
	if err := db.Create(&note).Error; err != nil {
		t.Fatalf("create note: %v", err)
	}
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "short_note", TargetID: note.ID}).Error; err != nil {
		t.Fatalf("create legacy like: %v", err)
	}
	r := newShortNoteHTTPRouter(service, &user)
	w := shortNoteRequest(t, r, http.MethodPut, "/api/v1/short-notes/"+note.ID.String()+"/vote", `{"direction":"down"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"likes_count":0`) || !strings.Contains(w.Body.String(), `"dislikes_count":1`) || !strings.Contains(w.Body.String(), `"vote_score":-1`) || !strings.Contains(w.Body.String(), `"viewer_vote":"down"`) {
		t.Fatalf("unexpected downvote response: %d %s", w.Code, w.Body.String())
	}
	w = shortNoteRequest(t, r, http.MethodDelete, "/api/v1/short-notes/"+note.ID.String()+"/vote", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"vote_score":0`) || !strings.Contains(w.Body.String(), `"viewer_vote":"none"`) {
		t.Fatalf("unexpected cleared vote response: %d %s", w.Code, w.Body.String())
	}
}

func TestShortNoteRejectsBlankOverlongAndTooManyImages(t *testing.T) {
	service, _, user := newShortNoteHTTPTestService(t)
	r := newShortNoteHTTPRouter(service, &user)
	for _, body := range []string{
		`{"content":"   "}`,
		`{"content":"` + strings.Repeat("字", 501) + `"}`,
		`{"content":"valid","media_urls":["1","2","3","4","5","6","7","8","9","10"]}`,
	} {
		w := shortNoteRequest(t, r, http.MethodPost, "/api/v1/short-notes", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	}
}

func TestShortNoteTimelineOrdersByCreationAndPaginates(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	r := newShortNoteHTTPRouter(service, nil)
	first := model.ShortNote{UserID: user.ID, Content: "first"}
	second := model.ShortNote{UserID: user.ID, Content: "second"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&first).Update("created_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	w := shortNoteRequest(t, r, http.MethodGet, "/api/v1/short-notes?page=1&page_size=1", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"content":"second"`) || strings.Contains(w.Body.String(), `"content":"first"`) || !strings.Contains(w.Body.String(), `"total":2`) {
		t.Fatalf("unexpected timeline: %d %s", w.Code, w.Body.String())
	}
}

func TestShortNoteTimelineFiltersByAuthor(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	other := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ShortNote{UserID: user.ID, Content: "alice note"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ShortNote{UserID: other.UUID, Content: "bob note"}).Error; err != nil {
		t.Fatal(err)
	}

	r := newShortNoteHTTPRouter(service, nil)
	w := shortNoteRequest(t, r, http.MethodGet, "/api/v1/short-notes?user_id="+other.UUID.String(), "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"content":"bob note"`) || strings.Contains(w.Body.String(), `"content":"alice note"`) || !strings.Contains(w.Body.String(), `"total":1`) {
		t.Fatalf("unexpected author timeline: %d %s", w.Code, w.Body.String())
	}
}

func TestShortNoteTimelineRejectsInvalidAuthorID(t *testing.T) {
	service, _, _ := newShortNoteHTTPTestService(t)
	r := newShortNoteHTTPRouter(service, nil)
	w := shortNoteRequest(t, r, http.MethodGet, "/api/v1/short-notes?user_id=invalid", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestShortNoteOnlyAuthorCanUpdateOrDeleteAndUpdateMarksEdited(t *testing.T) {
	service, db, owner := newShortNoteHTTPTestService(t)
	note := model.ShortNote{UserID: owner.ID, Content: "before"}
	if err := db.Create(&note).Error; err != nil {
		t.Fatal(err)
	}
	other := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	otherCurrent := authctx.CurrentUser{ID: other.UUID, Username: other.Username, Role: other.Role}
	foreign := newShortNoteHTTPRouter(service, &otherCurrent)
	if w := shortNoteRequest(t, foreign, http.MethodPut, "/api/v1/short-notes/"+note.ID.String(), `{"content":"after"}`); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 update, got %d: %s", w.Code, w.Body.String())
	}
	if w := shortNoteRequest(t, foreign, http.MethodDelete, "/api/v1/short-notes/"+note.ID.String(), ""); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 delete, got %d: %s", w.Code, w.Body.String())
	}

	r := newShortNoteHTTPRouter(service, &owner)
	w := shortNoteRequest(t, r, http.MethodPut, "/api/v1/short-notes/"+note.ID.String(), `{"content":"after","media_urls":["https://example.test/new.jpg"]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"edited":true`) {
		t.Fatalf("unexpected update: %d %s", w.Code, w.Body.String())
	}
	if w = shortNoteRequest(t, r, http.MethodDelete, "/api/v1/short-notes/"+note.ID.String(), ""); w.Code != http.StatusOK {
		t.Fatalf("unexpected delete: %d %s", w.Code, w.Body.String())
	}
	var count int64
	if err := db.Model(&model.ShortNote{}).Where("id = ?", note.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("expected soft deletion, count=%d err=%v", count, err)
	}
}

func TestShortNoteLikeIsIdempotent(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	note := model.ShortNote{UserID: user.ID, Content: "note"}
	if err := db.Create(&note).Error; err != nil {
		t.Fatal(err)
	}
	r := newShortNoteHTTPRouter(service, &user)
	path := "/api/v1/short-notes/" + note.ID.String() + "/like"
	for range 2 {
		if w := shortNoteRequest(t, r, http.MethodPost, path, ""); w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
	}
	var vote model.ShortNoteVote
	if err := db.Where("short_note_id = ? AND user_id = ?", note.ID, user.ID).First(&vote).Error; err != nil || vote.Direction != "up" {
		t.Fatalf("expected one up vote, vote=%+v err=%v", vote, err)
	}
	for range 2 {
		if w := shortNoteRequest(t, r, http.MethodDelete, path, ""); w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
	}
	var count int64
	if err := db.Model(&model.ShortNoteVote{}).Where("short_note_id = ? AND user_id = ?", note.ID, user.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("expected no active votes, count=%d err=%v", count, err)
	}
}

func TestShortNoteMentionsNotifiesTargetUser(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	adminUser := model.User{Username: "admin", Email: "admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	if err := db.Create(&adminUser).Error; err != nil {
		t.Fatal(err)
	}
	r := newShortNoteHTTPRouter(service, &user)
	body := `{"content":"Hello @admin check this out"}`
	w := shortNoteRequest(t, r, http.MethodPost, "/api/v1/short-notes", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	if err := db.Model(&model.Notification{}).Where("recipient_id = ? AND type = ?", adminUser.UUID, "content_mention").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected 1 mention notification for admin, got %d, err=%v", count, err)
	}
}

func TestShortNoteUpdateRefreshesMentionsAndNotifiesNewTarget(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	adminUser := model.User{Username: "admin", Email: "admin-update@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	bobUser := model.User{Username: "bob", Email: "bob-update@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	for _, account := range []*model.User{&adminUser, &bobUser} {
		if err := db.Create(account).Error; err != nil {
			t.Fatalf("create mentioned user: %v", err)
		}
	}

	r := newShortNoteHTTPRouter(service, &user)
	create := shortNoteRequest(t, r, http.MethodPost, "/api/v1/short-notes", `{"content":"hello @admin"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", create.Code, create.Body.String())
	}
	noteID := uuid.MustParse(responseID(t, create))

	update := shortNoteRequest(t, r, http.MethodPut, "/api/v1/short-notes/"+noteID.String(), `{"content":"hello @bob"}`)
	if update.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", update.Code, update.Body.String())
	}

	var refs []model.ContentReference
	if err := db.Order("start_offset ASC").Find(&refs, "source_type = ? AND source_id = ?", "short_note", noteID).Error; err != nil {
		t.Fatalf("load content references: %v", err)
	}
	if len(refs) != 1 || refs[0].TargetType != "user" || refs[0].TargetID != bobUser.UUID {
		t.Fatalf("expected references to point to bob only, got %#v", refs)
	}

	var bobCount int64
	if err := db.Model(&model.Notification{}).Where("recipient_id = ? AND type = ?", bobUser.UUID, "content_mention").Count(&bobCount).Error; err != nil {
		t.Fatalf("count bob notifications: %v", err)
	}
	if bobCount != 1 {
		t.Fatalf("expected 1 mention notification for bob after update, got %d", bobCount)
	}
}

func TestCreateAndGetShortNoteWithMediaAttachment(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	router := newShortNoteHTTPRouter(service, &user)

	// 创建测试歌曲
	song := model.Song{
		Title:          "测试歌曲",
		DurationSec:    180,
		AudioURL:       "https://example.com/audio.mp3",
		CoverURL:       "https://example.com/cover.jpg",
		LifecycleStatus: "active",
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	songID := song.ID

	body := map[string]any{
		"content":               "分享一首好歌",
		"media_urls":            []string{},
		"media_attachment_type": "song",
		"media_attachment_id":   songID.String(),
	}
	payload, _ := json.Marshal(body)
	res := shortNoteRequest(t, router, "POST", "/api/v1/short-notes", string(payload))
	if res.Code != http.StatusCreated && res.Code != http.StatusOK {
		t.Fatalf("expected 200/201, got %d: %s", res.Code, res.Body.String())
	}

	var resData struct {
		Data NoteDTO `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &resData); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resData.Data.MediaAttachment == nil {
		t.Fatalf("expected media_attachment to be non-nil")
	}
	if resData.Data.MediaAttachment.Type != "song" || resData.Data.MediaAttachment.ID != songID {
		t.Errorf("unexpected media attachment: %+v", resData.Data.MediaAttachment)
	}
	if resData.Data.MediaAttachment.Title != "测试歌曲" {
		t.Errorf("expected title '测试歌曲', got %q", resData.Data.MediaAttachment.Title)
	}
	if resData.Data.MediaAttachment.Duration != 180 {
		t.Errorf("expected duration 180, got %d", resData.Data.MediaAttachment.Duration)
	}

	// GET 验证
	noteID := resData.Data.ID
	getRes := shortNoteRequest(t, router, "GET", "/api/v1/short-notes/"+noteID.String(), "")
	if getRes.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", getRes.Code, getRes.Body.String())
	}
	var getData struct {
		Data NoteDTO `json:"data"`
	}
	if err := json.Unmarshal(getRes.Body.Bytes(), &getData); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if getData.Data.MediaAttachment == nil || getData.Data.MediaAttachment.ID != songID {
		t.Fatalf("expected GET media attachment with songID, got %+v", getData.Data.MediaAttachment)
	}

	// content_references 验证
	var refs []model.ContentReference
	if err := db.Where("source_type = ? AND source_id = ? AND source_field = ?", "short_note", noteID, "media_attachment").Find(&refs).Error; err != nil {
		t.Fatalf("query content_references: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 media attachment reference, got %d", len(refs))
	}
	if refs[0].TargetType != "song" || refs[0].TargetID != songID {
		t.Errorf("unexpected reference target: %+v", refs[0])
	}
}

func TestShortNoteMediaAttachmentValidation(t *testing.T) {
	service, _, user := newShortNoteHTTPTestService(t)
	router := newShortNoteHTTPRouter(service, &user)

	// 无效类型
	w := shortNoteRequest(t, router, http.MethodPost, "/api/v1/short-notes", `{"content":"test","media_attachment_type":"invalid","media_attachment_id":"`+uuid.New().String()+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid type, got %d: %s", w.Code, w.Body.String())
	}

	// 提供了类型但缺少 ID
	w = shortNoteRequest(t, router, http.MethodPost, "/api/v1/short-notes", `{"content":"test","media_attachment_type":"song"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing ID, got %d: %s", w.Code, w.Body.String())
	}

	// 提供了 ID 但缺少类型
	w = shortNoteRequest(t, router, http.MethodPost, "/api/v1/short-notes", `{"content":"test","media_attachment_id":"`+uuid.New().String()+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing type, got %d: %s", w.Code, w.Body.String())
	}
}

func TestShortNoteMediaAttachmentAlbumAndVideo(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	router := newShortNoteHTTPRouter(service, &user)

	// 测试专辑附件
	album := model.Album{
		Title:    "测试专辑",
		CoverURL: "https://example.com/album.jpg",
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	albumID := album.ID

	albumBody := map[string]any{
		"content":               "分享一张好专辑",
		"media_attachment_type": "album",
		"media_attachment_id":   albumID.String(),
	}
	payload, _ := json.Marshal(albumBody)
	res := shortNoteRequest(t, router, "POST", "/api/v1/short-notes", string(payload))
	if res.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", res.Code, res.Body.String())
	}
	var albumRes struct {
		Data NoteDTO `json:"data"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &albumRes)
	if albumRes.Data.MediaAttachment == nil || albumRes.Data.MediaAttachment.Type != "album" || albumRes.Data.MediaAttachment.ID != albumID || albumRes.Data.MediaAttachment.Title != "测试专辑" {
		t.Fatalf("unexpected album media attachment: %+v", albumRes.Data.MediaAttachment)
	}

	// 测试视频附件
	video := model.Video{
		UserID:       user.ID,
		Title:        "测试视频",
		ThumbnailURL: "https://example.com/video.jpg",
		DurationSec:  120,
		VideoURL:     "https://example.com/video.mp4",
	}
	if err := db.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	videoID := video.ID

	videoBody := map[string]any{
		"content":               "分享一段视频",
		"media_attachment_type": "video",
		"media_attachment_id":   videoID.String(),
	}
	payload, _ = json.Marshal(videoBody)
	res = shortNoteRequest(t, router, "POST", "/api/v1/short-notes", string(payload))
	if res.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", res.Code, res.Body.String())
	}
	var videoRes struct {
		Data NoteDTO `json:"data"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &videoRes)
	if videoRes.Data.MediaAttachment == nil || videoRes.Data.MediaAttachment.Type != "video" || videoRes.Data.MediaAttachment.ID != videoID || videoRes.Data.MediaAttachment.CoverURL != "https://example.com/video.jpg" || videoRes.Data.MediaAttachment.Duration != 120 {
		t.Fatalf("unexpected video media attachment: %+v", videoRes.Data.MediaAttachment)
	}
}

func TestShortNoteUpdateMediaAttachment(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	router := newShortNoteHTTPRouter(service, &user)

	song1 := model.Song{
		Title:          "歌曲1",
		DurationSec:    100,
		AudioURL:       "https://example.com/1.mp3",
		CoverURL:       "https://example.com/c1.jpg",
		LifecycleStatus: "active",
	}
	if err := db.Create(&song1).Error; err != nil {
		t.Fatalf("create song1: %v", err)
	}
	songID1 := song1.ID

	song2 := model.Song{
		Title:          "歌曲2",
		DurationSec:    200,
		AudioURL:       "https://example.com/2.mp3",
		CoverURL:       "https://example.com/c2.jpg",
		LifecycleStatus: "active",
	}
	if err := db.Create(&song2).Error; err != nil {
		t.Fatalf("create song2: %v", err)
	}
	songID2 := song2.ID

	body := map[string]any{
		"content":               "初始短笺",
		"media_attachment_type": "song",
		"media_attachment_id":   songID1.String(),
	}
	payload, _ := json.Marshal(body)
	createRes := shortNoteRequest(t, router, "POST", "/api/v1/short-notes", string(payload))
	noteID := responseID(t, createRes)

	// 更新为歌曲2
	updateBody := map[string]any{
		"content":               "更新后的短笺",
		"media_attachment_type": "song",
		"media_attachment_id":   songID2.String(),
	}
	payload, _ = json.Marshal(updateBody)
	updateRes := shortNoteRequest(t, router, "PUT", "/api/v1/short-notes/"+noteID, string(payload))
	if updateRes.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", updateRes.Code, updateRes.Body.String())
	}
	var updateData struct {
		Data NoteDTO `json:"data"`
	}
	_ = json.Unmarshal(updateRes.Body.Bytes(), &updateData)
	if updateData.Data.MediaAttachment == nil || updateData.Data.MediaAttachment.ID != songID2 || updateData.Data.MediaAttachment.Title != "歌曲2" {
		t.Fatalf("expected media attachment updated to song 2, got %+v", updateData.Data.MediaAttachment)
	}

	// 验证 content_references 只有歌曲2
	var refs []model.ContentReference
	if err := db.Where("source_type = ? AND source_id = ? AND source_field = ?", "short_note", noteID, "media_attachment").Find(&refs).Error; err != nil {
		t.Fatalf("load refs: %v", err)
	}
	if len(refs) != 1 || refs[0].TargetID != songID2 {
		t.Fatalf("expected 1 reference pointing to songID2, got %+v", refs)
	}

	// 清空附件（更新为 null / 空附件）
	clearBody := map[string]any{
		"content":               "已清空附件的短笺",
		"media_attachment_type": "",
		"media_attachment_id":   nil,
	}
	payload, _ = json.Marshal(clearBody)
	clearRes := shortNoteRequest(t, router, "PUT", "/api/v1/short-notes/"+noteID, string(payload))
	if clearRes.Code != http.StatusOK {
		t.Fatalf("expected 200 on clear attachment, got %d: %s", clearRes.Code, clearRes.Body.String())
	}
	var clearData struct {
		Data NoteDTO `json:"data"`
	}
	_ = json.Unmarshal(clearRes.Body.Bytes(), &clearData)
	if clearData.Data.MediaAttachment != nil {
		t.Fatalf("expected media attachment to be nil after clearing, got %+v", clearData.Data.MediaAttachment)
	}

	// 验证 content_references 中不再有 media_attachment 引用
	var clearedRefs []model.ContentReference
	if err := db.Where("source_type = ? AND source_id = ? AND source_field = ?", "short_note", noteID, "media_attachment").Find(&clearedRefs).Error; err != nil {
		t.Fatalf("load cleared refs: %v", err)
	}
	if len(clearedRefs) != 0 {
		t.Fatalf("expected 0 media attachment references after clear, got %+v", clearedRefs)
	}
}

func TestShortNoteMediaAttachmentMissingOrDeletedRecord(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	router := newShortNoteHTTPRouter(service, &user)

	// 1. 引用不存在的歌曲 ID
	missingSongID := uuid.New()
	createBody := map[string]any{
		"content":               "引用不存在的歌曲",
		"media_attachment_type": "song",
		"media_attachment_id":   missingSongID.String(),
	}
	payload, _ := json.Marshal(createBody)
	createRes := shortNoteRequest(t, router, "POST", "/api/v1/short-notes", string(payload))
	if createRes.Code != http.StatusCreated {
		t.Fatalf("expected 201 on create with missing song, got %d: %s", createRes.Code, createRes.Body.String())
	}
	var createData struct {
		Data NoteDTO `json:"data"`
	}
	_ = json.Unmarshal(createRes.Body.Bytes(), &createData)
	if createData.Data.MediaAttachment == nil {
		t.Fatalf("expected fallback media attachment DTO, got nil")
	}
	if createData.Data.MediaAttachment.Type != "song" || createData.Data.MediaAttachment.ID != missingSongID {
		t.Fatalf("expected fallback DTO with type song and id %s, got %+v", missingSongID, createData.Data.MediaAttachment)
	}
	if createData.Data.MediaAttachment.Title != "" {
		t.Fatalf("expected empty title in fallback DTO, got %q", createData.Data.MediaAttachment.Title)
	}

	noteID := createData.Data.ID

	// GET 详情不应返回 500，应返回 200 及 fallback DTO
	getRes := shortNoteRequest(t, router, "GET", "/api/v1/short-notes/"+noteID.String(), "")
	if getRes.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET with missing song, got %d: %s", getRes.Code, getRes.Body.String())
	}
	var getData struct {
		Data NoteDTO `json:"data"`
	}
	_ = json.Unmarshal(getRes.Body.Bytes(), &getData)
	if getData.Data.MediaAttachment == nil || getData.Data.MediaAttachment.ID != missingSongID {
		t.Fatalf("expected fallback DTO on GET, got %+v", getData.Data.MediaAttachment)
	}

	// List 时间线不应返回 500
	listRes := shortNoteRequest(t, router, "GET", "/api/v1/short-notes", "")
	if listRes.Code != http.StatusOK {
		t.Fatalf("expected 200 on List, got %d: %s", listRes.Code, listRes.Body.String())
	}

	// 2. 软删除/物理删除已引用的歌曲后，查询短笺不应返回 500，应返回 fallback DTO
	song := model.Song{
		Title:    "将被删除的歌曲",
		AudioURL: "https://example.com/song.mp3",
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	createBody2 := map[string]any{
		"content":               "引用待删除歌曲",
		"media_attachment_type": "song",
		"media_attachment_id":   song.ID.String(),
	}
	payload2, _ := json.Marshal(createBody2)
	createRes2 := shortNoteRequest(t, router, "POST", "/api/v1/short-notes", string(payload2))
	if createRes2.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", createRes2.Code, createRes2.Body.String())
	}
	noteID2 := responseID(t, createRes2)

	// 软删除歌曲 (GORM Delete sets deleted_at)
	if err := db.Delete(&song).Error; err != nil {
		t.Fatalf("delete song: %v", err)
	}

	// 再次查询短笺，应返回 fallback DTO，不能返回 500
	getRes2 := shortNoteRequest(t, router, "GET", "/api/v1/short-notes/"+noteID2, "")
	if getRes2.Code != http.StatusOK {
		t.Fatalf("expected 200 after song deletion, got %d: %s", getRes2.Code, getRes2.Body.String())
	}
	var getData2 struct {
		Data NoteDTO `json:"data"`
	}
	_ = json.Unmarshal(getRes2.Body.Bytes(), &getData2)
	if getData2.Data.MediaAttachment == nil || getData2.Data.MediaAttachment.ID != song.ID {
		t.Fatalf("expected fallback DTO after song deleted, got %+v", getData2.Data.MediaAttachment)
	}
	if getData2.Data.MediaAttachment.Title != "" {
		t.Fatalf("expected empty title in fallback DTO, got %q", getData2.Data.MediaAttachment.Title)
	}

	// 3. 测试不存在的专辑和视频 ID 也同样返回 fallback DTO
	missingAlbumID := uuid.New()
	albumBody := map[string]any{
		"content":               "引用不存在的专辑",
		"media_attachment_type": "album",
		"media_attachment_id":   missingAlbumID.String(),
	}
	payloadAlbum, _ := json.Marshal(albumBody)
	albumRes := shortNoteRequest(t, router, "POST", "/api/v1/short-notes", string(payloadAlbum))
	if albumRes.Code != http.StatusCreated {
		t.Fatalf("expected 201 on create with missing album, got %d: %s", albumRes.Code, albumRes.Body.String())
	}
	var albumData struct {
		Data NoteDTO `json:"data"`
	}
	_ = json.Unmarshal(albumRes.Body.Bytes(), &albumData)
	if albumData.Data.MediaAttachment == nil || albumData.Data.MediaAttachment.Type != "album" || albumData.Data.MediaAttachment.ID != missingAlbumID {
		t.Fatalf("expected fallback DTO for missing album, got %+v", albumData.Data.MediaAttachment)
	}

	missingVideoID := uuid.New()
	videoBody := map[string]any{
		"content":               "引用不存在的视频",
		"media_attachment_type": "video",
		"media_attachment_id":   missingVideoID.String(),
	}
	payloadVideo, _ := json.Marshal(videoBody)
	videoRes := shortNoteRequest(t, router, "POST", "/api/v1/short-notes", string(payloadVideo))
	if videoRes.Code != http.StatusCreated {
		t.Fatalf("expected 201 on create with missing video, got %d: %s", videoRes.Code, videoRes.Body.String())
	}
	var videoData struct {
		Data NoteDTO `json:"data"`
	}
	_ = json.Unmarshal(videoRes.Body.Bytes(), &videoData)
	if videoData.Data.MediaAttachment == nil || videoData.Data.MediaAttachment.Type != "video" || videoData.Data.MediaAttachment.ID != missingVideoID {
		t.Fatalf("expected fallback DTO for missing video, got %+v", videoData.Data.MediaAttachment)
	}
}

