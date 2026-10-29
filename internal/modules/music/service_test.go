package music

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	dsn := testdb.TestPostgresDSN()
	var cleanup func() error
	if dsn != "" {
		name, remove, err := testdb.CreatePostgresTemplate(dsn, "music_template", musicTestModels()...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "music tests: PostgreSQL template unavailable, using schema isolation: %v\n", err)
		} else if err := os.Setenv("TEST_POSTGRES_TEMPLATE_DATABASE", name); err != nil {
			_ = remove()
			fmt.Fprintf(os.Stderr, "music tests: cannot enable PostgreSQL template, using schema isolation: %v\n", err)
		} else {
			cleanup = remove
		}
	}
	code := m.Run()
	if cleanup != nil {
		_ = os.Unsetenv("TEST_POSTGRES_TEMPLATE_DATABASE")
		if err := cleanup(); err != nil {
			code = 1
		}
	}
	os.Exit(code)
}

func musicTestModels() []any {
	return append([]any{&model.MusicTagRevision{}}, musicTestBaseModels()...)
}

func musicTestBaseModels() []any {
	return []any{&model.User{}, &model.Artist{}, &model.ArtistMember{}, &model.ArtistAlias{}, &model.ArtistMerge{}, &model.Album{}, &model.AlbumArtist{}, &model.AlbumRating{}, &model.Song{}, &model.SongRating{}, &model.SongArtist{}, &model.ArtistBookmark{}, &model.AlbumBookmark{}, &model.PlaylistBookmark{}, &model.Playlist{}, &model.PlaylistSong{}, &model.MusicListeningHistory{}, &model.MusicPlaybackSession{}, &model.MusicPlaybackProgress{}, &model.MusicSearchInteraction{}, &model.MusicRecommendationEvent{}, &model.MusicCatalogLink{}, &model.MusicMatchRecord{}, &model.AlbumImportSession{}, &model.MusicAssetUploadSession{}, &model.MediaAsset{}, &model.AlbumImportFile{}, &model.AlbumImportJob{}, &model.MusicEdit{}, &model.MusicEditVote{}, &model.MusicEditDecision{}, &model.MusicEditChange{}, &model.MusicSongLyric{}, &model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{}, &model.MusicLyricAnnotation{}, &model.MusicLyricAnnotationVote{}, &model.MusicTag{}, &model.MusicTagAssignment{}, &model.MusicTagVote{}, &model.AuditLog{}, &model.Notification{}, &model.Revision{}, &model.EditConflict{}, &model.MusicEntryStateEvent{}, &model.MusicEntryStateRequest{}, &model.SongAudioReplacement{}}

}

func newMusicTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()

	db := testdb.OpenPostgres(t, "music_service")
	testdb.Migrate(t, db,
		&model.User{},
		&model.Artist{},
		&model.ArtistMember{},
		&model.ArtistAlias{},
		&model.ArtistMerge{},
		&model.Album{},
		&model.AlbumRating{},
		&model.AlbumArtist{},
		&model.Song{},
		&model.SongArtist{},
		&model.ArtistBookmark{},
		&model.AlbumBookmark{},
		&model.PlaylistBookmark{},
		&model.Playlist{},
		&model.PlaylistSong{},
		&model.MusicListeningHistory{},
		&model.MusicPlaybackSession{},
		&model.MusicPlaybackProgress{},
		&model.MusicRecommendationEvent{},
		&model.MusicSearchInteraction{},
		&model.AlbumImportSession{},
		&model.MusicAssetUploadSession{},
		&model.MediaAsset{},
		&model.AlbumImportFile{},
		&model.AlbumImportJob{},
		&model.MusicEdit{},
		&model.MusicEditVote{},
		&model.MusicEditDecision{},
		&model.MusicEditChange{},
		&model.MusicSongLyric{},
		&model.MusicSongLyricLine{},
		&model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{},
		&model.MusicLyricAnnotationVote{},
		&model.AuditLog{},
		&model.Notification{},
		&model.Revision{},
		&model.MusicTagRevision{},
	)

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
}

func seedReadyImportMedia(t *testing.T, db *gorm.DB, sessionID uuid.UUID, coverURL string, trackTitles ...string) {
	t.Helper()
	payload := map[string]any{}
	var session model.AlbumImportSession
	if err := db.First(&session, "id = ?", sessionID).Error; err != nil {
		t.Fatalf("load import session: %v", err)
	}
	if session.PayloadJSON != "" {
		_ = json.Unmarshal([]byte(session.PayloadJSON), &payload)
	}
	payload["derived_cover"] = coverURL
	tracks := make([]map[string]any, 0, len(trackTitles))
	for index, title := range trackTitles {
		tracks = append(tracks, map[string]any{
			"title": title, "track_number": index + 1,
			"audio_key": fmt.Sprintf("audio-%d", index+1),
			"audio_url": fmt.Sprintf("https://cdn.test/%s/%d.mp3", sessionID, index+1),
		})
	}
	payload["derived_tracks"] = tracks
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode import payload: %v", err)
	}
	if err := db.Model(&session).Update("payload_json", string(encoded)).Error; err != nil {
		t.Fatalf("save import payload: %v", err)
	}
}

