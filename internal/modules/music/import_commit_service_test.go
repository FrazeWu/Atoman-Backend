package music

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/google/uuid"
)

func TestFinalizeSubmittedAlbumImportRetainsProcessedLyrics(t *testing.T) {
	for _, albumType := range []string{"album", "single"} {
		for _, manual := range []bool{false, true} {
			t.Run(albumType+"/"+map[bool]string{false: "imported", true: "manual"}[manual], func(t *testing.T) {
				svc, db, user := newMusicTestService(t)
				fileID := uuid.New().String()
				track := AlbumImportTrackPayload{FileID: fileID, Title: "用户改名", TrackNumber: 1}
				want, source := "[00:01.00]本地歌词", "local"
				if manual {
					want, source = "[00:02.00]用户编辑歌词", ""
					track.Lyrics = &AlbumImportTrackLyricsPayload{Content: want, Format: "lrc"}
				}
				request := CommitAlbumImportSessionInput{Artist: completeAlbumImportArtistPayload("Artist"), ArtistSource: "artist source", AlbumSource: "album source", Album: AlbumImportAlbumPayload{Title: "Album", AlbumType: albumType, ReleaseDate: "2020-01-01", CoverURL: "https://example.test/cover.jpg", Tracks: []AlbumImportTrackPayload{track}}}
				payload, err := json.Marshal(map[string]any{"commit_request": request, "derived_tracks": []map[string]any{{"title": "原名", "file_id": fileID, "audio_url": "https://example.test/track.mp3", "lyrics_source": "local", "lyrics": AlbumImportTrackLyricsPayload{Content: "[00:01.00]本地歌词", Format: "lrc"}}}})
				if err != nil {
					t.Fatal(err)
				}
				session := model.AlbumImportSession{UserID: &user.ID, Status: AlbumImportStatusReady, PayloadJSON: string(payload)}
				if err := db.Create(&session).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&model.AlbumImportFile{Base: model.Base{ID: uuid.MustParse(fileID)}, ImportID: session.ID, FileName: "track.mp3", Role: "audio", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusCompleted}).Error; err != nil {
					t.Fatal(err)
				}
				if err := svc.FinalizeSubmittedAlbumImport(session.ID); err != nil {
					t.Fatal(err)
				}
				var song model.Song
				if err := db.First(&song).Error; err != nil {
					t.Fatal(err)
				}
				var lyrics model.MusicSongLyric
				if err := db.First(&lyrics, "song_id = ?", song.ID).Error; err != nil {
					t.Fatal(err)
				}
				if song.Lyrics != want || lyrics.Content != want || lyrics.Source != source {
					t.Fatalf("song=%#v lyrics=%#v", song, lyrics)
				}
			})
		}
	}
}

func TestFindDuplicateImportedAlbumNormalizesTitleAndIgnoresRetiredAlbums(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	artist := model.Artist{Name: "Kendrick Lamar", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatal(err)
	}
	active := model.Album{Title: "  DAMN.  ", LifecycleStatus: model.MusicLifecycleActive}
	retired := model.Album{Title: "DAMN.", LifecycleStatus: model.MusicLifecycleRetired}
	if err := db.Create(&active).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&retired).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AlbumArtist{AlbumID: active.ID, ArtistID: artist.ID, Role: "primary"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AlbumArtist{AlbumID: retired.ID, ArtistID: artist.ID, Role: "primary"}).Error; err != nil {
		t.Fatal(err)
	}
	duplicate, err := findDuplicateImportedAlbum(db, []resolvedCommitAlbumImportArtist{{Artist: &artist}}, "damn.")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate == nil || duplicate.ID != active.ID {
		t.Fatalf("expected active duplicate, got %#v", duplicate)
	}
}

func TestPromoteAlbumImportAssetCopiesLargePlaybackWithoutHTTPDownload(t *testing.T) {
	requests := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = io.WriteString(w, strings.Repeat("a", 11*1024*1024))
	}))
	defer origin.Close()
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.test")
	t.Setenv("MUSIC_PLAYBACK_URL_PREFIX", origin.URL)
	importID := uuid.New()
	source := "music/album-imports/playback/sessions/" + importID.String() + "/files/track.mp3"
	destination := "music/albums/album/tracks/song/version.mp3"
	var sources, destinations, deleted []string
	svc := &Service{s3: fakeMusicPromotionS3Client(t, &sources, &destinations, &deleted)}
	url, oldKey, newKey, err := svc.promoteAlbumImportAsset(origin.URL+"/"+source, destination, importID)
	if err != nil || url != "https://cdn.example.test/"+destination || oldKey != source || newKey != destination {
		t.Fatalf("promotion: url=%q old=%q new=%q error=%v", url, oldKey, newKey, err)
	}
	if requests != 0 || len(sources) != 1 || sources[0] != "atoman-test/"+source || len(destinations) != 1 || destinations[0] != destination {
		t.Fatalf("expected server-side copy, HTTP requests=%d sources=%v destinations=%v", requests, sources, destinations)
	}
}

