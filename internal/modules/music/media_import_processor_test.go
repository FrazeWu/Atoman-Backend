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
	"golang.org/x/text/encoding/simplifiedchinese"
)

type fakeMediaCommandRunner struct {
	paths map[string]string
	runs  [][]string
	run   func(string, []string) ([]byte, error)
}

func (r *fakeMediaCommandRunner) LookPath(name string) (string, error) {
	if path := r.paths[name]; path != "" {
		return path, nil
	}
	return "", errMissingMediaTool{name: name}
}

func (r *fakeMediaCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.runs = append(r.runs, append([]string{name}, args...))
	if r.run != nil {
		return r.run(name, args)
	}
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

func TestMediaImportProcessorExtractsArchiveInTempTreeAndKeepsDiscPath(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	archive := model.AlbumImportFile{ImportID: session.ID, RelativePath: "album.zip", FileName: "album.zip", Role: AlbumImportFileRoleArchive, DetectedFormat: "zip", SourceKey: "source/album.zip", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending}
	if err := db.Create(&archive).Error; err != nil {
		t.Fatal(err)
	}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe", "7zz": "/bin/7zz"}}
	runner.run = func(name string, args []string) ([]byte, error) {
		if name == "7zz" && args[0] == "l" {
			return []byte("Path = Disc 2/01 - Second.flac\nSize = 4\nPacked Size = 4\n\n"), nil
		}
		if name == "7zz" && args[0] == "x" {
			for _, arg := range args {
				if strings.HasPrefix(arg, "-o") {
					return nil, os.MkdirAll(filepath.Join(strings.TrimPrefix(arg, "-o"), "Disc 2"), 0700)
				}
			}
			return nil, nil
		}
		if name == "7zz" {
			return nil, nil
		}
		if name == "ffprobe" {
			return []byte(`{"format":{"duration":"12","tags":{}}}`), nil
		}
		for _, arg := range args {
			if strings.HasSuffix(arg, ".mp3") {
				return nil, os.WriteFile(arg, []byte("derived"), 0600)
			}
		}
		return nil, nil
	}
	// The runner creates the directory; materialize the audio on the second command invocation.
	original := runner.run
	runner.run = func(name string, args []string) ([]byte, error) {
		result, err := original(name, args)
		if name == "7zz" && len(args) > 0 && args[0] == "x" && err == nil {
			for _, arg := range args {
				if strings.HasPrefix(arg, "-o") {
					_ = os.WriteFile(filepath.Join(strings.TrimPrefix(arg, "-o"), "Disc 2", "01 - Second.flac"), []byte("audio"), 0600)
				}
			}
		}
		return result, err
	}
	store := &fakeMediaStore{objects: map[string][]byte{archive.SourceKey: []byte("zip")}, puts: map[string][]byte{}}
	if err := NewMediaImportProcessor(db, store, runner, "").Process(context.Background(), model.AlbumImportJob{ImportID: session.ID}, nil); err != nil {
		t.Fatal(err)
	}
	var files []model.AlbumImportFile
	if err := db.Where("import_id = ? AND role = ?", session.ID, AlbumImportFileRoleAudio).Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].DiscNumber != 2 || files[0].PlaybackKey == "" || files[0].SourceKey != "" {
		t.Fatalf("unexpected extracted file: %#v", files)
	}
	if len(runner.runs) < 2 || runner.runs[0][0] != "7zz" || runner.runs[0][1] != "l" || runner.runs[1][0] != "7zz" || runner.runs[1][1] != "x" {
		t.Fatalf("expected list then extract, got %#v", runner.runs)
	}
}

func TestProcessCUEAudioCreatesIndependentRangesAndCueTitles(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "Disc 1", "album.flac")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	store := &fakeMediaStore{objects: map[string][]byte{}, puts: map[string][]byte{}}
	processor := NewMediaImportProcessor(db, store, runner, "")
	tracks := []cueTrack{{file: "album.flac", number: 1, title: "Cue One", startSeconds: 0}, {file: "album.flac", number: 2, title: "Cue Two", startSeconds: 10}}
	if err := processor.processCUEAudio(context.Background(), session.ID, source, "Disc 1/album.flac", tracks); err != nil {
		t.Fatal(err)
	}
	var files []model.AlbumImportFile
	if err := db.Where("import_id = ? AND role = ?", session.ID, AlbumImportFileRoleAudio).Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Title == "Tagged title" || files[0].PlaybackKey == files[1].PlaybackKey {
		t.Fatalf("unexpected cue files: %#v", files)
	}
	foundRange := false
	for _, run := range runner.runs {
		if run[0] == "ffmpeg" && containsMediaArg(run, "-ss") && containsMediaArg(run, "-to") {
			foundRange = true
		}
	}
	if !foundRange {
		t.Fatalf("missing CUE ffmpeg ranges: %#v", runner.runs)
	}
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
	if err := ValidateMediaToolchain(runner); err != nil {
		t.Fatalf("non-archive import must not require 7zz: %v", err)
	}
	if err := ValidateArchiveToolchain(runner); err == nil || !strings.Contains(err.Error(), "7zz") {
		t.Fatalf("archive import must require 7zz, got %v", err)
	}
}

func TestValidateArchiveListingRejectsDangerousEntriesAndLimits(t *testing.T) {
	tests := []string{
		"Path = /etc/passwd\nSize = 1\nPacked Size = 1\n",
		"Path = Disc/../../etc/passwd\nSize = 1\nPacked Size = 1\n",
		"Path = Disc/link\nSize = 1\nPacked Size = 1\nAttributes = lrwxrwxrwx\n",
		"Path = Disc/bomb.flac\nSize = 101\nPacked Size = 1\n",
		"Path = Disc/large.flac\nSize = 32212254721\nPacked Size = 32212254721\n",
	}
	for _, listing := range tests {
		if err := validateArchiveListing([]byte(listing)); err == nil {
			t.Fatal("expected archive listing rejection")
		}
	}
	var entries strings.Builder
	for i := 0; i <= mediaArchiveMaxEntries; i++ {
		entries.WriteString("Path = Disc/track.flac\nSize = 1\nPacked Size = 1\n\n")
	}
	if err := validateArchiveListing([]byte(entries.String())); err == nil {
		t.Fatal("expected entry count rejection")
	}
}

func TestParseCUESupportsUTF8AndGBK(t *testing.T) {
	utf8Cue := []byte("FILE \"album.flac\" WAVE\nTRACK 01 AUDIO\nTITLE \"Cue First\"\nINDEX 01 00:00:00\nTRACK 02 AUDIO\nTITLE \"Cue Second\"\nINDEX 01 03:01:50\n")
	gbkCue, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("FILE \"album.flac\" WAVE\nTRACK 01 AUDIO\nTITLE \"中文标题\"\nINDEX 01 00:00:00\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{utf8Cue, gbkCue} {
		tracks, err := parseCUE(raw)
		if err != nil || len(tracks) == 0 || tracks[0].title == "" || tracks[0].startSeconds != 0 {
			t.Fatalf("unexpected CUE parse: tracks=%#v err=%v", tracks, err)
		}
	}
	tracks, _ := parseCUE(utf8Cue)
	if tracks[0].title != "Cue First" || tracks[1].startSeconds != 181.0+50.0/75.0 {
		t.Fatalf("unexpected CUE tracks: %#v", tracks)
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
