package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func newVideoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.UserDefaultChannel{},
		&model.Video{},
		&model.VideoBookmark{},
		&model.ChannelBookmark{},
		&model.VideoProcessingJob{},
		&model.VideoTag{},
		&model.VideoCollection{},
		&model.VideoTagRelation{},
		&model.Comment{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
	)
	return db
}

func signedVideoListTokenForTest(t *testing.T, user model.User) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	require.NoError(t, err)
	return signed
}

func TestSetupVideoRoutesListUsesOptionalAuthForOwnerCollection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newVideoTestDB(t)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })

	owner := seedVideoUser(t, db)
	other := seedVideoUser(t, db)
	ownerChannel := seedVideoChannel(t, db, owner.UUID, "Owner Videos")
	otherChannel := seedVideoChannel(t, db, other.UUID, "Other Videos")
	ownerCollection := model.Collection{ChannelID: ownerChannel.ID, Name: "Owner Collection"}
	otherCollection := model.Collection{ChannelID: otherChannel.ID, Name: "Other Collection"}
	require.NoError(t, db.Create(&ownerCollection).Error)
	require.NoError(t, db.Create(&otherCollection).Error)

	ownerPublic := seedVideoWithState(t, db, owner.UUID, "published", "public")
	ownerDraft := seedVideoWithState(t, db, owner.UUID, "draft", "public")
	ownerPrivate := seedVideoWithState(t, db, owner.UUID, "published", "private")
	otherPublic := seedVideoWithState(t, db, other.UUID, "published", "public")
	otherPrivate := seedVideoWithState(t, db, other.UUID, "published", "private")
	for _, video := range []model.Video{ownerPublic, ownerDraft, ownerPrivate} {
		require.NoError(t, db.Model(&video).Update("channel_id", ownerChannel.ID).Error)
	}
	for _, video := range []model.Video{otherPublic, otherPrivate} {
		require.NoError(t, db.Model(&video).Update("channel_id", otherChannel.ID).Error)
	}
	for _, link := range []model.VideoCollection{
		{VideoID: ownerPublic.ID, CollectionID: ownerCollection.ID},
		{VideoID: ownerDraft.ID, CollectionID: ownerCollection.ID},
		{VideoID: ownerPrivate.ID, CollectionID: ownerCollection.ID},
		{VideoID: otherPublic.ID, CollectionID: otherCollection.ID},
		{VideoID: otherPrivate.ID, CollectionID: otherCollection.ID},
	} {
		require.NoError(t, db.Create(&link).Error)
	}

	r := gin.New()
	SetupVideoRoutes(r, db, nil)
	requestIDs := func(collectionID uuid.UUID, authorization string) []uuid.UUID {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/videos?collection_id="+collectionID.String(), nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var videos []model.Video
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &videos))
		ids := make([]uuid.UUID, 0, len(videos))
		for _, video := range videos {
			ids = append(ids, video.ID)
		}
		return ids
	}

	require.ElementsMatch(t, []uuid.UUID{ownerPublic.ID}, requestIDs(ownerCollection.ID, ""))
	require.ElementsMatch(t, []uuid.UUID{
		ownerPublic.ID,
		ownerDraft.ID,
		ownerPrivate.ID,
	}, requestIDs(ownerCollection.ID, "Bearer "+signedVideoListTokenForTest(t, owner)))
	require.ElementsMatch(t, []uuid.UUID{otherPublic.ID}, requestIDs(
		otherCollection.ID,
		"Bearer "+signedVideoListTokenForTest(t, owner),
	))
	require.ElementsMatch(t, []uuid.UUID{ownerPublic.ID}, requestIDs(ownerCollection.ID, "Bearer invalid.token"))

	requestChannelIDs := func(channelID uuid.UUID, authorization string) []uuid.UUID {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/videos?channel_id="+channelID.String(), nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var videos []model.Video
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &videos))
		ids := make([]uuid.UUID, 0, len(videos))
		for _, video := range videos {
			ids = append(ids, video.ID)
		}
		return ids
	}

	ownerAuthorization := "Bearer " + signedVideoListTokenForTest(t, owner)
	require.ElementsMatch(t, []uuid.UUID{ownerPublic.ID}, requestChannelIDs(ownerChannel.ID, ""))
	require.ElementsMatch(t, []uuid.UUID{
		ownerPublic.ID,
		ownerDraft.ID,
		ownerPrivate.ID,
	}, requestChannelIDs(ownerChannel.ID, ownerAuthorization))
	require.ElementsMatch(t, []uuid.UUID{ownerPublic.ID}, requestChannelIDs(ownerChannel.ID, "Bearer invalid.token"))
	require.ElementsMatch(t, []uuid.UUID{otherPublic.ID}, requestChannelIDs(otherChannel.ID, ownerAuthorization))
}