func TestPromoteAlbumImportAssetRejectsMissingStorageForPlayback(t *testing.T) {
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.test")
	importID := uuid.New()
	url := "https://cdn.example.test/music/album-imports/playback/sessions/" + importID.String() + "/files/track.mp3"
	if _, _, _, err := (&Service{}).promoteAlbumImportAsset(url, "track.mp3", importID); err == nil {
		t.Fatal("temporary playback URL must not be accepted without storage")
	}
}

func TestPromoteAlbumImportAssetRejectsBrowserPreview(t *testing.T) {
	for _, url := range []string{"blob:https://site.test/preview", "data:image/png;base64,preview"} {
		if _, _, _, err := (&Service{}).promoteAlbumImportAsset(url, "cover.webp", uuid.New()); err == nil {
			t.Fatalf("不得保存临时地址 %s", url)
		}
	}
}

func TestCommitAlbumImportSessionRetainsPlaybackWhenCopyFails(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	var sources, destinations, deleted []string
	svc.s3 = fakeMusicPromotionS3Client(t, &sources, &destinations, &deleted)
	svc.s3.Config.MaxRetries = new(int)
	svc.s3.Handlers.Send.Clear()
	svc.s3.Handlers.Send.PushBack(func(r *request.Request) { r.Error = errors.New("copy failed") })
	origin := httptest.NewServer(http.NotFoundHandler())
	defer origin.Close()
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", origin.URL)
	importID := uuid.New()
	key := "music/album-imports/playback/sessions/" + importID.String() + "/files/track.mp3"
	payload, err := json.Marshal(map[string]any{"derived_tracks": []map[string]any{{
		"title": "Track", "track_number": 1, "audio_key": key, "audio_url": origin.URL + "/" + key,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	session := model.AlbumImportSession{Base: model.Base{ID: importID}, UserID: &user.ID, Status: AlbumImportStatusReady, PayloadJSON: string(payload)}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	file := model.AlbumImportFile{ImportID: importID, FileName: "track.mp3", PlaybackKey: key, UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusCompleted}
	if err := db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	_, err = svc.CommitAlbumImportSession(user, importID, CommitAlbumImportSessionInput{
		Artist: completeAlbumImportArtistPayload("Artist"), ArtistSource: "artist source", AlbumSource: "album source",
		Album: AlbumImportAlbumPayload{Title: "Album", ReleaseDate: "2020-01-01", CoverURL: origin.URL + "/cover.jpg"},
	})
	if err == nil {
		t.Fatal("copy failure must abort commit")
	}
	if err := db.First(&session, "id = ?", importID).Error; err != nil {
		t.Fatal(err)
	}
	if session.Status != AlbumImportStatusReady {
		t.Fatalf("session status=%s", session.Status)
	}
	cleaned, err := NewImportWorker(db, NewMusicImportObjectStore(svc.s3), "test-worker").CleanupCommitted(context.Background())
	if err != nil || cleaned {
		t.Fatalf("uncommitted playback was scheduled for cleanup: cleaned=%t err=%v", cleaned, err)
	}
	if err := db.First(&file, "id = ?", file.ID).Error; err != nil || file.PlaybackKey != key {
		t.Fatalf("temporary playback not retained: file=%#v error=%v", file, err)
	}
	var count int64
	if err := db.Model(&model.Song{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("song created after failed copy: count=%d error=%v", count, err)
	}
}

func TestMusicAlbumImportSourceKeyAcceptsPlaybackURLPrefix(t *testing.T) {
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.test")
	t.Setenv("MUSIC_PLAYBACK_URL_PREFIX", "https://playback.example.test")

	key, ok := musicAlbumImportSourceKey("https://playback.example.test/music/album-imports/playback/sessions/import/track.mp3")
	if !ok || key != "music/album-imports/playback/sessions/import/track.mp3" {
		t.Fatalf("source key = %q, ok=%v", key, ok)
	}
}

func TestMatchDerivedTrackAudioUsesAudioKeyAfterTrackRenameAndReorder(t *testing.T) {
	derivedTracks := []any{
		map[string]any{"title": "IGOR'S THEME", "audio_key": "audio-igor", "audio_url": "https://cdn.test/igor.mp3"},
		map[string]any{"title": "EARFQUAKE", "audio_key": "audio-earfquake", "audio_url": "https://cdn.test/earfquake.mp3"},
	}

	matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{
		Title: "EARFQUAKE (用户修改)", TrackNumber: 1, AudioKey: "audio-earfquake",
	}, map[int]bool{})

	if matched.AudioURL != "https://cdn.test/earfquake.mp3" {
		t.Fatalf("expected selected audio to follow audio key, got %#v", matched)
	}
}

func TestMatchDerivedTrackAudioUsesPlaybackKeyWhenAudioURLIsMissing(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.test")

	matched := matchDerivedTrackAudio([]any{
		map[string]any{"title": "Track", "audio_key": "music/imports/track.mp3"},
	}, AlbumImportTrackPayload{
		Title: "Track", AudioKey: "music/imports/track.mp3",
	}, map[int]bool{})

	if matched.AudioURL != "https://cdn.example.test/music/imports/track.mp3" {
		t.Fatalf("expected playback key to resolve to audio URL, got %#v", matched)
	}
}

func TestHydrateDerivedTrackAudioUsesProcessedFileByID(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.test")
	fileID := uuid.New()
	rawTracks := []any{map[string]any{"file_id": fileID.String(), "title": "Track"}}

	hydrateDerivedTrackAudio(rawTracks, map[string]model.AlbumImportFile{
		fileID.String(): {Base: model.Base{ID: fileID}, PlaybackKey: "music/imports/track.mp3"},
	}, nil)
	matched := matchDerivedTrackAudio(rawTracks, AlbumImportTrackPayload{FileID: fileID.String()}, map[int]bool{})

	if matched.AudioURL != "https://cdn.example.test/music/imports/track.mp3" {
		t.Fatalf("expected processed file to restore audio URL, got %#v", matched)
	}
}

func TestMatchDerivedTrackAudioUsesFileIDAfterTrackRename(t *testing.T) {
	derivedTracks := []any{
		map[string]any{
			"file_id": "file-igor", "title": "IGOR'S THEME", "audio_url": "https://cdn.test/igor.mp3",
		},
	}

	matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{
		FileID: "file-igor", Title: "用户改名", TrackNumber: 1,
	}, map[int]bool{})

	if matched.AudioURL != "https://cdn.test/igor.mp3" {
		t.Fatalf("expected selected audio to follow file id, got %#v", matched)
	}
}

func TestMatchDerivedTrackAudioNormalizesTrackTitle(t *testing.T) {
	derivedTracks := []any{
		map[string]any{
			"title": "Alien Girl (Today w/ Her)", "track_number": 4, "disc_number": 1,
			"audio_url": "https://cdn.test/alien-girl.mp3",
		},
	}

	matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{
		Title: "Alien Girl (Today W_ Her)", TrackNumber: 4, DiscNumber: 1,
	}, map[int]bool{})

	if matched.AudioURL != "https://cdn.test/alien-girl.mp3" {
		t.Fatalf("expected normalized title to select audio, got %#v", matched)
	}
}

func TestMatchDerivedTrackAudioDoesNotFallBackToArrayPosition(t *testing.T) {
	derivedTracks := []any{
		map[string]any{"title": "IGOR'S THEME", "audio_key": "audio-igor", "audio_url": "https://cdn.test/igor.mp3"},
		map[string]any{"title": "EARFQUAKE", "audio_key": "audio-earfquake", "audio_url": "https://cdn.test/earfquake.mp3"},
	}

	matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{
		Title: "用户改名", TrackNumber: 1,
	}, map[int]bool{})

	if matched.AudioURL != "" {
		t.Fatalf("expected no audio match without a stable identity, got %#v", matched)
	}
}

func TestMatchDerivedTrackAudioDoesNotUseTitleWhenAudioKeyIsStale(t *testing.T) {
	derivedTracks := []any{
		map[string]any{"title": "Same title", "audio_key": "audio-first", "audio_url": "https://cdn.test/first.mp3"},
		map[string]any{"title": "Same title", "audio_key": "audio-second", "audio_url": "https://cdn.test/second.mp3"},
	}

	matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{
		Title: "Same title", TrackNumber: 1, AudioKey: "audio-missing",
	}, map[int]bool{})

	if matched.AudioURL != "" {
		t.Fatalf("expected stale audio key to prevent title fallback, got %#v", matched)
	}
}