func TestCreatePlaylistAcceptsOwnedCoverAndRejectsAnotherUsersUpload(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.test/assets")
	t.Setenv("S3_BUCKET", "atoman-test")
	svc, db, user := newMusicTestService(t)
	other := model.User{Username: "cover-owner-" + uuid.NewString()[:8], Email: uuid.NewString() + "@test.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	makeCover := func(ownerID uuid.UUID) string {
		key := "music/covers/uploads/users/" + ownerID.String() + "/2026/10/cover.webp"
		url := "https://cdn.example.test/assets/" + key
		asset := model.MediaAsset{UserID: &ownerID, Purpose: "music.cover", URL: url, Key: key, ContentType: "image/webp", Size: 32}
		if err := db.Create(&asset).Error; err != nil {
			t.Fatalf("create cover asset: %v", err)
		}
		return url
	}
	ownedURL := makeCover(user.ID)
	playlist, err := svc.CreatePlaylist(user, CreatePlaylistRequest{Name: "Owned cover", CoverURL: ownedURL})
	if err != nil || playlist.CoverURL != ownedURL {
		t.Fatalf("expected own cover upload to be accepted, playlist=%+v err=%v", playlist, err)
	}

	_, err = svc.CreatePlaylist(user, CreatePlaylistRequest{Name: "Other cover", CoverURL: makeCover(other.UUID)})
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code != "music.upload_asset_forbidden" {
		t.Fatalf("expected another user's cover to be forbidden, got %v", err)
	}
	var count int64
	if err := db.Model(&model.Playlist{}).Where("user_id = ? AND name = ?", user.ID, "Other cover").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("expected unauthorized playlist not to be created, count=%d err=%v", count, err)
	}
}

