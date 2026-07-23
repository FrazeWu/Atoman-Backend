package music

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

func TestRegisterRoutesAlbumImportFileUploadLifecycle(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	store := &fakeAlbumImportMultipartStore{uploadID: "upload-http-file", signedURL: "https://storage.test/file-part"}
	service.albumImportMultipart = store
	session, err := service.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	router := newMusicHTTPRouter(service, &user)
	basePath := "/api/v1/music/imports/albums/" + session.ID.String()

	registered := performMusicJSONRequest(t, router, http.MethodPost, basePath+"/files", `{"files":[{"relativePath":"CD1/01.flac","fileName":"01.flac","fileSize":1024,"contentType":"audio/flac"}]}`)
	if registered.Code != http.StatusOK {
		t.Fatalf("register files: %d %s", registered.Code, registered.Body.String())
	}
	var registeredResponse struct {
		Data AlbumImportDTO `json:"data"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &registeredResponse); err != nil {
		t.Fatal(err)
	}
	if registeredResponse.Data.InputMode != AlbumImportInputModeFolder || len(registeredResponse.Data.Files) != 1 || registeredResponse.Data.Files[0].CompletedParts == nil {
		t.Fatalf("unexpected registration response: %#v", registeredResponse.Data)
	}
	file := registeredResponse.Data.Files[0]
	filePath := basePath + "/files/" + file.ID

	partURL := performMusicJSONRequest(t, router, http.MethodPost, filePath+"/parts/1", `{}`)
	if partURL.Code != http.StatusOK || !stringsContain(partURL.Body.String(), store.signedURL) {
		t.Fatalf("presign part: %d %s", partURL.Code, partURL.Body.String())
	}
	partComplete := performMusicJSONRequest(t, router, http.MethodPost, filePath+"/parts/1/complete", `{"etag":"etag-1","size":1024}`)
	if partComplete.Code != http.StatusOK || !stringsContain(partComplete.Body.String(), `"completedParts":[`) {
		t.Fatalf("complete part: %d %s", partComplete.Code, partComplete.Body.String())
	}
	fileComplete := performMusicJSONRequest(t, router, http.MethodPost, filePath+"/complete", "")
	if fileComplete.Code != http.StatusOK || !stringsContain(fileComplete.Body.String(), `"uploadStatus":"uploaded"`) {
		t.Fatalf("complete file: %d %s", fileComplete.Code, fileComplete.Body.String())
	}
	queued := performMusicJSONRequest(t, router, http.MethodPost, basePath+"/complete", "")
	if queued.Code != http.StatusOK || !stringsContain(queued.Body.String(), `"status":"queued"`) {
		t.Fatalf("queue import: %d %s", queued.Code, queued.Body.String())
	}
}

func TestRegisterRoutesAlbumImportFileManagement(t *testing.T) {
	t.Run("delete", func(t *testing.T) {
		service, _, user := newMusicHTTPTestService(t)
		service.albumImportMultipart = &fakeAlbumImportMultipartStore{}
		session, file := registerAlbumImportFilesForTest(t, service, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
		response := performMusicJSONRequest(t, newMusicHTTPRouter(service, &user), http.MethodDelete, albumImportFileHTTPPath(session.ID, file.ID), "")
		if response.Code != http.StatusNoContent {
			t.Fatalf("delete file: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("replace", func(t *testing.T) {
		service, _, user := newMusicHTTPTestService(t)
		service.albumImportMultipart = &fakeAlbumImportMultipartStore{}
		session, file := registerAlbumImportFilesForTest(t, service, user, []AlbumImportFileInput{albumImportFileInput("old.mp3", 1024)})
		response := performMusicJSONRequest(t, newMusicHTTPRouter(service, &user), http.MethodPost, albumImportFileHTTPPath(session.ID, file.ID)+"/replace", `{"relativePath":"new.flac","fileName":"new.flac","fileSize":2048,"contentType":"audio/flac"}`)
		if response.Code != http.StatusOK || !stringsContain(response.Body.String(), `"fileName":"new.flac"`) {
			t.Fatalf("replace file: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("retry", func(t *testing.T) {
		service, db, user := newMusicHTTPTestService(t)
		service.albumImportMultipart = &fakeAlbumImportMultipartStore{}
		session, file := registerAlbumImportFilesForTest(t, service, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
		if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Updates(map[string]any{
			"upload_status": AlbumImportFileUploadStatusUploaded, "processing_status": AlbumImportFileProcessingStatusFailed, "error_message": "failed",
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Update("status", AlbumImportStatusNeedsAttention).Error; err != nil {
			t.Fatal(err)
		}
		response := performMusicJSONRequest(t, newMusicHTTPRouter(service, &user), http.MethodPost, albumImportFileHTTPPath(session.ID, file.ID)+"/retry", "")
		if response.Code != http.StatusOK || !stringsContain(response.Body.String(), `"status":"queued"`) {
			t.Fatalf("retry file: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("cancel", func(t *testing.T) {
		service, _, user := newMusicHTTPTestService(t)
		service.albumImportMultipart = &fakeAlbumImportMultipartStore{}
		session, _ := registerAlbumImportFilesForTest(t, service, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
		path := "/api/v1/music/imports/albums/" + session.ID.String()
		response := performMusicJSONRequest(t, newMusicHTTPRouter(service, &user), http.MethodDelete, path, "")
		if response.Code != http.StatusOK || !stringsContain(response.Body.String(), `"status":"canceled"`) {
			t.Fatalf("cancel import: %d %s", response.Code, response.Body.String())
		}
	})
}

func TestRegisterRoutesAlbumImportNewWritesRejectAnotherUser(t *testing.T) {
	service, _, owner := newMusicHTTPTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	service.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, service, owner, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
	other := authctx.CurrentUser{ID: uuid.New(), Username: "other", Role: authctx.RoleUser}
	router := newMusicHTTPRouter(service, &other)
	basePath := "/api/v1/music/imports/albums/" + session.ID.String()
	filePath := basePath + "/files/" + file.ID.String()

	tests := []struct {
		name, method, path, body string
	}{
		{name: "register", method: http.MethodPost, path: basePath + "/files", body: `{"files":[{"relativePath":"other.flac","fileName":"other.flac","fileSize":1,"contentType":"audio/flac"}]}`},
		{name: "part URL", method: http.MethodPost, path: filePath + "/parts/1", body: `{}`},
		{name: "part complete", method: http.MethodPost, path: filePath + "/parts/1/complete", body: `{"etag":"etag","size":1}`},
		{name: "file complete", method: http.MethodPost, path: filePath + "/complete"},
		{name: "session complete", method: http.MethodPost, path: basePath + "/complete"},
		{name: "delete file", method: http.MethodDelete, path: filePath},
		{name: "retry", method: http.MethodPost, path: filePath + "/retry"},
		{name: "replace", method: http.MethodPost, path: filePath + "/replace", body: `{"relativePath":"new.flac","fileName":"new.flac","fileSize":1}`},
		{name: "cancel", method: http.MethodDelete, path: basePath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performMusicJSONRequest(t, router, test.method, test.path, test.body)
			assertMusicHTTPError(t, response, http.StatusNotFound, "music.import_not_found")
		})
	}
}

func albumImportFileHTTPPath(sessionID, fileID uuid.UUID) string {
	return "/api/v1/music/imports/albums/" + sessionID.String() + "/files/" + fileID.String()
}

func stringsContain(value, part string) bool {
	return len(part) <= len(value) && strings.Contains(value, part)
}