func TestGetVideosFiltersCurrentUsersSubscribedChannels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	viewer := seedVideoUser(t, db)
	creator := seedVideoUser(t, db)
	subscribedChannel := seedVideoChannel(t, db, creator.UUID, "Subscribed Channel")
	otherChannel := seedVideoChannel(t, db, creator.UUID, "Other Channel")
	subscribedVideo := seedVideo(t, db, creator.UUID)
	otherVideo := seedVideo(t, db, creator.UUID)
	require.NoError(t, db.Model(&subscribedVideo).Update("channel_id", subscribedChannel.ID).Error)
	require.NoError(t, db.Model(&otherVideo).Update("channel_id", otherChannel.ID).Error)

	source := model.FeedSource{
		SourceType: "internal_channel",
		SourceID:   &subscribedChannel.ID,
		Hash:       "video-subscription-" + subscribedChannel.ID.String(),
		Title:      subscribedChannel.Name,
	}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&model.Subscription{UserID: viewer.UUID, FeedSourceID: source.ID}).Error)

	r := gin.New()
	r.GET("/api/v1/videos", func(c *gin.Context) {
		c.Set("user_id", viewer.UUID)
		GetVideos(db)(c)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos?subscribed=true", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var videos []model.Video
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &videos))
	require.Len(t, videos, 1)
	require.Equal(t, subscribedVideo.ID, videos[0].ID)
}

func TestGetVideosRequiresAuthenticationForSubscribedChannels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	r := gin.New()
	r.GET("/api/v1/videos", GetVideos(db))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos?subscribed=true&page=1&limit=2", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

func TestGetVideosPaginatesCurrentUsersSubscribedChannels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	viewer := seedVideoUser(t, db)
	creator := seedVideoUser(t, db)
	subscribedChannel := seedVideoChannel(t, db, creator.UUID, "Paged Subscribed Channel")
	otherChannel := seedVideoChannel(t, db, creator.UUID, "Paged Other Channel")

	createdAt := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	newest := seedVideo(t, db, creator.UUID)
	middle := seedVideo(t, db, creator.UUID)
	oldest := seedVideo(t, db, creator.UUID)
	unsubscribed := seedVideo(t, db, creator.UUID)
	for index, video := range []model.Video{newest, middle, oldest} {
		require.NoError(t, db.Model(&video).Updates(map[string]any{
			"channel_id": subscribedChannel.ID,
			"created_at": createdAt.Add(-time.Duration(index) * time.Hour),
		}).Error)
	}
	require.NoError(t, db.Model(&unsubscribed).Updates(map[string]any{
		"channel_id": otherChannel.ID,
		"created_at": createdAt.Add(time.Hour),
	}).Error)

	source := model.FeedSource{
		SourceType: "internal_channel",
		SourceID:   &subscribedChannel.ID,
		Hash:       "paged-video-subscription-" + subscribedChannel.ID.String(),
		Title:      subscribedChannel.Name,
	}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&model.Subscription{UserID: viewer.UUID, FeedSourceID: source.ID}).Error)

	r := gin.New()
	r.GET("/api/v1/videos", func(c *gin.Context) {
		c.Set("user_id", viewer.UUID)
		GetVideos(db)(c)
	})

	requestPage := func(page int) []model.Video {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/videos?subscribed=true&sort=latest&limit=2&page=%d", page), nil)
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var videos []model.Video
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &videos))
		return videos
	}

	pageOne := requestPage(1)
	pageTwo := requestPage(2)
	require.Len(t, pageOne, 2)
	require.Len(t, pageTwo, 1)
	require.Equal(t, []uuid.UUID{newest.ID, middle.ID}, []uuid.UUID{pageOne[0].ID, pageOne[1].ID})
	require.Equal(t, []uuid.UUID{oldest.ID}, []uuid.UUID{pageTwo[0].ID})
	require.NotEqual(t, pageOne[0].ID, pageTwo[0].ID)
	require.NotEqual(t, pageOne[1].ID, pageTwo[0].ID)
	for _, video := range append(pageOne, pageTwo...) {
		require.NotEqual(t, unsubscribed.ID, video.ID)
	}
}

