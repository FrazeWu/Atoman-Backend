package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	revisionservice "atoman/internal/service"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMusicDiscussionMergePreservesTreesAndRollsBack(t *testing.T) {
	_, db, user := newMusicHTTPTestService(t)
	require.NoError(t, db.AutoMigrate(&model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentPublishRecord{}))
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_integrity_target ON discussion_targets(kind, resource_key)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_integrity_floor ON comment_entries(target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`).Error)
	sourceID, targetID := uuid.New(), uuid.New()
	source := model.DiscussionTarget{Kind: "music_album", ResourceID: sourceID, ResourceKey: sourceID.String(), NextFloor: 2}
	target := model.DiscussionTarget{Kind: "music_album", ResourceID: targetID, ResourceKey: targetID.String(), NextFloor: 2}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&target).Error)
	floor := 1
	root := model.CommentEntry{TargetID: source.ID, AuthorID: user.ID, FloorNumber: &floor, Content: "source", Status: "active", ReplyCount: 1}
	require.NoError(t, db.Create(&root).Error)
	reply := model.CommentEntry{TargetID: source.ID, AuthorID: user.ID, RootID: &root.ID, ReplyToID: &root.ID, Content: "reply", Status: "active"}
	require.NoError(t, db.Create(&reply).Error)
	require.NoError(t, db.Create(&model.CommentEntry{TargetID: target.ID, AuthorID: user.ID, FloorNumber: &floor, Content: "target", Status: "active"}).Error)
	rollback := errors.New("rollback")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := mergeMusicDiscussionTargets(tx, "album", sourceID, "album", targetID); err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	var unchanged model.CommentEntry
	require.NoError(t, db.First(&unchanged, "id = ?", root.ID).Error)
	require.Equal(t, source.ID, unchanged.TargetID)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return mergeMusicDiscussionTargets(tx, "album", sourceID, "album", targetID) }))
	require.NoError(t, db.First(&target, "id = ?", target.ID).Error)
	require.Equal(t, 3, target.CommentCount)
	require.Equal(t, 2, target.RootCount)
	require.Equal(t, 3, target.NextFloor)
	require.NoError(t, db.First(&root, "id = ?", root.ID).Error)
	require.Equal(t, target.ID, root.TargetID)
	require.Equal(t, 2, *root.FloorNumber)
	require.NoError(t, db.First(&reply, "id = ?", reply.ID).Error)
	require.Equal(t, target.ID, reply.TargetID)
	require.Equal(t, root.ID, *reply.RootID)
}

func TestMusicSongMergePreservesLyricsHistoryAndAnchors(t *testing.T) {
	svc, db, user := newMusicHTTPTestService(t)
	source := model.Song{Title: "source", AudioURL: "/source.mp3"}
	target := model.Song{Title: "target", AudioURL: "/target.mp3"}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&target).Error)
	sourceLyrics, err := svc.SaveSongLyrics(user, source.ID, SaveLyricsInput{Content: "source line", Format: "plain"})
	require.NoError(t, err)
	annotation, err := svc.CreateLyricAnnotation(user, source.ID, CreateAnnotationInput{LineID: sourceLyrics.Lines[0].ID, SelectedText: "source", EndOffset: 6, Body: "keep me"})
	require.NoError(t, err)
	_, err = svc.SaveSongLyrics(user, target.ID, SaveLyricsInput{Content: "target line", Format: "plain"})
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return mergeSongLyrics(tx, source.ID, target.ID) }))
	var versions []model.MusicSongLyricVersion
	require.NoError(t, db.Where("song_id = ?", target.ID).Order("version").Find(&versions).Error)
	require.GreaterOrEqual(t, len(versions), 2)
	contents := map[string]bool{}
	for _, v := range versions {
		contents[v.Content] = true
	}
	require.True(t, contents["source line"])
	require.True(t, contents["target line"])
	var moved model.MusicLyricAnnotation
	require.NoError(t, db.First(&moved, "id = ?", annotation.ID).Error)
	require.Equal(t, target.ID, moved.SongID)
	require.Equal(t, "needs_rebind", moved.Status)
	_, err = svc.SaveSongLyrics(user, target.ID, SaveLyricsInput{Content: "next line", Format: "plain"})
	require.NoError(t, err, "merged version numbering must allow subsequent edits")
}

func TestMusicTagMergeAndImportPreserveVotes(t *testing.T) {
	_, db, user := newMusicHTTPTestService(t)
	sourceID, targetID := uuid.New(), uuid.New()
	tag := model.MusicTag{Name: "Folk", NormalizedName: "folk", Kind: model.MusicTagKindType, CreatedBy: user.ID}
	require.NoError(t, db.Create(&tag).Error)
	source := model.MusicTagAssignment{EntityType: "album", EntityID: sourceID, TagID: tag.ID, CreatedBy: user.ID}
	target := model.MusicTagAssignment{EntityType: "album", EntityID: targetID, TagID: tag.ID, CreatedBy: user.ID}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&target).Error)
	otherUser := uuid.New()
	require.NoError(t, db.Create(&model.MusicTagVote{AssignmentID: source.ID, UserID: user.ID, Vote: "up"}).Error)
	require.NoError(t, db.Create(&model.MusicTagVote{AssignmentID: source.ID, UserID: otherUser, Vote: "up"}).Error)
	require.NoError(t, db.Create(&model.MusicTagVote{AssignmentID: target.ID, UserID: user.ID, Vote: "down"}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return mergeMusicTagAssignments(tx, "album", sourceID, "album", targetID) }))
	var votes []model.MusicTagVote
	require.NoError(t, db.Where("assignment_id = ?", target.ID).Find(&votes).Error)
	require.Len(t, votes, 2)
	for _, vote := range votes {
		if vote.UserID == user.ID {
			require.Equal(t, "down", vote.Vote, "latest vote wins")
		}
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return replaceAlbumImportTags(tx, user.ID, targetID, []AlbumImportTagPayload{{Kind: model.MusicTagKindType, Name: "Folk"}})
	}))
	var assignment model.MusicTagAssignment
	require.NoError(t, db.Where("entity_type = 'album' AND entity_id = ? AND tag_id = ?", targetID, tag.ID).First(&assignment).Error)
	require.Equal(t, target.ID, assignment.ID)
	require.NoError(t, db.Where("assignment_id = ?", assignment.ID).Find(&votes).Error)
	require.Len(t, votes, 2)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return replaceAlbumImportTags(tx, user.ID, targetID, nil) }))
	var retained model.MusicTagAssignment
	require.NoError(t, db.First(&retained, "id = ?", assignment.ID).Error, "missing external tags must not erase community votes")
}

func TestMusicPlaybackMergeUsesLatestActualPosition(t *testing.T) {
	_, db, user := newMusicHTTPTestService(t)
	source := model.Song{Title: "source", AudioURL: "/a.mp3"}
	target := model.Song{Title: "target", AudioURL: "/b.mp3"}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&target).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.MusicPlaybackProgress{UserID: user.ID, SongID: target.ID, PositionSeconds: 100, DurationSeconds: 200, ReportedAt: now.Add(-time.Minute)}).Error)
	require.NoError(t, db.Create(&model.MusicPlaybackProgress{UserID: user.ID, SongID: source.ID, PositionSeconds: 10, DurationSeconds: 200, ReportedAt: now}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return mergeSongPlayback(tx, source.ID, target.ID) }))
	var result model.MusicPlaybackProgress
	require.NoError(t, db.Where("song_id = ?", target.ID).First(&result).Error)
	require.Equal(t, float64(10), result.PositionSeconds)
}

func TestMusicStandalonePendingAnnotations(t *testing.T) {
	svc, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "standalone", AudioURL: "/single.mp3"}
	require.NoError(t, db.Create(&song).Error)
	lyrics, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	require.NoError(t, err)
	annotation, err := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{LineID: lyrics.Lines[0].ID, SelectedText: "hello", EndOffset: 5, Body: "note"})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.MusicLyricAnnotation{}).Where("id = ?", annotation.ID).Update("status", "needs_rebind").Error)
	items, err := svc.ListPendingLyricAnnotations(authctx.CurrentUser{ID: user.ID, Role: user.Role})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, song.ID.String(), items[0].SongID)
	require.Nil(t, items[0].AlbumID)
}

func TestMusicPlayEventsAreAtomicAndKeepPlaylistContext(t *testing.T) {
	svc, db, user := newMusicHTTPTestService(t)
	require.NoError(t, db.AutoMigrate(&model.MusicPlayEvent{}))
	song := model.Song{Title: "idempotent", AudioURL: "/song.mp3", AudioStatus: "ready"}
	require.NoError(t, db.Create(&song).Error)
	playlist := model.Playlist{Name: "playlist", UserID: user.ID, IsPublic: true}
	require.NoError(t, db.Create(&playlist).Error)
	require.NoError(t, db.Create(&model.PlaylistSong{PlaylistID: playlist.ID, SongID: song.ID, Position: 1}).Error)
	event := uuid.New()
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- svc.RecordSongPlayEventForViewer(&user, song.ID, &playlist.ID, &event, "user:"+user.ID.String())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.NoError(t, db.First(&song, "id = ?", song.ID).Error)
	require.Equal(t, int64(1), song.PlayCount)
	require.NoError(t, db.First(&playlist, "id = ?", playlist.ID).Error)
	require.Equal(t, int64(1), playlist.PlayCount)
	var history model.MusicListeningHistory
	require.NoError(t, db.Where("user_id = ? AND song_id = ?", user.ID, song.ID).First(&history).Error)
	require.Equal(t, int64(1), history.PlayCount)
	require.Error(t, svc.RecordSongPlayEventForViewer(&user, song.ID, nil, &event, "user:"+user.ID.String()), "cannot reuse an event for different context")
	second := uuid.New()
	require.NoError(t, svc.RecordSongPlayEventForViewer(&user, song.ID, nil, &second, "user:"+user.ID.String()))
	require.NoError(t, svc.RecordSongPlayForViewer(&user, song.ID), "legacy clients remain supported")
	require.NoError(t, db.First(&playlist, "id = ?", playlist.ID).Error)
	require.Equal(t, int64(1), playlist.PlayCount, "non-playlist plays must not increase playlist counts")
}

func TestMusicAudioLeaseReclaimFencesStaleWorker(t *testing.T) {
	_, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "lease", AudioURL: "/old.mp3"}
	require.NoError(t, db.Create(&song).Error)
	job := model.SongAudioReplacement{SongID: song.ID, RequestedBy: user.ID, AudioURL: "/new.mp3", Status: "processing", LockedBy: "worker:old"}
	require.NoError(t, db.Create(&job).Error)
	claimed := make(chan struct{})
	staleDone := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		<-claimed
		err := revisionservice.NewRevisionService(db).ApplySongAudioReplacement(job.ID, "worker:old", "/stale-promoted.mp3")
		errs <- err
		_, _ = failSongAudioReplacement(db, job.ID, "worker:old", errors.New("stale failure"))
		close(staleDone)
	}()
	// 租约到期并被重新领取后，旧 Worker 仍会继续运行。
	require.NoError(t, db.Model(&job).Update("locked_by", "worker:new").Error)
	close(claimed)
	<-staleDone
	require.ErrorIs(t, <-errs, gorm.ErrRecordNotFound)
	require.NoError(t, db.First(&song, "id = ?", song.ID).Error)
	require.Equal(t, "/old.mp3", song.AudioURL)
	require.NoError(t, db.First(&job, "id = ?", job.ID).Error)
	require.Equal(t, "processing", job.Status, "old failure must not fail the new owner's job")
	require.NoError(t, revisionservice.NewRevisionService(db).ApplySongAudioReplacement(job.ID, "worker:new", ""))
	require.NoError(t, db.First(&song, "id = ?", song.ID).Error)
	require.Equal(t, "/new.mp3", song.AudioURL)
}

func TestMusicSongRedirectsRespectTargetVisibility(t *testing.T) {
	svc, db, _ := newMusicHTTPTestService(t)
	target := model.Song{Title: "target", AudioURL: "/target.mp3"}
	require.NoError(t, db.Create(&target).Error)
	source := model.Song{Title: "source", AudioURL: "/source.mp3", Status: "closed", LifecycleStatus: model.MusicLifecycleMerged, RedirectTo: &target.ID}
	require.NoError(t, db.Create(&source).Error)
	id, err := resolveMusicSongID(db, source.ID, nil)
	require.NoError(t, err)
	require.Equal(t, target.ID, id)
	router := newMusicHTTPRouter(svc, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/music/songs/"+source.ID.String(), nil))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var payload struct {
		Data songDetailResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, target.ID, payload.Data.Song.ID)
	require.NoError(t, db.Model(&target).Update("lifecycle_status", model.MusicLifecycleRetired).Error)
	_, err = resolveMusicSongID(db, source.ID, nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestMusicPlayEventHTTPPreservesLegacyRequests(t *testing.T) {
	svc, db, user := newMusicHTTPTestService(t)
	require.NoError(t, db.AutoMigrate(&model.MusicPlayEvent{}))
	song := model.Song{Title: "HTTP event", AudioURL: "/http.mp3", AudioStatus: "ready"}
	require.NoError(t, db.Create(&song).Error)
	router := newMusicHTTPRouter(svc, &user)
	body := fmt.Sprintf(`{"song_id":%q,"play_event_id":%q}`, song.ID.String(), uuid.NewString())
	for range 2 {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/music/plays", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}
	require.NoError(t, db.First(&song, "id = ?", song.ID).Error)
	require.Equal(t, int64(1), song.PlayCount)
}

func TestMusicArtistMergeIgnoresLegacyClosedFlagOnActiveEntries(t *testing.T) {
	svc, db, user := newMusicHTTPTestService(t)
	source := model.Artist{Name: "source", EntryStatus: "closed", LifecycleStatus: model.MusicLifecycleActive}
	target := model.Artist{Name: "target", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&target).Error)
	user.Role = authctx.RoleAdmin
	require.NoError(t, svc.MergeArtists(user, source.ID, target.ID))
}

func TestMusicStateMatrixUsesLifecycleAndEditStatus(t *testing.T) {
	svc, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "matrix", AudioURL: "/matrix.mp3", AudioStatus: "ready", UploadedBy: &user.ID}
	require.NoError(t, db.Create(&song).Error)
	for _, life := range []string{model.MusicLifecycleActive, model.MusicLifecycleDraft, model.MusicLifecycleRetired, model.MusicLifecycleMerged} {
		for _, edit := range []string{model.MusicEditDevelopment, model.MusicEditLocked, model.MusicEditClosed} {
			for _, legacy := range []string{"open", "closed"} {
				t.Run(fmt.Sprintf("%s/%s/%s", life, edit, legacy), func(t *testing.T) {
					require.NoError(t, db.Model(&song).Updates(map[string]any{"lifecycle_status": life, "edit_status": edit, "status": legacy}).Error)
					visible := life == model.MusicLifecycleActive || life == model.MusicLifecycleDraft
					require.Equal(t, visible, svc.RecordSongPlayForViewer(&user, song.ID) == nil)
					require.Equal(t, life == model.MusicLifecycleActive, svc.RecordSongPlayForViewer(nil, song.ID) == nil)
					err := db.Transaction(func(tx *gorm.DB) error { return revisionservice.ValidateMusicEntryEdit(tx, "song", song.ID, user.ID) })
					require.Equal(t, visible && edit == model.MusicEditDevelopment, err == nil)
				})
			}
		}
	}
}

func TestMusicMatchAuthoritySurvivesAutomaticFailure(t *testing.T) {
	_, db, _ := newMusicHTTPTestService(t)
	id := uuid.New()
	require.NoError(t, upsertMusicMatchRecord(db, "album", id, "discogs", "123", "/confirmed", model.MusicMatchMatched, 1, true, nil))
	require.NoError(t, upsertMusicMatchRecord(db, "album", id, "discogs", "", "", model.MusicMatchUnmatched, 0, false, nil))
	var record model.MusicMatchRecord
	require.NoError(t, db.Where("entity_id = ?", id).First(&record).Error)
	require.Equal(t, "123", record.ExternalID)
	require.True(t, record.UserOverridden)
	newer := model.MusicMatchRecord{Status: model.MusicMatchUnmatched, Base: model.Base{UpdatedAt: time.Now().Add(time.Hour)}}
	matched := model.MusicMatchRecord{Status: model.MusicMatchMatched}
	chosen := chooseMusicMatchRecord([]model.MusicMatchRecord{newer, matched})
	require.Equal(t, model.MusicMatchMatched, chosen.Status)
	chosen = chooseMusicMatchRecord([]model.MusicMatchRecord{newer, matched, record})
	require.True(t, chosen.UserOverridden)
}

func TestMusicBackfillDoesNotReplaceManualMetadata(t *testing.T) {
	album := model.Album{Title: "人工确认的标题", MetadataManualOverride: true}
	// 保护条件应在探测音频、查询外部目录和写数据库之前生效。
	result := backfillCatalogAlbum(context.Background(), nil, nil, &album, true)
	require.False(t, result.Applied)
	require.Equal(t, "manual metadata override is protected", result.Reason)
	require.Equal(t, "人工确认的标题", album.Title)
}
