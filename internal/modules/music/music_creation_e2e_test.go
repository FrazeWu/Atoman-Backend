package music

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"atoman/internal/model"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	awssession "github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
)

// 隔离对象存储以真实文件保存上传、转码及复制结果，不访问生产桶。
type musicE2EDiskStore struct {
	fakeAlbumImportMultipartStore
	root string
}

func (s *musicE2EDiskStore) PutObject(key, _ string, body io.Reader) error {
	path := filepath.Join(s.root, key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, body)
	return err
}
func (s *musicE2EDiskStore) OpenObject(key string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(s.root, key))
}
func (s *musicE2EDiskStore) DeleteObject(key string) error {
	return os.Remove(filepath.Join(s.root, key))
}
func (s *musicE2EDiskStore) ObjectSize(key string) (int64, error) {
	info, err := os.Stat(filepath.Join(s.root, key))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func TestMusicCreationRealAudioWikiEndToEnd(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe", "7zz"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("真实音频验收需要 " + tool)
		}
	}
	s, db, user := newMusicTestService(t)
	store := &musicE2EDiskStore{root: t.TempDir()}
	s.albumImportMultipart = store
	objects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		if r.Method == http.MethodPut {
			source, _ := url.PathUnescape(strings.TrimPrefix(r.Header.Get("x-amz-copy-source"), "/"))
			source = strings.TrimPrefix(source, "bucket/")
			f, err := store.OpenObject(source)
			if err != nil {
				http.Error(w, err.Error(), 404)
				return
			}
			defer f.Close()
			if err = store.PutObject(key, "audio/mpeg", f); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<CopyObjectResult><ETag>"e2e"</ETag><LastModified>2026-10-23T00:00:00Z</LastModified></CopyObjectResult>`)
			return
		}
		http.ServeFile(w, r, filepath.Join(store.root, key))
	}))
	defer objects.Close()
	prefix := objects.URL + "/bucket"
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("S3_BUCKET", "bucket")
	t.Setenv("S3_URL_PREFIX", prefix)
	t.Setenv("MUSIC_PLAYBACK_URL_PREFIX", prefix)
	s.s3 = s3.New(awssession.Must(awssession.NewSession(&aws.Config{Endpoint: aws.String(objects.URL), Region: aws.String("test"), S3ForcePathStyle: aws.Bool(true), Credentials: credentials.NewStaticCredentials("test", "test", "")})))
	audioPath := filepath.Join(t.TempDir(), "track.mp3")
	if output, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-metadata", "title=Track", "-metadata", "album=Source Album", "-metadata", "artist=Source Artist", "-y", audioPath).CombinedOutput(); err != nil {
		t.Fatalf("generate audio: %s %v", output, err)
	}
	audio, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("Source Artist/Source Album/01 Track.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write(audio); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	router := newMusicHTTPRouter(s, &user)
	jsonRequest := func(method, path string, input any) []byte {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response := performMusicJSONRequest(t, router, method, "/api/v1/music"+path, string(body))
		if response.Code < 200 || response.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	create := func(input CommitAlbumImportSessionInput) model.Album {
		t.Helper()
		raw := jsonRequest("POST", "/imports/albums", map[string]any{})
		var envelope struct{ Data AlbumImportDTO }
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		id := envelope.Data.ImportID
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("archive", "album.zip")
		if err != nil {
			t.Fatal(err)
		}
		part.Write(archive.Bytes())
		form.Close()
		req := httptest.NewRequest("POST", "/api/v1/music/imports/albums/"+id+"/upload", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != 200 {
			t.Fatalf("upload: %s", response.Body.String())
		}
		worker := NewImportWorker(db, store, "music-real-e2e")
		if processed, err := worker.RunOnce(context.Background(), NewMediaImportProcessor(db, store, NewSystemMediaCommandRunner(), prefix)); err != nil || !processed {
			t.Fatalf("real process: %t %v", processed, err)
		}
		var session model.AlbumImportSession
		if err := db.First(&session, "id=?", id).Error; err != nil {
			t.Fatal(err)
		}
		var derived map[string]any
		json.Unmarshal([]byte(session.PayloadJSON), &derived)
		if stringValue(derived["derived_album_title"]) != "Source Album" {
			t.Fatalf("embedded metadata not identified: %s", session.PayloadJSON)
		}
		jsonRequest("POST", "/imports/albums/"+id+"/commit", input)
		if err := db.First(&session, "id=?", id).Error; err != nil {
			t.Fatal(err)
		}
		if session.TargetAlbumID == nil {
			t.Fatalf("create: %s", session.ErrorMessage)
		}
		var album model.Album
		if err := db.First(&album, "id=?", session.TargetAlbumID).Error; err != nil {
			t.Fatal(err)
		}
		jsonRequest("GET", "/albums/"+album.ID.String(), nil)
		return album
	}
	input := CommitAlbumImportSessionInput{Artist: AlbumImportArtistPayload{Name: "Minimal E2E Artist"}, Album: AlbumImportAlbumPayload{Title: "Edited Album", Tracks: []AlbumImportTrackPayload{{Title: "Track", TrackNumber: 1}}}}
	album := create(input)
	var song model.Song
	if err = db.First(&song, "album_id=?", album.ID).Error; err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(song.AudioURL)
	if err != nil {
		t.Fatal(err)
	}
	played, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || len(played) == 0 {
		t.Fatal("published audio cannot play")
	}
	playPath := filepath.Join(t.TempDir(), "play.mp3")
	if err = os.WriteFile(playPath, played, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", playPath).CombinedOutput(); err != nil {
		t.Fatalf("invalid playback: %s %v", output, err)
	}
	jsonRequest("POST", "/albums/"+album.ID.String()+"/tags", map[string]any{"kind": "type", "name": "E2E Folk"})
	tags, err := s.ListMusicTags(&user, "album", album.ID)
	if err != nil || len(tags) != 1 {
		t.Fatal("album tag failed", err)
	}
	tagID := tags[0].ID.String()
	songTags, err := s.ListMusicTags(&user, "song", song.ID)
	if err != nil || len(songTags) != 0 {
		t.Fatal("album tag inherited", err)
	}
	jsonRequest("POST", "/songs/"+song.ID.String()+"/tags", map[string]any{"kind": "type", "name": "E2E Folk"})
	jsonRequest("GET", "/songs?tag_id="+tagID, nil)
	jsonRequest("PUT", "/songs/"+song.ID.String()+"/tags/"+tagID+"/vote", map[string]any{"vote": "down"})
	before, _ := s.ListMusicTags(&user, "song", song.ID)
	jsonRequest("DELETE", "/songs/"+song.ID.String()+"/tags/"+tagID, nil)
	jsonRequest("POST", "/songs/"+song.ID.String()+"/tags", map[string]any{"kind": "type", "name": "E2E Folk"})
	after, _ := s.ListMusicTags(&user, "song", song.ID)
	if len(after) != 1 || after[0].AssignmentID != before[0].AssignmentID || after[0].Downvotes != 1 {
		t.Fatal("readd lost identity or votes")
	}
	var artist model.Artist
	if err = db.First(&artist, "name=?", "Minimal E2E Artist").Error; err != nil {
		t.Fatal(err)
	}
	input.ArtistID = artist.ID.String()
	input.Album.CanonicalAlbumID = &album.ID
	input.Album.EditionType = "remaster"
	input.Album.ReleaseDate = "2026"
	remaster := create(input)
	var remasterSong model.Song
	if err = db.First(&remasterSong, "album_id=?", remaster.ID).Error; err != nil {
		t.Fatal(err)
	}
	if remasterSong.ID == song.ID {
		t.Fatal("remaster shared song identity")
	}
	for kind, id := range map[string]uuid.UUID{"album": remaster.ID, "song": remasterSong.ID} {
		rows, err := s.ListMusicTags(&user, kind, id)
		if err != nil || len(rows) != 0 {
			t.Fatal("edition copied tags", err)
		}
	}
}