func TestGetVideosUsesStableUniqueOrderingAcrossPages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	creator := seedVideoUser(t, db)
	createdAt := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)

	ids := map[int]uuid.UUID{
		1: uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		2: uuid.MustParse("00000000-0000-4000-8000-000000000002"),
		3: uuid.MustParse("00000000-0000-4000-8000-000000000003"),
		4: uuid.MustParse("00000000-0000-4000-8000-000000000004"),
	}
	for _, number := range []int{2, 4, 1, 3} {
		video := model.Video{
			Base:        model.Base{ID: ids[number], CreatedAt: createdAt},
			UserID:      creator.UUID,
			Title:       fmt.Sprintf("stable video %d", number),
			StorageType: "local",
			VideoURL:    fmt.Sprintf("https://example.com/stable-%d.mp4", number),
			Status:      "published",
			Visibility:  "public",
			ViewCount:   100,
		}
		require.NoError(t, db.Create(&video).Error)
	}

	r := gin.New()
	r.GET("/api/v1/videos", GetVideos(db))
	expected := []uuid.UUID{ids[4], ids[3], ids[2], ids[1]}
	for _, sort := range []string{"popular", "latest"} {
		t.Run(sort, func(t *testing.T) {
			requestPage := func(page int) []uuid.UUID {
				t.Helper()
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/videos?sort=%s&limit=2&page=%d", sort, page), nil)
				r.ServeHTTP(w, req)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var videos []model.Video
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &videos))
				result := make([]uuid.UUID, 0, len(videos))
				for _, video := range videos {
					result = append(result, video.ID)
				}
				return result
			}

			pageOne := requestPage(1)
			pageTwo := requestPage(2)
			require.Equal(t, expected[:2], pageOne)
			require.Equal(t, expected[2:], pageTwo)
			require.Equal(t, expected, append(pageOne, pageTwo...))
		})
	}
}

func seedVideoUser(t *testing.T, db *gorm.DB) model.User {
	t.Helper()
	u := model.User{Username: "vuser_" + uuid.NewString()[:8], Email: uuid.NewString() + "@test.com", Password: "x", IsActive: true}
	require.NoError(t, db.Create(&u).Error)
	return u
}

func seedVideo(t *testing.T, db *gorm.DB, userID uuid.UUID) model.Video {
	t.Helper()
	v := model.Video{
		UserID:      userID,
		Title:       "test video",
		StorageType: "local",
		VideoURL:    "https://example.com/test.mp4",
		Status:      "published",
		Visibility:  "public",
	}
	require.NoError(t, db.Create(&v).Error)
	return v
}

func seedVideoWithState(t *testing.T, db *gorm.DB, userID uuid.UUID, status string, visibility string) model.Video {
	t.Helper()
	v := model.Video{
		UserID:      userID,
		Title:       "test video",
		StorageType: "local",
		VideoURL:    "https://example.com/test.mp4",
		Status:      status,
		Visibility:  visibility,
	}
	require.NoError(t, db.Create(&v).Error)
	return v
}

func withVideoAuth(userID uuid.UUID, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("userID", userID)
		h(c)
	}
}

func seedVideoChannel(t *testing.T, db *gorm.DB, userID uuid.UUID, name string) model.Channel {
	t.Helper()
	channel := model.Channel{
		UserID:      &userID,
		Name:        name,
		Slug:        strings.ToLower(strings.ReplaceAll(name, " ", "-")) + "-" + uuid.NewString()[:8],
		ContentType: "video",
	}
	require.NoError(t, db.Create(&channel).Error)
	return channel
}

func seedVideoCollection(t *testing.T, db *gorm.DB, channelID uuid.UUID, name string) model.Collection {
	t.Helper()
	collection := model.Collection{
		ChannelID: channelID,
		Name:      name,
	}
	require.NoError(t, db.Create(&collection).Error)
	return collection
}

