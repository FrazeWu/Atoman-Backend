package music

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"gorm.io/gorm"
)

func TestCommitAlbumImportSessionReadyCreatesArtistAndAlbum(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusReady,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{
				Name:      "FKA twigs",
				LegalName: "Tahliah Debrett Barnett",
				StageNames: []ArtistStageNamePayload{
					{Name: "FKA twigs", IsPrimary: true, StartDateText: "2012"},
					{Name: "Twigs", IsPrimary: false, EndDateText: "2012"},
				},
				BirthPlace: "Cheltenham, England",
			},
			Album: AlbumImportAlbumPayload{
				Title:       "LP1",
				ReleaseYear: 2014,
				Tracks: []AlbumImportTrackPayload{
					{Title: "Preface", TrackNumber: 1},
					{Title: "Lights On", TrackNumber: 2},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	committed, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: AlbumImportArtistPayload{
			Name:      "FKA twigs",
			LegalName: "Tahliah Debrett Barnett",
			StageNames: []ArtistStageNamePayload{
				{Name: "FKA twigs", IsPrimary: true, StartDateText: "2012"},
				{Name: "Twigs", IsPrimary: false, EndDateText: "2012"},
			},
			BirthPlace: "Cheltenham, England",
		},
		Album: AlbumImportAlbumPayload{
			Title:       "LP1",
			ReleaseYear: 2014,
			Tracks: []AlbumImportTrackPayload{
				{Title: "Preface", TrackNumber: 1},
				{Title: "Lights On", TrackNumber: 2},
			},
		},
	})
	if err != nil {
		t.Fatalf("commit session: %v", err)
	}
	if committed.Status != AlbumImportStatusCommitted {
		t.Fatalf("expected committed status, got %#v", committed)
	}

	var artist model.Artist
	if err := db.Where("name = ?", "FKA twigs").First(&artist).Error; err != nil {
		t.Fatalf("load artist: %v", err)
	}
	if artist.LegalName != "Tahliah Debrett Barnett" {
		t.Fatalf("expected legal name persisted, got %#v", artist)
	}
	var stageNames []ArtistStageNamePayload
	if err := json.Unmarshal([]byte(artist.StageNamesJSON), &stageNames); err != nil {
		t.Fatalf("unmarshal stage names json: %v", err)
	}
	if len(stageNames) != 2 || !stageNames[0].IsPrimary || stageNames[0].Name != "FKA twigs" || stageNames[0].StartDateText != "2012" || stageNames[1].EndDateText != "2012" {
		t.Fatalf("expected structured stage names persisted, got %#v", stageNames)
	}
	if artist.BirthPlace != "Cheltenham, England" {
		t.Fatalf("expected birth place persisted, got %#v", artist)
	}

	var album model.Album
	if err := db.Preload("Artists").Where("title = ?", "LP1").First(&album).Error; err != nil {
		t.Fatalf("load album: %v", err)
	}
	if album.ReleaseYear != 2014 {
		t.Fatalf("expected release year persisted, got %#v", album)
	}
	if len(album.Artists) != 1 || album.Artists[0].ID != artist.ID {
		t.Fatalf("expected album linked to artist, got %#v", album.Artists)
	}
}

func TestCommitAlbumImportSessionRejectsNonReadyStatus(t *testing.T) {
	svc, _, user := newMusicTestService(t)

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Burial"},
			Album:  AlbumImportAlbumPayload{Title: "Untrue"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err = svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: AlbumImportArtistPayload{Name: "Burial"},
		Album:  AlbumImportAlbumPayload{Title: "Untrue"},
	})
	var appErr *apperr.AppError
	if err == nil {
		t.Fatal("expected commit to fail for non-ready session")
	}
	if !errors.As(err, &appErr) {
		t.Fatalf("expected app error, got %v", err)
	}
	if appErr.Code != "music.import_invalid_status" {
		t.Fatalf("expected import_invalid_status, got %#v", appErr)
	}
}

func TestUploadAlbumImportArchiveTransitionsPendingUploadToReady(t *testing.T) {
	svc, _, user := newMusicTestService(t)

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Burial"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	archiveName := "Untrue (Deluxe).zip"
	archiveBody := newImportTestZipArchive(t, map[string]string{
		"01 - Untitled.mp3": "",
		"02 - Archangel.flac": "",
		"booklet/cover.jpg":  "",
	})

	updated, err := svc.UploadAlbumImportArchive(user, session.ID, archiveName, bytes.NewReader(archiveBody))
	if err != nil {
		t.Fatalf("upload archive: %v", err)
	}
	if updated.Status != AlbumImportStatusReady {
		t.Fatalf("expected ready status, got %#v", updated)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(updated.PayloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload json: %v", err)
	}

	if payload["archive_name"] != archiveName {
		t.Fatalf("expected archive_name %q, got %#v", archiveName, payload["archive_name"])
	}
	if payload["derived_album_title"] != "Untrue (Deluxe)" {
		t.Fatalf("expected derived_album_title, got %#v", payload["derived_album_title"])
	}
	derivedTracks, ok := payload["derived_tracks"].([]any)
	if !ok {
		t.Fatalf("expected derived_tracks array, got %#v", payload["derived_tracks"])
	}
	if len(derivedTracks) != 2 {
		t.Fatalf("expected 2 derived tracks, got %#v", derivedTracks)
	}
	assertDerivedTrackPresent(t, derivedTracks, "Untitled", 1)
	assertDerivedTrackPresent(t, derivedTracks, "Archangel", 2)
}

func TestCommitAlbumImportSessionRollsBackArtistWhenAlbumCreateFails(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	prevHook := albumImportCreateAlbumHook
	albumImportCreateAlbumHook = func(_ *gorm.DB, _ *model.Album) error {
		return fmt.Errorf("forced album create failure")
	}
	defer func() {
		albumImportCreateAlbumHook = prevHook
	}()

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusReady,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{
				Name:      "Rollback Artist",
				LegalName: "Rollback Legal",
			},
			Album: AlbumImportAlbumPayload{
				Title: "LP1",
			},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: AlbumImportArtistPayload{
			Name:      "Rollback Artist",
			LegalName: "Rollback Legal",
		},
		Album: AlbumImportAlbumPayload{
			Title: "LP1",
		},
	}); err == nil {
		t.Fatal("expected commit to fail when album create fails")
	}

	var artists int64
	if err := db.Model(&model.Artist{}).Where("name = ?", "Rollback Artist").Count(&artists).Error; err != nil {
		t.Fatalf("count artists: %v", err)
	}
	if artists != 0 {
		t.Fatalf("expected rollback artist not persisted, got %d", artists)
	}
}

func newImportTestZipArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func assertDerivedTrackPresent(t *testing.T, tracks []any, title string, trackNumber int) {
	t.Helper()

	for _, raw := range tracks {
		track, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if track["title"] == title && track["track_number"] == float64(trackNumber) {
			return
		}
	}
	t.Fatalf("expected derived track %q #%d in %#v", title, trackNumber, tracks)
}
