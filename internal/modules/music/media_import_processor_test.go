package music

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"atoman/internal/model"
)

type fakeMediaCommandRunner struct {
	paths map[string]string
	runs  [][]string
}

func (r *fakeMediaCommandRunner) LookPath(name string) (string, error) {
	if path := r.paths[name]; path != "" {
		return path, nil
	}
	return "", errMissingMediaTool{name: name}
}

func (r *fakeMediaCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.runs = append(r.runs, append([]string{name}, args...))
	if name == "ffprobe" {
		return []byte(`{"format":{"duration":"123.5","tags":{"title":"Tagged title"}}}`), nil
	}
	for _, arg := range args {
		if strings.HasSuffix(arg, ".mp3") || strings.HasSuffix(arg, ".webp") {
			return nil, os.WriteFile(arg, []byte("derived"), 0600)
		}
	}
	return nil, nil
}

type fakeMediaStore struct {
	objects map[string][]byte
	puts    map[string][]byte
}

func (s *fakeMediaStore) OpenObject(key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.objects[key])), nil
}

func (s *fakeMediaStore) PutObject(key, _ string, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.puts[key] = data
	return nil
}

func TestValidateMediaToolchainRequiresAllTools(t *testing.T) {
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	if err := ValidateMediaToolchain(runner); err == nil || !strings.Contains(err.Error(), "7zz") {
		t.Fatalf("expected missing 7zz error, got %v", err)
	}
}

func TestMediaImportProcessorTranscodesUploadedAudioAndUpdatesFile(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	file := model.AlbumImportFile{ImportID: session.ID, RelativePath: "Disc 1/01 - First.flac", FileName: "01 - First.flac", Role: AlbumImportFileRoleAudio, DetectedFormat: "flac", SourceKey: "source/first.flac", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending}
	if err := db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	job := model.AlbumImportJob{ImportID: session.ID}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe", "7zz": "/bin/7zz"}}
	store := &fakeMediaStore{objects: map[string][]byte{file.SourceKey: []byte("audio")}, puts: map[string][]byte{}}
	processor := NewMediaImportProcessor(db, store, runner, "https://play.example/")

	if err := processor.Process(context.Background(), job, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var got model.AlbumImportFile
	if err := db.First(&got, "id = ?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.PlaybackKey == "" || !strings.HasSuffix(got.PlaybackKey, ".mp3") || got.Title != "Tagged title" || got.DiscNumber != 1 || got.TrackNumber != 1 || got.DurationSeconds != 123.5 || got.ProcessingStatus != "completed" {
		t.Fatalf("unexpected processed file: %#v", got)
	}
	if len(store.puts) != 1 || len(store.puts[got.PlaybackKey]) == 0 {
		t.Fatalf("expected playback object at %q, got %#v", got.PlaybackKey, store.puts)
	}
	if len(runner.runs) != 2 || runner.runs[1][0] != "ffmpeg" || !containsMediaArg(runner.runs[1], "320k") {
		t.Fatalf("expected ffprobe then 320k ffmpeg, got %#v", runner.runs)
	}
	var storedSession model.AlbumImportSession
	if err := db.First(&storedSession, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedSession.Stage != AlbumImportStageTranscoding || storedSession.ProgressCurrent != 1 || storedSession.ProgressTotal != 1 {
		t.Fatalf("unexpected session progress: %#v", storedSession)
	}
	if filepath.Ext(got.PlaybackKey) != ".mp3" {
		t.Fatalf("playback extension = %q", filepath.Ext(got.PlaybackKey))
	}
}

func containsMediaArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