func TestCreateVideoBookmarkIsIdempotentWithRepeatedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)

	r := gin.New()
	r.POST("/api/v1/videos/bookmarks", withVideoAuth(user.UUID, CreateVideoBookmark(db)))

	body, err := json.Marshal(map[string]any{"video_id": video.ID})
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/bookmarks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
	}

	var count int64
	require.NoError(t, db.Model(&model.VideoBookmark{}).Where("user_id = ? AND video_id = ?", user.UUID, video.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestListAndDeleteVideoBookmarks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	otherUser := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)
	otherVideo := seedVideo(t, db, otherUser.UUID)
	bookmark := model.VideoBookmark{UserID: user.UUID, VideoID: video.ID}
	otherBookmark := model.VideoBookmark{UserID: otherUser.UUID, VideoID: otherVideo.ID}
	require.NoError(t, db.Create(&bookmark).Error)
	require.NoError(t, db.Create(&otherBookmark).Error)

	r := gin.New()
	r.GET("/api/v1/videos/bookmarks", withVideoAuth(user.UUID, GetVideoBookmarks(db)))
	r.DELETE("/api/v1/videos/bookmarks/:id", withVideoAuth(user.UUID, DeleteVideoBookmark(db)))

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/videos/bookmarks", nil)
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	require.Equal(t, http.StatusOK, listW.Code, "body=%s", listW.Body.String())
	require.Contains(t, listW.Body.String(), bookmark.ID.String())
	require.NotContains(t, listW.Body.String(), otherBookmark.ID.String())

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/videos/bookmarks/"+bookmark.ID.String(), nil)
	deleteW := httptest.NewRecorder()
	r.ServeHTTP(deleteW, deleteReq)
	require.Equal(t, http.StatusOK, deleteW.Code, "body=%s", deleteW.Body.String())

	var count int64
	require.NoError(t, db.Model(&model.VideoBookmark{}).Where("id = ?", bookmark.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestVideoBookmarksSupportPopularSort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	hotVideo := seedVideo(t, db, user.UUID)
	coldVideo := seedVideo(t, db, user.UUID)
	require.NoError(t, db.Model(&hotVideo).Update("view_count", 100).Error)
	require.NoError(t, db.Model(&coldVideo).Update("view_count", 10).Error)
	require.NoError(t, db.Create(&model.VideoBookmark{UserID: user.UUID, VideoID: coldVideo.ID}).Error)
	require.NoError(t, db.Create(&model.VideoBookmark{UserID: user.UUID, VideoID: hotVideo.ID}).Error)

	r := gin.New()
	r.GET("/api/v1/videos/bookmarks", withVideoAuth(user.UUID, GetVideoBookmarks(db)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/bookmarks?sort=popular", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp struct {
		Data []struct {
			VideoID string `json:"video_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 2)
	require.Equal(t, hotVideo.ID.String(), resp.Data[0].VideoID)
}

func TestCreateChannelBookmarkIsIdempotentWithRepeatedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	channel := seedVideoChannel(t, db, user.UUID, "Bookmarks Channel")

	r := gin.New()
	r.POST("/api/v1/videos/channel-bookmarks", withVideoAuth(user.UUID, CreateChannelBookmark(db)))

	body, err := json.Marshal(map[string]any{"channel_id": channel.ID})
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/channel-bookmarks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
	}

	var count int64
	require.NoError(t, db.Model(&model.ChannelBookmark{}).Where("user_id = ? AND channel_id = ?", user.UUID, channel.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestListAndDeleteChannelBookmarks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	otherUser := seedVideoUser(t, db)
	channel := seedVideoChannel(t, db, user.UUID, "Bookmarked Channel")
	otherChannel := seedVideoChannel(t, db, otherUser.UUID, "Other Channel")
	bookmark := model.ChannelBookmark{UserID: user.UUID, ChannelID: channel.ID, Kind: "video_channel"}
	otherBookmark := model.ChannelBookmark{UserID: otherUser.UUID, ChannelID: otherChannel.ID}
	require.NoError(t, db.Create(&bookmark).Error)
	require.NoError(t, db.Create(&otherBookmark).Error)

	r := gin.New()
	r.GET("/api/v1/videos/channel-bookmarks", withVideoAuth(user.UUID, GetChannelBookmarks(db)))
	r.DELETE("/api/v1/videos/channel-bookmarks/:id", withVideoAuth(user.UUID, DeleteChannelBookmark(db)))

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/videos/channel-bookmarks", nil)
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	require.Equal(t, http.StatusOK, listW.Code, "body=%s", listW.Body.String())
	require.Contains(t, listW.Body.String(), bookmark.ID.String())
	require.NotContains(t, listW.Body.String(), otherBookmark.ID.String())

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/videos/channel-bookmarks/"+bookmark.ID.String(), nil)
	deleteW := httptest.NewRecorder()
	r.ServeHTTP(deleteW, deleteReq)
	require.Equal(t, http.StatusOK, deleteW.Code, "body=%s", deleteW.Body.String())

	var count int64
	require.NoError(t, db.Model(&model.ChannelBookmark{}).Where("id = ?", bookmark.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestVideoChannelBookmarksExcludePodcastShowBookmarks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	videoChannel := seedVideoChannel(t, db, user.UUID, "Video Channel")
	podcastShow := seedVideoChannel(t, db, user.UUID, "Podcast Show")

	videoBookmark := model.ChannelBookmark{UserID: user.UUID, ChannelID: videoChannel.ID, Kind: "video_channel"}
	podcastBookmark := model.ChannelBookmark{UserID: user.UUID, ChannelID: podcastShow.ID, Kind: "podcast_show"}
	require.NoError(t, db.Create(&videoBookmark).Error)
	require.NoError(t, db.Create(&podcastBookmark).Error)

	r := gin.New()
	r.GET("/api/v1/videos/channel-bookmarks", withVideoAuth(user.UUID, GetChannelBookmarks(db)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/channel-bookmarks", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	require.Contains(t, w.Body.String(), videoBookmark.ID.String())
	require.NotContains(t, w.Body.String(), podcastBookmark.ID.String())
}

func TestSetupVideoRoutesMountsRecommendationItemsEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)

	video := model.Video{
		UserID:       user.UUID,
		Title:        "推荐视频",
		Description:  "这是一个适合推荐的视频。",
		StorageType:  "local",
		VideoURL:     "https://example.com/recommend.mp4",
		ThumbnailURL: "https://example.com/recommend.jpg",
		Status:       "published",
		Visibility:   "public",
		ViewCount:    120,
	}
	require.NoError(t, db.Create(&video).Error)

	r := gin.New()
	SetupVideoRoutes(r, db, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/recommend/items?mode=hot&page=1&page_size=20", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("expected recommendation route to be mounted, got 404: %s", w.Body.String())
	}
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Summary     string `json:"summary"`
			ContentType string `json:"content_type"`
			TargetPath  string `json:"target_path"`
			ScoreLabel  string `json:"score_label"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.NotEmpty(t, payload.Data, "body=%s", w.Body.String())

	first := payload.Data[0]
	if first.ID == "" || first.Title == "" || first.TargetPath == "" || first.ScoreLabel == "" || first.ContentType != "video" {
		t.Fatalf("expected recommendation dto fields, got %#v", first)
	}
}

func videoMultipartBody(t *testing.T, field, filename, contentType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="` + field + `"; filename="` + filename + `"`},
		"Content-Type":        {contentType},
	})
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return body, writer.FormDataContentType()
}

func TestUploadVideoCoverRejectsForgedPNGContentType(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "local")
	gin.SetMode(gin.TestMode)
	userID := uuid.New()

	r := gin.New()
	r.POST("/api/v1/videos/upload-cover", withVideoAuth(userID, UploadVideoCover(nil)))

	body, contentType := videoMultipartBody(t, "cover", "cover.png", "image/png", []byte("not a png"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/upload-cover", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUploadVideoFileRejectsForgedMP4ContentType(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "local")
	gin.SetMode(gin.TestMode)
	userID := uuid.New()

	r := gin.New()
	r.POST("/api/v1/videos/upload-video", withVideoAuth(userID, UploadVideoFile(nil)))

	body, contentType := videoMultipartBody(t, "video", "clip.mp4", "video/mp4", []byte("not an mp4"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/upload-video", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateVideoRollsBackWhenCollectionAssignmentFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)

	r := gin.New()
	r.POST("/api/v1/videos", withVideoAuth(user.UUID, CreateVideo(db)))

	body := strings.NewReader(`{
		"title":"rollback video",
		"storage_type":"local",
		"video_url":"https://example.com/test.mp4",
		"tags":["rollback-tag"],
		"collection_ids":["` + uuid.NewString() + `"]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)

	var videoCount int64
	require.NoError(t, db.Model(&model.Video{}).Where("title = ?", "rollback video").Count(&videoCount).Error)
	require.Zero(t, videoCount)

	var jobCount int64
	require.NoError(t, db.Model(&model.VideoProcessingJob{}).Count(&jobCount).Error)
	require.Zero(t, jobCount)

	var tagCount int64
	require.NoError(t, db.Model(&model.VideoTag{}).Where("name = ?", "rollback-tag").Count(&tagCount).Error)
	require.Zero(t, tagCount)
}

func TestUpdateVideoUsesNewChannelWhenAssigningCollections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	oldChannel := seedVideoChannel(t, db, user.UUID, "Old Channel")
	newChannel := seedVideoChannel(t, db, user.UUID, "New Channel")
	newCollection := seedVideoCollection(t, db, newChannel.ID, "New Collection")
	video := seedVideo(t, db, user.UUID)
	require.NoError(t, db.Model(&video).Update("channel_id", oldChannel.ID).Error)

	r := gin.New()
	r.PUT("/api/v1/videos/:id", withVideoAuth(user.UUID, UpdateVideo(db)))

	body := strings.NewReader(`{
		"channel_id":"` + newChannel.ID.String() + `",
		"collection_ids":["` + newCollection.ID.String() + `"]
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/videos/"+video.ID.String(), body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var updated model.Video
	require.NoError(t, db.Preload("Collections").First(&updated, "id = ?", video.ID).Error)
	require.NotNil(t, updated.ChannelID)
	require.Equal(t, newChannel.ID, *updated.ChannelID)
	require.Len(t, updated.Collections, 1)
	require.Equal(t, newCollection.ID, updated.Collections[0].ID)
}

func TestUpdateVideoRollsBackWhenCollectionAssignmentFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	channel := seedVideoChannel(t, db, user.UUID, "Rollback Channel")
	video := seedVideo(t, db, user.UUID)
	require.NoError(t, db.Model(&video).Update("channel_id", channel.ID).Error)

	r := gin.New()
	r.PUT("/api/v1/videos/:id", withVideoAuth(user.UUID, UpdateVideo(db)))

	body := strings.NewReader(`{
		"title":"updated before failure",
		"tags":["updated-tag"],
		"collection_ids":["` + uuid.NewString() + `"]
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/videos/"+video.ID.String(), body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)

	var updated model.Video
	require.NoError(t, db.First(&updated, "id = ?", video.ID).Error)
	require.Equal(t, "test video", updated.Title)

	var tagCount int64
	require.NoError(t, db.Model(&model.VideoTag{}).Where("name = ?", "updated-tag").Count(&tagCount).Error)
	require.Zero(t, tagCount)
}

func TestCreateVideoRequiresOwnedVideoChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	otherUser := seedVideoUser(t, db)
	ownedVideoChannel := seedVideoChannel(t, db, user.UUID, "Owned Video Channel")
	otherUsersVideoChannel := seedVideoChannel(t, db, otherUser.UUID, "Other Users Video Channel")
	mismatchedChannel := model.Channel{
		UserID:      &user.UUID,
		Name:        "Users Blog Channel",
		Slug:        "users-blog-channel-" + uuid.NewString()[:8],
		ContentType: "blog",
	}
	require.NoError(t, db.Create(&mismatchedChannel).Error)

	r := gin.New()
	r.POST("/api/v1/videos", withVideoAuth(user.UUID, CreateVideo(db)))

	cases := []struct {
		name      string
		channelID string
		want      int
	}{
		{name: "owned video channel succeeds", channelID: ownedVideoChannel.ID.String(), want: http.StatusCreated},
		{name: "other users channel is forbidden", channelID: otherUsersVideoChannel.ID.String(), want: http.StatusForbidden},
		{name: "content type mismatch is rejected", channelID: mismatchedChannel.ID.String(), want: http.StatusBadRequest},
		{name: "missing channel is not found", channelID: uuid.NewString(), want: http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(`{
				"channel_id":"` + tc.channelID + `",
				"title":"channel-bound video",
				"storage_type":"local",
				"video_url":"https://example.com/test.mp4"
			}`)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/videos", body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, tc.want, w.Code, "body=%s", w.Body.String())
		})
	}
}

func TestUpdateVideoRejectsChannelOwnershipAndTypeMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	otherUser := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)
	currentChannel := seedVideoChannel(t, db, user.UUID, "Current Video Channel")
	otherUsersVideoChannel := seedVideoChannel(t, db, otherUser.UUID, "Other Update Video Channel")
	mismatchedChannel := model.Channel{
		UserID:      &user.UUID,
		Name:        "User Podcast Channel",
		Slug:        "user-podcast-channel-" + uuid.NewString()[:8],
		ContentType: "podcast",
	}
	require.NoError(t, db.Create(&mismatchedChannel).Error)
	require.NoError(t, db.Model(&video).Update("channel_id", currentChannel.ID).Error)

	r := gin.New()
	r.PUT("/api/v1/videos/:id", withVideoAuth(user.UUID, UpdateVideo(db)))

	cases := []struct {
		name      string
		channelID string
		want      int
	}{
		{name: "other users channel is forbidden", channelID: otherUsersVideoChannel.ID.String(), want: http.StatusForbidden},
		{name: "content type mismatch is rejected", channelID: mismatchedChannel.ID.String(), want: http.StatusBadRequest},
		{name: "missing channel is not found", channelID: uuid.NewString(), want: http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(`{"channel_id":"` + tc.channelID + `"}`)
			req := httptest.NewRequest(http.MethodPut, "/api/v1/videos/"+video.ID.String(), body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, tc.want, w.Code, "body=%s", w.Body.String())
		})
	}
}

func TestGetVideoReturnsPublishedPublicVideo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	video := seedVideoWithState(t, db, user.UUID, "published", "public")

	r := gin.New()
	r.GET("/api/v1/videos/:id", GetVideo(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/"+video.ID.String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), video.ID.String())
}

func TestGetVideoRejectsAnonymousAccessToNonPublicVideos(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)

	cases := []struct {
		name       string
		status     string
		visibility string
	}{
		{name: "draft", status: "draft", visibility: "public"},
		{name: "private", status: "published", visibility: "private"},
		{name: "followers", status: "published", visibility: "followers"},
		{name: "unpublished_private", status: "draft", visibility: "private"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video := seedVideoWithState(t, db, user.UUID, tc.status, tc.visibility)

			r := gin.New()
			r.GET("/api/v1/videos/:id", GetVideo(db))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/"+video.ID.String(), nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusNotFound, w.Code)
			require.NotContains(t, w.Body.String(), video.ID.String())
			require.NotContains(t, w.Body.String(), video.VideoURL)
		})
	}
}