func TestMatchDerivedTrackAudioUsesOnlyRemainingAudioWhenTitleIsUnmatched(t *testing.T) {
	derivedTracks := []any{
		map[string]any{"title": "Known", "audio_url": "https://cdn.test/known.mp3"},
		map[string]any{"title": "Local spelling", "audio_url": "https://cdn.test/remaining.mp3"},
	}
	used := map[int]bool{}

	if matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{Title: "Known"}, used); matched.AudioURL == "" {
		t.Fatal("expected the known track to match")
	}
	matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{Title: "External spelling"}, used)
	if matched.AudioURL != "https://cdn.test/remaining.mp3" {
		t.Fatalf("expected the only remaining processed audio, got %#v", matched)
	}
}

func TestResolveCommitDerivedTrackAudioDoesNotGuessRemainingAudioByPosition(t *testing.T) {
	derived := []any{
		map[string]any{"title": "两代人", "audio_url": "https://cdn.test/1.mp3"},
		map[string]any{"title": "县道184(卷首诗)", "audio_url": "https://cdn.test/2.mp3"},
		map[string]any{"title": "嗷！(器乐曲)", "audio_url": "https://cdn.test/3.mp3"},
		map[string]any{"title": "愁上愁下", "audio_url": "https://cdn.test/4.mp3"},
	}
	tracks := []AlbumImportTrackPayload{
		{Title: "县道184", TrackNumber: 1},
		{Title: "愁上愁下", TrackNumber: 2},
		{Title: "阿芬懁人", TrackNumber: 3},
		{Title: "嗷！(ㄠˋ)", TrackNumber: 4},
	}

	resolved := resolveCommitDerivedTrackAudio(derived, tracks)
	if resolved[2].AudioURL != "" {
		t.Fatalf("expected unmatched title to remain unbound, got %#v", resolved)
	}
}