func TestUpdateSystemPlaylistRejectsVisibilityBeforePromotingCover(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.test/assets")
	t.Setenv("S3_BUCKET", "atoman-test")
	svc, db, user := newMusicTestService(t)
	other := model.User{Username: "playlist-cover-owner-" + uuid.NewString()[:8], Email: uuid.NewString() + "@test.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	key := "music/covers/uploads/users/" + other.UUID.String() + "/2026/10/cover.webp"
	coverURL := "https://cdn.example.test/assets/" + key
	if err := db.Create(&model.MediaAsset{UserID: &other.UUID, Purpose: "music.cover", URL: coverURL, Key: key, ContentType: "image/webp", Size: 32}).Error; err != nil {
		t.Fatalf("create other user's cover asset: %v", err)
	}
	playlist, err := svc.repo.EnsureFavoritePlaylist(user.ID)
	if err != nil {
		t.Fatalf("create favorite playlist: %v", err)
	}
	makePublic := true
	_, err = svc.UpdatePlaylist(user, playlist.ID, UpdatePlaylistRequest{CoverURL: &coverURL, IsPublic: &makePublic})
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code != "music.system_playlist_protected" {
		t.Fatalf("expected system playlist visibility conflict before cover validation, got %v", err)
	}
}

func completeAlbumImportArtistPayload(name string) AlbumImportArtistPayload {
	return AlbumImportArtistPayload{
		Name: name, LegalName: name, ImageURL: "/artist.jpg",
		Nationality: "CN", BirthDate: "1990-01-01", ArtistForm: "person",
	}
}

func TestPlaylistBookmarksRequireExistingPlaylistAndStayUserScoped(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	owner := model.User{Username: "playlist-owner", Email: "playlist-owner@example.com", Password: "hash", Role: "user", IsActive: true}
	other := model.User{Username: "playlist-other", Email: "playlist-other@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	playlist := model.Playlist{UserID: owner.UUID, Name: "Shared", IsPublic: true}
	if err := db.Create(&playlist).Error; err != nil {
		t.Fatalf("create playlist: %v", err)
	}

	first, err := svc.BookmarkPlaylist(user, playlist.ID)
	if err != nil {
		t.Fatalf("bookmark playlist: %v", err)
	}
	second, err := svc.BookmarkPlaylist(user, playlist.ID)
	if err != nil {
		t.Fatalf("bookmark playlist twice: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected idempotent bookmark, got %s and %s", first.ID, second.ID)
	}

	otherUser := authctx.CurrentUser{ID: other.UUID, Username: other.Username, Role: authctx.RoleUser}
	if _, err := svc.BookmarkPlaylist(otherUser, playlist.ID); err != nil {
		t.Fatalf("bookmark playlist as other user: %v", err)
	}
	bookmarks, total, err := svc.ListPlaylistBookmarks(user, 1, 20, "latest")
	if err != nil {
		t.Fatalf("list playlist bookmarks: %v", err)
	}
	if total != 1 || len(bookmarks) != 1 || bookmarks[0].UserID != user.ID {
		t.Fatalf("expected only current user's bookmark, got total=%d rows=%#v", total, bookmarks)
	}
	if bookmarks[0].Playlist == nil || bookmarks[0].Playlist.User == nil || bookmarks[0].Playlist.User.Username != owner.Username {
		t.Fatalf("expected playlist and owner to be preloaded, got %#v", bookmarks[0].Playlist)
	}

	if err := svc.DeletePlaylistBookmark(user, playlist.ID); err != nil {
		t.Fatalf("delete playlist bookmark: %v", err)
	}
	otherBookmarks, otherTotal, err := svc.ListPlaylistBookmarks(otherUser, 1, 20, "latest")
	if err != nil {
		t.Fatalf("list other user's playlist bookmarks: %v", err)
	}
	if otherTotal != 1 || len(otherBookmarks) != 1 {
		t.Fatalf("deleting current user's bookmark affected another user: total=%d rows=%#v", otherTotal, otherBookmarks)
	}

	if _, err := svc.BookmarkPlaylist(user, uuid.New()); err == nil {
		t.Fatal("expected nonexistent playlist bookmark to fail")
	}
}

func TestPlaylistBookmarksRespectPlaylistVisibility(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	ownerModel := model.User{Username: "visibility-owner", Email: "visibility-owner@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&ownerModel).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	owner := authctx.CurrentUser{ID: ownerModel.UUID, Username: ownerModel.Username, Role: authctx.RoleUser}
	privatePlaylist := model.Playlist{UserID: owner.ID, Name: "Private"}
	if err := db.Create(&privatePlaylist).Error; err != nil {
		t.Fatalf("create private playlist: %v", err)
	}

	if _, err := svc.BookmarkPlaylist(user, privatePlaylist.ID); err == nil {
		t.Fatal("expected another user's private playlist to be hidden")
	}
	if _, err := svc.BookmarkPlaylist(owner, privatePlaylist.ID); err != nil {
		t.Fatalf("owner should bookmark own private playlist: %v", err)
	}

	publicPlaylist := model.Playlist{UserID: owner.ID, Name: "Public", IsPublic: true}
	if err := db.Create(&publicPlaylist).Error; err != nil {
		t.Fatalf("create public playlist: %v", err)
	}
	if _, err := svc.BookmarkPlaylist(user, publicPlaylist.ID); err != nil {
		t.Fatalf("bookmark public playlist: %v", err)
	}
	if err := db.Model(&publicPlaylist).Update("is_public", false).Error; err != nil {
		t.Fatalf("make playlist private: %v", err)
	}

	orphanedPlaylist := model.Playlist{UserID: owner.ID, Name: "Orphaned", IsPublic: true}
	if err := db.Create(&orphanedPlaylist).Error; err != nil {
		t.Fatalf("create orphan target: %v", err)
	}
	if _, err := svc.BookmarkPlaylist(user, orphanedPlaylist.ID); err != nil {
		t.Fatalf("bookmark orphan target: %v", err)
	}
	if err := db.Delete(&orphanedPlaylist).Error; err != nil {
		t.Fatalf("delete orphan target: %v", err)
	}

	bookmarks, total, err := svc.ListPlaylistBookmarks(user, 1, 20, "latest")
	if err != nil {
		t.Fatalf("list visible bookmarks: %v", err)
	}
	if total != 0 || len(bookmarks) != 0 {
		t.Fatalf("expected private and orphaned targets to be excluded, total=%d rows=%#v", total, bookmarks)
	}
}

func TestDeletePlaylistRemovesAllBookmarks(t *testing.T) {
	svc, db, owner := newMusicTestService(t)
	otherModel := model.User{Username: "delete-bookmark-other", Email: "delete-bookmark-other@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&otherModel).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	other := authctx.CurrentUser{ID: otherModel.UUID, Username: otherModel.Username, Role: authctx.RoleUser}
	playlist := model.Playlist{UserID: owner.ID, Name: "Delete With Bookmarks", IsPublic: true}
	if err := db.Create(&playlist).Error; err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	if _, err := svc.BookmarkPlaylist(owner, playlist.ID); err != nil {
		t.Fatalf("owner bookmark: %v", err)
	}
	if _, err := svc.BookmarkPlaylist(other, playlist.ID); err != nil {
		t.Fatalf("other bookmark: %v", err)
	}

	if err := svc.DeletePlaylist(owner, playlist.ID); err != nil {
		t.Fatalf("delete playlist: %v", err)
	}
	var bookmarkCount int64
	if err := db.Unscoped().Model(&model.PlaylistBookmark{}).Where("playlist_id = ?", playlist.ID).Count(&bookmarkCount).Error; err != nil {
		t.Fatalf("count deleted playlist bookmarks: %v", err)
	}
	if bookmarkCount != 0 {
		t.Fatalf("expected playlist bookmarks to be hard deleted, got %d", bookmarkCount)
	}
	bookmarks, total, err := svc.ListPlaylistBookmarks(other, 1, 20, "latest")
	if err != nil {
		t.Fatalf("list bookmarks after playlist delete: %v", err)
	}
	if total != 0 || len(bookmarks) != 0 {
		t.Fatalf("expected empty bookmark list after playlist delete, total=%d rows=%#v", total, bookmarks)
	}
}

func TestPlaylistBookmarkPopularSortIgnoresSoftDeletedSongs(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	inflated := model.Playlist{UserID: user.ID, Name: "Inflated", IsPublic: true}
	live := model.Playlist{UserID: user.ID, Name: "Live", IsPublic: true}
	if err := db.Create(&inflated).Error; err != nil {
		t.Fatalf("create inflated playlist: %v", err)
	}
	if err := db.Create(&live).Error; err != nil {
		t.Fatalf("create live playlist: %v", err)
	}
	for index := 0; index < 2; index++ {
		song := model.Song{Title: "Deleted Song " + string(rune('A'+index)), AudioURL: "/deleted.mp3", Status: "open"}
		if err := db.Create(&song).Error; err != nil {
			t.Fatalf("create deleted song: %v", err)
		}
		row := model.PlaylistSong{PlaylistID: inflated.ID, SongID: song.ID}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("add deleted song: %v", err)
		}
		if err := db.Delete(&row).Error; err != nil {
			t.Fatalf("soft delete playlist song: %v", err)
		}
	}
	liveSong := model.Song{Title: "Live Song", AudioURL: "/live.mp3", Status: "open"}
	if err := db.Create(&liveSong).Error; err != nil {
		t.Fatalf("create live song: %v", err)
	}
	if err := db.Create(&model.PlaylistSong{PlaylistID: live.ID, SongID: liveSong.ID}).Error; err != nil {
		t.Fatalf("add live song: %v", err)
	}
	if _, err := svc.BookmarkPlaylist(user, inflated.ID); err != nil {
		t.Fatalf("bookmark inflated playlist: %v", err)
	}
	if _, err := svc.BookmarkPlaylist(user, live.ID); err != nil {
		t.Fatalf("bookmark live playlist: %v", err)
	}

	bookmarks, _, err := svc.ListPlaylistBookmarks(user, 1, 20, "popular")
	if err != nil {
		t.Fatalf("list popular bookmarks: %v", err)
	}
	if len(bookmarks) != 2 || bookmarks[0].PlaylistID != live.ID {
		t.Fatalf("expected live playlist first, got %#v", bookmarks)
	}
}

func TestPlaylistCoverAndPopularSortIgnoreRemovedSong(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	old := model.Playlist{UserID: user.ID, Name: "Old", Kind: "user"}
	current := model.Playlist{UserID: user.ID, Name: "Current", Kind: "user"}
	for _, playlist := range []*model.Playlist{&old, &current} {
		if err := db.Create(playlist).Error; err != nil {
			t.Fatal(err)
		}
	}
	staleSong := model.Song{Title: "Removed", AudioURL: "/removed.mp3", CoverURL: "/removed.jpg", Status: "open", LifecycleStatus: model.MusicLifecycleActive}
	liveSong := model.Song{Title: "Active", AudioURL: "/active.mp3", CoverURL: "/active.jpg", Status: "open", LifecycleStatus: model.MusicLifecycleActive}
	for _, song := range []*model.Song{&staleSong, &liveSong} {
		if err := db.Create(song).Error; err != nil {
			t.Fatal(err)
		}
	}
	removed := model.PlaylistSong{PlaylistID: old.ID, SongID: staleSong.ID}
	kept := model.PlaylistSong{PlaylistID: current.ID, SongID: liveSong.ID}
	for _, item := range []*model.PlaylistSong{&removed, &kept} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Delete(&removed).Error; err != nil {
		t.Fatal(err)
	}
	covers, err := svc.repo.PlaylistFirstSongCovers([]uuid.UUID{old.ID, current.ID})
	if err != nil {
		t.Fatal(err)
	}
	if covers[old.ID] != "" || covers[current.ID] != liveSong.CoverURL {
		t.Fatalf("unexpected covers: %#v", covers)
	}
	playlists, _, err := svc.repo.ListPlaylists(user.ID, 1, 20, "popular")
	if err != nil {
		t.Fatal(err)
	}
	if len(playlists) != 2 || playlists[0].ID != current.ID {
		t.Fatalf("unexpected popular order: %#v", playlists)
	}
}

func createModerator(t *testing.T, db *gorm.DB) authctx.CurrentUser {
	t.Helper()
	moderatorModel := model.User{Username: "mod", Email: "mod@example.com", Password: "hash", Role: authctx.RoleModerator, IsActive: true}
	if err := db.Create(&moderatorModel).Error; err != nil {
		t.Fatalf("create moderator: %v", err)
	}
	return authctx.CurrentUser{ID: moderatorModel.UUID, Username: moderatorModel.Username, Role: authctx.RoleModerator}
}

func TestDeletePlaylistSoftDeletesPlaylistSongs(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	playlist := model.Playlist{UserID: user.ID, Name: "Disposable"}
	if err := db.Create(&playlist).Error; err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	song := model.Song{Title: "Disposable Song", AudioURL: "/audio/disposable.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	playlistSong := model.PlaylistSong{PlaylistID: playlist.ID, SongID: song.ID}
	if err := db.Create(&playlistSong).Error; err != nil {
		t.Fatalf("create playlist song: %v", err)
	}

	if err := svc.DeletePlaylist(user, playlist.ID); err != nil {
		t.Fatalf("delete playlist: %v", err)
	}

	var remaining int64
	if err := db.Model(&model.PlaylistSong{}).Where("playlist_id = ?", playlist.ID).Count(&remaining).Error; err != nil {
		t.Fatalf("count playlist songs: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected playlist songs to be soft deleted, got %d", remaining)
	}
}

func TestUpsertPlaylistSongRevalidatesLifecycleInsideTransaction(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	playlist := model.Playlist{UserID: user.ID, Name: "Lifecycle Recheck"}
	song := model.Song{Title: "Lifecycle Recheck", AudioURL: "/audio/recheck.mp3", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&playlist).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Song{}).Where("id = ?", song.ID).Update("lifecycle_status", model.MusicLifecycleMerged).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.repo.UpsertPlaylistSong(playlist.ID, song.ID); err == nil {
		t.Fatal("expected inactive song to be rejected")
	}
	var count int64
	if err := db.Model(&model.PlaylistSong{}).Where("playlist_id = ? AND song_id = ?", playlist.ID, song.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected no playlist relation, got %d", count)
	}
}

func TestUpsertPlaylistSongLocksPlaylistBeforeAssigningPosition(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	playlist := model.Playlist{UserID: user.ID, Name: "Serialized Positions"}
	if err := db.Create(&playlist).Error; err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	song := model.Song{Title: "Concurrent Add", AudioURL: "/audio/concurrent.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}

	lockedPlaylist := false
	const callbackName = "test:observe_playlist_position_lock"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "music_playlists" {
			return
		}
		_, lockedPlaylist = tx.Statement.Clauses["FOR"]
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	if _, err := svc.repo.UpsertPlaylistSong(playlist.ID, song.ID); err != nil {
		t.Fatalf("upsert playlist song: %v", err)
	}
	if !lockedPlaylist {
		t.Fatal("expected playlist row to be locked before assigning the next position")
	}
}

func TestPlaylistNewSongAppearsFirstAndKeepsManualOrder(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	playlist := model.Playlist{UserID: user.ID, Name: "Newest first"}
	if err := db.Create(&playlist).Error; err != nil {
		t.Fatal(err)
	}
	ids := make([]uuid.UUID, 3)
	for i := range ids {
		song := model.Song{Title: fmt.Sprintf("Song %d", i), AudioURL: fmt.Sprintf("/audio/%d.mp3", i), Status: "open"}
		if err := db.Create(&song).Error; err != nil {
			t.Fatal(err)
		}
		ids[i] = song.ID
	}
	for _, id := range ids[:2] {
		if _, err := svc.repo.UpsertPlaylistSong(playlist.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.repo.ReorderPlaylistSongs(playlist.ID, []uuid.UUID{ids[0], ids[1]}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.repo.UpsertPlaylistSong(playlist.ID, ids[2]); err != nil {
		t.Fatal(err)
	}
	for page, expected := range []uuid.UUID{ids[2], ids[0], ids[1]} {
		items, total, err := svc.repo.ListPlaylistSongs(playlist.ID, page+1, 1, &user)
		if err != nil {
			t.Fatal(err)
		}
		if total != 3 || len(items) != 1 || items[0].SongID != expected {
			t.Fatalf("page %d: total=%d items=%+v", page+1, total, items)
		}
	}
}

func TestRecommendAlbumsLatestUsesSubsecondCreatedAtBeforeStableID(t *testing.T) {
	svc, db, _ := newMusicTestService(t)
	second := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	newerID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	olderID := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	albums := []model.Album{
		{Base: model.Base{ID: newerID, CreatedAt: second.Add(900 * time.Millisecond)}, Title: "Newer", EntryStatus: "open", Status: "open"},
		{Base: model.Base{ID: olderID, CreatedAt: second.Add(100 * time.Millisecond)}, Title: "Older", EntryStatus: "open", Status: "open"},
	}
	if err := db.Create(&albums).Error; err != nil {
		t.Fatalf("create albums: %v", err)
	}

	items, _, err := svc.RecommendAlbumsByMode(recommendation.ModeLatest, 1, 20)
	if err != nil {
		t.Fatalf("recommend latest albums: %v", err)
	}
	if len(items) != 2 || items[0].ID != newerID.String() {
		t.Fatalf("expected subsecond-newer album first, got %#v", items)
	}
}

func TestApproveEditOnlyAllowsOneDecision(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	moderator := createModerator(t, db)
	edit := model.MusicEdit{
		Type:        "create_artist",
		EntityType:  "artist",
		SubmittedBy: user.ID,
		Status:      "open",
		Reason:      "seed edit",
		PayloadJSON: `{"name":"Approve Once Artist"}`,
		ChangesJSON: "{}",
		SourcesJSON: "[]",
		Votable:     true,
	}
	if err := db.Create(&edit).Error; err != nil {
		t.Fatalf("create edit: %v", err)
	}

	first, err := svc.ApproveEdit(moderator, edit.ID, "approve once")
	if err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if first.Status != "applied" {
		t.Fatalf("expected applied edit, got %#v", first)
	}

	_, err = svc.ApproveEdit(moderator, edit.ID, "approve twice")
	if !isEditNotOpenError(err) {
		t.Fatalf("expected edit_not_open on second approve, got %v", err)
	}

	var decisions int64
	if err := db.Model(&model.MusicEditDecision{}).Where("edit_id = ?", edit.ID).Count(&decisions).Error; err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	if decisions != 1 {
		t.Fatalf("expected one decision, got %d", decisions)
	}
}

func TestApproveThenRejectEditOnlyAllowsOneDecisionAndOneApply(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	moderator := createModerator(t, db)
	edit := model.MusicEdit{
		Type:        "create_artist",
		EntityType:  "artist",
		SubmittedBy: user.ID,
		Status:      "open",
		Reason:      "seed artist",
		PayloadJSON: `{"name":"One Shot Artist"}`,
		ChangesJSON: "{}",
		SourcesJSON: "[]",
		Votable:     true,
	}
	if err := db.Create(&edit).Error; err != nil {
		t.Fatalf("create edit: %v", err)
	}

	if _, err := svc.ApproveEdit(moderator, edit.ID, "approve"); err != nil {
		t.Fatalf("approve edit: %v", err)
	}
	_, err := svc.RejectEdit(moderator, edit.ID, "reject too late")
	if !isEditNotOpenError(err) {
		t.Fatalf("expected edit_not_open on reject after approve, got %v", err)
	}

	var artists int64
	if err := db.Model(&model.Artist{}).Where("name = ?", "One Shot Artist").Count(&artists).Error; err != nil {
		t.Fatalf("count artists: %v", err)
	}
	if artists != 1 {
		t.Fatalf("expected one applied artist, got %d", artists)
	}

	var decisions int64
	if err := db.Model(&model.MusicEditDecision{}).Where("edit_id = ?", edit.ID).Count(&decisions).Error; err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	if decisions != 1 {
		t.Fatalf("expected one decision, got %d", decisions)
	}
}

func isEditNotOpenError(err error) bool {
	var appErr *apperr.AppError
	return errors.As(err, &appErr) && appErr.Code == "music.edit_not_open"
}

func TestConcurrentApproveEditOnlyAppliesOnce(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	moderator := createModerator(t, db)
	edit := model.MusicEdit{
		Type:        "create_artist",
		EntityType:  "artist",
		SubmittedBy: user.ID,
		Status:      "open",
		Reason:      "seed artist",
		PayloadJSON: `{"name":"Concurrent Artist"}`,
		ChangesJSON: "{}",
		SourcesJSON: "[]",
		Votable:     true,
	}
	if err := db.Create(&edit).Error; err != nil {
		t.Fatalf("create edit: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.ApproveEdit(moderator, edit.ID, "approve")
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !isEditNotOpenError(err) {
			t.Fatalf("expected losing approval to return edit_not_open, got %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one successful approval, got %d", successes)
	}

	var artists int64
	if err := db.Model(&model.Artist{}).Where("name = ?", "Concurrent Artist").Count(&artists).Error; err != nil {
		t.Fatalf("count artists: %v", err)
	}
	if artists != 1 {
		t.Fatalf("expected one applied artist, got %d", artists)
	}

	var decisions int64
	if err := db.Model(&model.MusicEditDecision{}).Where("edit_id = ?", edit.ID).Count(&decisions).Error; err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	if decisions != 1 {
		t.Fatalf("expected one decision, got %d", decisions)
	}
}

func TestSubmitEditRejectsLegacyRevisionAndAlbumMergePaths(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	for _, editType := range []string{"update_artist", "update_album", "merge_album"} {
		entityID := uuid.New()
		_, err := svc.SubmitEdit(user, SubmitEditRequest{
			Type: editType, EntityType: "album", EntityID: &entityID,
			Changes: map[string]any{"name": "Changed"}, Reason: "legacy update",
		})
		if err == nil {
			t.Fatalf("expected %s to require revisions", editType)
		}
	}
}

func TestSubmitEditAutoAppliesCreateArtistForMainWikiFlow(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{
		Type:       "create_artist",
		EntityType: "artist",
		Payload: map[string]any{
			"name":       "Instant Artist",
			"bio":        "created immediately",
			"legal_name": "Instant Legal Name",
			"stage_names": []map[string]any{
				{"name": "Instant Artist", "is_primary": true, "start_date_text": "2020"},
				{"name": "IA", "is_primary": false, "end_date_text": "2021"},
			},
			"birth_place": "Shanghai",
		},
		Reason: "new artist",
	})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	if edit.Status != "applied" || !edit.AutoApplied {
		t.Fatalf("expected auto-applied create artist edit, got %#v", edit)
	}

	var artist model.Artist
	if err := db.Where("name = ?", "Instant Artist").First(&artist).Error; err != nil {
		t.Fatalf("expected artist persisted immediately: %v", err)
	}
	var stageNames []ArtistStageNamePayload
	if err := json.Unmarshal([]byte(artist.StageNamesJSON), &stageNames); err != nil {
		t.Fatalf("unmarshal stage names json: %v", err)
	}
	if artist.LegalName != "Instant Legal Name" || artist.BirthPlace != "Shanghai" {
		t.Fatalf("expected extended artist fields, got %#v", artist)
	}
	if len(stageNames) != 2 || !stageNames[0].IsPrimary || stageNames[0].Name != "Instant Artist" || stageNames[1].Name != "IA" || stageNames[1].EndDateText != "2021" {
		t.Fatalf("expected structured stage names, got %#v", stageNames)
	}
}

func TestSubmitEditAutoAppliesCreateGroupArtistForMainWikiFlow(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	memberA := model.Artist{Name: "Member A", EntryStatus: "open"}
	memberB := model.Artist{Name: "Member B", EntryStatus: "open"}
	if err := db.Create(&memberA).Error; err != nil {
		t.Fatalf("create member A: %v", err)
	}
	if err := db.Create(&memberB).Error; err != nil {
		t.Fatalf("create member B: %v", err)
	}

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{
		Type:       "create_artist",
		EntityType: "artist",
		Payload: map[string]any{
			"name":              "Test Group",
			"artist_form":       "group",
			"active_start_date": "2020-01-01",
			"active_end_date":   "2024-12-31",
			"members": []map[string]any{
				{
					"artist_id":  memberA.ID.String(),
					"join_date":  "2020-01-01",
					"leave_date": "2022-05-01",
				},
				{
					"artist_id": memberB.ID.String(),
					"join_date": "2020-01-01",
				},
			},
		},
		Reason: "new group artist",
	})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}
	if edit.Status != "applied" || !edit.AutoApplied {
		t.Fatalf("expected auto-applied create group artist edit, got %#v", edit)
	}

	var artist model.Artist
	if err := db.Where("name = ?", "Test Group").First(&artist).Error; err != nil {
		t.Fatalf("load group artist: %v", err)
	}
	if artist.ArtistForm != "group" {
		t.Fatalf("expected group form, got %#v", artist)
	}
	if got := artist.ActiveStartDate.Format("2006-01-02"); got != "2020-01-01" {
		t.Fatalf("expected active_start_date, got %q", got)
	}
	if got := artist.ActiveEndDate.Format("2006-01-02"); got != "2024-12-31" {
		t.Fatalf("expected active_end_date, got %q", got)
	}

	var members []model.ArtistMember
	if err := db.Where("group_artist_id = ?", artist.ID).Order("join_date ASC, created_at ASC").Find(&members).Error; err != nil {
		t.Fatalf("load group members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 group members, got %#v", members)
	}
	if members[0].MemberArtistID != memberA.ID || members[0].LeaveDate == nil || members[1].MemberArtistID != memberB.ID || members[1].LeaveDate != nil {
		t.Fatalf("unexpected member relations: %#v", members)
	}
}

func TestMergeArtistsMovesAlbumRelationsAndAliasesToTarget(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	target := model.Artist{Name: "Ye", LegalName: "Kanye Omari West", EntryStatus: "open"}
	source := model.Artist{Name: "kanye", EntryStatus: "open"}
	album := model.Album{Title: "2049", EntryStatus: "open", Status: "open"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target artist: %v", err)
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source artist: %v", err)
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	group := model.Artist{Name: "Sunday Service", ArtistForm: "group", EntryStatus: "open"}
	member := model.Artist{Name: "Group Member", EntryStatus: "open"}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group artist: %v", err)
	}
	if err := db.Create(&member).Error; err != nil {
		t.Fatalf("create member artist: %v", err)
	}
	for _, relation := range []model.ArtistMember{
		{GroupArtistID: group.ID, MemberArtistID: source.ID},
		{GroupArtistID: group.ID, MemberArtistID: target.ID},
		{GroupArtistID: source.ID, MemberArtistID: member.ID},
	} {
		if err := db.Create(&relation).Error; err != nil {
			t.Fatalf("create artist member relation: %v", err)
		}
	}
	if err := db.Model(&album).Association("Artists").Append(&source); err != nil {
		t.Fatalf("append source artist to album: %v", err)
	}
	if err := db.Create(&model.ArtistAlias{
		ArtistID: source.ID,
		Alias:    "Kanye West",
	}).Error; err != nil {
		t.Fatalf("create source alias: %v", err)
	}

	admin := authctx.CurrentUser{ID: user.ID, Username: user.Username, Role: authctx.RoleAdmin}
	if err := svc.MergeArtists(admin, source.ID, target.ID); err != nil {
		t.Fatalf("merge artists: %v", err)
	}

	var refreshedSource model.Artist
	if err := db.First(&refreshedSource, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("load source artist: %v", err)
	}
	if refreshedSource.EntryStatus != "closed" {
		t.Fatalf("expected source artist closed after merge, got %#v", refreshedSource)
	}

	var refreshedAlbum model.Album
	if err := db.Preload("Artists").First(&refreshedAlbum, "id = ?", album.ID).Error; err != nil {
		t.Fatalf("load album: %v", err)
	}
	if len(refreshedAlbum.Artists) != 1 || refreshedAlbum.Artists[0].ID != target.ID {
		t.Fatalf("expected album linked to target artist only, got %#v", refreshedAlbum.Artists)
	}

	var aliases []model.ArtistAlias
	if err := db.Where("artist_id = ?", target.ID).Find(&aliases).Error; err != nil {
		t.Fatalf("load target aliases: %v", err)
	}
	aliasSet := map[string]bool{}
	for _, alias := range aliases {
		aliasSet[alias.Alias] = true
	}
	if !aliasSet["kanye"] || !aliasSet["Kanye West"] {
		t.Fatalf("expected merged aliases on target artist, got %#v", aliases)
	}

	var memberRelations []model.ArtistMember
	if err := db.Where("group_artist_id = ? OR member_artist_id = ?", target.ID, target.ID).Find(&memberRelations).Error; err != nil {
		t.Fatalf("load merged member relations: %v", err)
	}
	if len(memberRelations) != 2 {
		t.Fatalf("expected deduplicated member relations on target artist, got %#v", memberRelations)
	}

	var mergeRecord model.ArtistMerge
	if err := db.First(&mergeRecord, "source_artist_id = ? AND target_artist_id = ?", source.ID, target.ID).Error; err != nil {
		t.Fatalf("expected merge audit record, got %v", err)
	}
}

func TestMergeArtistsRequiresAdmin(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	target := model.Artist{Name: "Target", EntryStatus: "open"}
	source := model.Artist{Name: "Source", EntryStatus: "open"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target artist: %v", err)
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source artist: %v", err)
	}

	err := svc.MergeArtists(user, source.ID, target.ID)
	if app := apperr.FromError(err); app == nil || app.Code != "music.merge_forbidden" {
		t.Fatalf("expected merge forbidden, got %v", err)
	}
}

func TestMergeAlbumsMovesMatchedSongUserRelationsAndClosesSource(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	target := model.Album{Title: "Target Album", EntryStatus: "open", Status: "open"}
	source := model.Album{Title: "Source Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target album: %v", err)
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source album: %v", err)
	}
	targetSong := model.Song{Title: "Same Song", AlbumID: &target.ID, Status: "open"}
	sourceSong := model.Song{Title: "Same Song", AlbumID: &source.ID, Status: "open"}
	if err := db.Create(&targetSong).Error; err != nil {
		t.Fatalf("create target song: %v", err)
	}
	if err := db.Create(&sourceSong).Error; err != nil {
		t.Fatalf("create source song: %v", err)
	}
	playlist := model.Playlist{UserID: user.ID, Name: "Merge Test", Kind: "user"}
	favorite := model.Playlist{UserID: user.ID, Name: "最爱", Kind: "favorite"}
	fixtures := []any{
		&playlist,
		&favorite,
		&model.AlbumBookmark{UserID: user.ID, AlbumID: source.ID},
		&model.MusicListeningHistory{UserID: user.ID, SongID: targetSong.ID, PlayCount: 2, LastPlayedAt: time.Now().Add(-time.Hour)},
		&model.MusicListeningHistory{UserID: user.ID, SongID: sourceSong.ID, PlayCount: 3, LastPlayedAt: time.Now()},
	}
	for _, fixture := range fixtures {
		if err := db.Create(fixture).Error; err != nil {
			t.Fatalf("create merge fixture: %v", err)
		}
	}
	if err := db.Create(&model.PlaylistSong{PlaylistID: playlist.ID, SongID: sourceSong.ID, Position: 1}).Error; err != nil {
		t.Fatalf("create playlist song: %v", err)
	}
	if err := db.Create(&model.PlaylistSong{PlaylistID: favorite.ID, SongID: sourceSong.ID, Position: 1}).Error; err != nil {
		t.Fatalf("create favorite song: %v", err)
	}

	admin := authctx.CurrentUser{ID: user.ID, Username: user.Username, Role: authctx.RoleAdmin}
	if err := svc.MergeAlbums(admin, target.ID, source.ID, []AlbumMergeSongMatchInput{{SourceSongID: sourceSong.ID, TargetSongID: targetSong.ID}}); err != nil {
		t.Fatalf("merge albums: %v", err)
	}

	var refreshedSource model.Album
	if err := db.First(&refreshedSource, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("load source album: %v", err)
	}
	if refreshedSource.EntryStatus != "closed" || refreshedSource.Status != "closed" || refreshedSource.RedirectTo == nil || *refreshedSource.RedirectTo != target.ID {
		t.Fatalf("expected source album closed, got %#v", refreshedSource)
	}
	if err := db.First(&sourceSong, "id = ?", sourceSong.ID).Error; err != nil {
		t.Fatalf("load source song: %v", err)
	}
	if sourceSong.Status != "closed" {
		t.Fatalf("expected matched source song closed, got %#v", sourceSong)
	}
	for name, value := range map[string]any{
		"album bookmark": &model.AlbumBookmark{},
		"playlist song":  &model.PlaylistSong{},
	} {
		query := db.Where("user_id = ? AND album_id = ?", user.ID, target.ID)
		if name == "playlist song" {
			query = db.Where("playlist_id = ? AND song_id = ?", playlist.ID, targetSong.ID)
		}
		if err := query.First(value).Error; err != nil {
			t.Fatalf("expected migrated %s: %v", name, err)
		}
	}
	var favoriteSong model.PlaylistSong
	if err := db.First(&favoriteSong, "playlist_id = ? AND song_id = ?", favorite.ID, targetSong.ID).Error; err != nil {
		t.Fatalf("expected migrated favorite song: %v", err)
	}
	var history model.MusicListeningHistory
	if err := db.First(&history, "user_id = ? AND song_id = ?", user.ID, targetSong.ID).Error; err != nil {
		t.Fatalf("load merged listening history: %v", err)
	}
	if history.PlayCount != 5 {
		t.Fatalf("expected merged play count 5, got %d", history.PlayCount)
	}
}

func TestSubmitEditAutoAppliesCreateAlbumForMainWikiFlow(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	artistA := model.Artist{Name: "Seed Artist A", EntryStatus: "open"}
	artistB := model.Artist{Name: "Seed Artist B", EntryStatus: "open"}
	if err := db.Create(&artistA).Error; err != nil {
		t.Fatalf("create artist A: %v", err)
	}
	if err := db.Create(&artistB).Error; err != nil {
		t.Fatalf("create artist B: %v", err)
	}

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{
		Type:       "create_album",
		EntityType: "album",
		Payload: map[string]any{
			"title":        "Instant Album",
			"artist_ids":   []string{artistA.ID.String(), artistB.ID.String()},
			"album_type":   "album",
			"release_year": 2024,
			"tracks": []map[string]any{
				{"title": "Intro", "track_number": 1},
			},
		},
		Reason: "new album",
	})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	if edit.Status != "applied" || !edit.AutoApplied {
		t.Fatalf("expected auto-applied create album edit, got %#v", edit)
	}

	var album model.Album
	if err := db.Preload("Artists").Where("title = ?", "Instant Album").First(&album).Error; err != nil {
		t.Fatalf("expected album persisted immediately: %v", err)
	}
	if len(album.Artists) != 2 {
		t.Fatalf("expected linked artists, got %#v", album.Artists)
	}
	if album.ReleaseYear != 2024 {
		t.Fatalf("expected release year persisted, got %#v", album)
	}
}

func TestRecommendAlbumsByModeDiscoverKeepsLowHotScoreAlbums(t *testing.T) {
	svc, db, _ := newMusicTestService(t)

	oldAlbum := model.Album{
		Title:       "Low Heat Old Album",
		EntryStatus: "open",
		Status:      "open",
		HotScore:    1,
	}
	freshAlbum := model.Album{
		Title:       "Low Heat Fresh Album",
		EntryStatus: "open",
		Status:      "open",
		HotScore:    1,
	}

	if err := db.Create(&oldAlbum).Error; err != nil {
		t.Fatalf("create old album: %v", err)
	}
	if err := db.Create(&freshAlbum).Error; err != nil {
		t.Fatalf("create fresh album: %v", err)
	}
	if err := db.Model(&oldAlbum).Update("created_at", "2026-01-01 00:00:00").Error; err != nil {
		t.Fatalf("update old album created_at: %v", err)
	}
	if err := db.Model(&freshAlbum).Update("created_at", "2026-07-01 00:00:00").Error; err != nil {
		t.Fatalf("update fresh album created_at: %v", err)
	}

	items, total, err := svc.RecommendAlbumsByMode("discover", 1, 20)
	if err != nil {
		t.Fatalf("recommend albums: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 discover items, got %d", total)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 discover results, got %d", len(items))
	}
	if items[0].Title != "Low Heat Fresh Album" {
		t.Fatalf("expected fresher low-heat album ranked first, got %#v", items)
	}
}