func TestIncrementVideoViewReturnsUpdatedCountAndRejectsUnavailableVideos(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)

	publicVideo := seedVideoWithState(t, db, user.UUID, "published", "public")
	require.NoError(t, db.Model(&publicVideo).Update("view_count", 7).Error)

	router := gin.New()
	router.POST("/api/v1/videos/:id/view", IncrementVideoView(db))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/"+publicVideo.ID.String()+"/view", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var payload struct {
		OK        bool `json:"ok"`
		ViewCount int  `json:"view_count"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.True(t, payload.OK)
	require.Equal(t, 8, payload.ViewCount)
	var stored model.Video
	require.NoError(t, db.First(&stored, "id = ?", publicVideo.ID).Error)
	require.Equal(t, 8, stored.ViewCount)

	unavailable := []struct {
		name  string
		video model.Video
	}{
		{name: "private", video: seedVideoWithState(t, db, user.UUID, "published", "private")},
		{name: "draft", video: seedVideoWithState(t, db, user.UUID, "draft", "public")},
	}
	deleted := seedVideoWithState(t, db, user.UUID, "published", "public")
	require.NoError(t, db.Delete(&deleted).Error)
	unavailable = append(unavailable, struct {
		name  string
		video model.Video
	}{name: "deleted", video: deleted})

	for _, tc := range unavailable {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, db.Unscoped().Model(&model.Video{}).Where("id = ?", tc.video.ID).Update("view_count", 7).Error)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/"+tc.video.ID.String()+"/view", nil)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
			require.NotContains(t, w.Body.String(), tc.video.ID.String())

			var count int
			require.NoError(t, db.Unscoped().Model(&model.Video{}).Select("view_count").Where("id = ?", tc.video.ID).Scan(&count).Error)
			require.Equal(t, 7, count)
		})
	}
}

func TestIncrementVideoViewDoesNotLoseConcurrentUpdates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	user := seedVideoUser(t, db)
	video := seedVideoWithState(t, db, user.UUID, "published", "public")

	router := gin.New()
	router.POST("/api/v1/videos/:id/view", IncrementVideoView(db))

	const increments = 8
	statuses := make(chan int, increments)
	var wg sync.WaitGroup
	for i := 0; i < increments; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/"+video.ID.String()+"/view", nil)
			router.ServeHTTP(w, req)
			statuses <- w.Code
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		require.Equal(t, http.StatusOK, status)
	}

	var stored model.Video
	require.NoError(t, db.First(&stored, "id = ?", video.ID).Error)
	require.Equal(t, increments, stored.ViewCount)
}

func TestIncrementVideoViewReturnsNotFoundWhenVideoBecomesUnavailableBeforeCountRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	video := seedVideoWithState(t, db, user.UUID, "published", "public")
	require.NoError(t, db.Model(&video).Update("view_count", 7).Error)

	triggerName := "hide_video_after_view_increment"
	triggerSQL := fmt.Sprintf(`CREATE TRIGGER %s
		AFTER UPDATE OF view_count ON videos
		WHEN NEW.id = '%s'
		BEGIN
			UPDATE videos SET visibility = 'private' WHERE id = NEW.id;
		END`, triggerName, video.ID.String())
	require.NoError(t, db.Exec(triggerSQL).Error)
	defer db.Exec("DROP TRIGGER IF EXISTS " + triggerName)

	router := gin.New()
	router.POST("/api/v1/videos/:id/view", IncrementVideoView(db))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/"+video.ID.String()+"/view", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), video.ID.String())
	var stored model.Video
	require.NoError(t, db.First(&stored, "id = ?", video.ID).Error)
	require.Equal(t, 8, stored.ViewCount)
	require.Equal(t, "private", stored.Visibility)
}

func TestGetVideoCommentsReturnsMixedComments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)

	// 普通评论
	c1 := model.Comment{TargetType: "video", TargetID: video.ID, UserID: model.NewNullableUserUUID(user.UUID), Content: "great video", Status: "visible"}
	// 时间点评论
	ts := 92
	c2 := model.Comment{TargetType: "video", TargetID: video.ID, UserID: model.NewNullableUserUUID(user.UUID), Content: "this part!", TimestampSec: &ts, Status: "visible"}
	require.NoError(t, db.Create(&c1).Error)
	require.NoError(t, db.Create(&c2).Error)

	r := gin.New()
	r.GET("/api/v1/videos/:id/comments", GetVideoComments(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/"+video.ID.String()+"/comments", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "great video")
	require.Contains(t, w.Body.String(), "this part!")
}

func TestGetVideoCommentsRejectsNonPublicVideos(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)

	cases := []struct {
		name       string
		status     string
		visibility string
	}{
		{name: "draft", status: "draft", visibility: "public"},
		{name: "private", status: "published", visibility: "private"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video := seedVideoWithState(t, db, user.UUID, tc.status, tc.visibility)
			comment := model.Comment{TargetType: "video", TargetID: video.ID, UserID: model.NewNullableUserUUID(user.UUID), Content: "hidden comment", Status: "visible"}
			require.NoError(t, db.Create(&comment).Error)

			r := gin.New()
			r.GET("/api/v1/videos/:id/comments", GetVideoComments(db))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/"+video.ID.String()+"/comments", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusNotFound, w.Code)
			require.NotContains(t, w.Body.String(), "hidden comment")
		})
	}
}

func TestCreateVideoCommentWithTimestamp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)

	r := gin.New()
	r.POST("/api/v1/videos/:id/comments", withVideoAuth(user.UUID, CreateVideoComment(db)))

	body := strings.NewReader(`{"content":"这一段很强","timestamp_sec":92}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/"+video.ID.String()+"/comments", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var comment model.Comment
	require.NoError(t, db.First(&comment).Error)
	require.Equal(t, "video", comment.TargetType)
	require.Equal(t, video.ID, comment.TargetID)
	require.NotNil(t, comment.TimestampSec)
	require.Equal(t, 92, *comment.TimestampSec)
}

func TestCreateVideoCommentRejectsNegativeTimestamp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)

	r := gin.New()
	r.POST("/api/v1/videos/:id/comments", withVideoAuth(user.UUID, CreateVideoComment(db)))

	body := strings.NewReader(`{"content":"bad timestamp","timestamp_sec":-1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/"+video.ID.String()+"/comments", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateVideoCommentRejectsNonPublicVideos(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)

	cases := []struct {
		name       string
		status     string
		visibility string
	}{
		{name: "draft", status: "draft", visibility: "public"},
		{name: "private", status: "published", visibility: "private"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video := seedVideoWithState(t, db, user.UUID, tc.status, tc.visibility)

			r := gin.New()
			r.POST("/api/v1/videos/:id/comments", withVideoAuth(user.UUID, CreateVideoComment(db)))

			body := strings.NewReader(`{"content":"should not write"}`)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/"+video.ID.String()+"/comments", body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusNotFound, w.Code)

			var count int64
			require.NoError(t, db.Model(&model.Comment{}).Where("target_type = ? AND target_id = ?", "video", video.ID).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}