func TestMatchDerivedTrackAudioUsesUniqueTitleAndPositionWhenIdentityIsStale(t *testing.T) {
	derivedTracks := []any{
		map[string]any{
			"title": "Unique title", "track_number": 2, "disc_number": 1,
			"audio_key": "audio-current", "audio_url": "https://cdn.test/current.mp3",
		},
	}

	matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{
		Title: "Unique title", TrackNumber: 2, DiscNumber: 1, AudioKey: "audio-stale",
	}, map[int]bool{})

	if matched.AudioURL != "https://cdn.test/current.mp3" {
		t.Fatalf("expected unique title and position fallback, got %#v", matched)
	}
}

func TestMatchDerivedTrackAudioUsesOriginalPositionAndTitleBaseAfterMetadataReorder(t *testing.T) {
	derivedTracks := []any{
		map[string]any{
			"title": "县道184(卷首诗)", "original_title": "县道184(卷首诗)",
			"original_track_number": 1, "audio_url": "https://cdn.test/county.mp3",
		},
		map[string]any{
			"title": "嗷！(器乐曲)", "original_title": "嗷！(器乐曲)",
			"original_track_number": 2, "audio_url": "https://cdn.test/ao.mp3",
		},
	}

	used := map[int]bool{}
	matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{
		Title: "嗷！(ㄠˋ)", TrackNumber: 1, OriginalTrack: 2,
	}, used)

	if matched.AudioURL != "https://cdn.test/ao.mp3" {
		t.Fatalf("expected original position to select audio after reorder, got %#v", matched)
	}
}

func TestAlbumImportTracksFromDerivedKeepsStableFieldsAndDeletedKeys(t *testing.T) {
	tracks := albumImportTracksFromDerived(map[string]any{
		"commit_request": map[string]any{
			"deleted_import_track_keys": []any{"audio:audio-second"},
		},
		"derived_tracks": []any{
			map[string]any{
				"file_id": "file-first", "audio_key": "audio-first", "title": "First",
				"track_number": 1, "disc_number": 1, "original_title": "Original First",
				"original_track_number": 7, "match_status": "matched",
			},
			map[string]any{
				"file_id": "file-second", "audio_key": "audio-second", "title": "Second",
				"track_number": 2, "disc_number": 1,
			},
		},
	})
	if len(tracks) != 1 || tracks[0].FileID != "file-first" || tracks[0].AudioKey != "audio-first" || tracks[0].OriginalTrack != 7 || tracks[0].MatchStatus != "matched" {
		t.Fatalf("unexpected restored derived tracks: %#v", tracks)
	}
}
