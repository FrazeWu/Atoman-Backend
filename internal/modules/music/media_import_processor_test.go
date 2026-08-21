package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
					_ = os.MkdirAll(filepath.Join(strings.TrimPrefix(arg, "-o"), "__MACOSX", "Disc 2"), 0700)
					_ = os.WriteFile(filepath.Join(strings.TrimPrefix(arg, "-o"), "__MACOSX", "Disc 2", "._01 - Second.flac"), []byte("resource fork"), 0600)
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

func TestProcessExtractedTreeFallsBackToEmbeddedAudioCover(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	first := filepath.Join(root, "01 - First.flac")
	second := filepath.Join(root, "02 - Second.flac")
	for _, source := range []string{first, second} {
		if err := os.WriteFile(source, []byte("audio"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	extractAttempts := 0
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	runner.run = func(name string, args []string) ([]byte, error) {
		if name == "ffprobe" {
			if containsMediaArg(args, "stream=width,height") {
				return []byte(`{"streams":[{"width":600,"height":600}]}`), nil
			}
			return []byte(`{"format":{"duration":"12","tags":{}}}`), nil
		}
		if name == "ffmpeg" && containsMediaArg(args, "0:v:0") {
			extractAttempts++
			if extractAttempts == 1 {
				return nil, errors.New("no embedded artwork")
			}
			return nil, os.WriteFile(args[len(args)-1], []byte("cover"), 0600)
		}
		output := args[len(args)-1]
		if strings.HasSuffix(output, ".mp3") {
			return nil, os.WriteFile(output, []byte("audio"), 0600)
		}
		if strings.HasSuffix(output, ".webp") {
			return nil, os.WriteFile(output, []byte("cover"), 0600)
		}
		return nil, nil
	}
	store := &fakeMediaStore{objects: map[string][]byte{}, puts: map[string][]byte{}}
	if err := NewMediaImportProcessor(db, store, runner, "").processExtractedTree(context.Background(), session.ID, root, nil); err != nil {
		t.Fatal(err)
	}
	var stored model.AlbumImportSession
	if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.PayloadJSON, "cover_key") || extractAttempts != 2 {
		t.Fatalf("expected embedded cover fallback after two attempts, payload=%s attempts=%d", stored.PayloadJSON, extractAttempts)
	}
}

func TestProcessExtractedTreePrefersExplicitCoverOverEmbeddedAudioCover(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cover.jpg"), []byte("cover"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "01 - First.flac"), []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	store := &fakeMediaStore{objects: map[string][]byte{}, puts: map[string][]byte{}}
	if err := NewMediaImportProcessor(db, store, runner, "").processExtractedTree(context.Background(), session.ID, root, nil); err != nil {
		t.Fatal(err)
	}
	for _, run := range runner.runs {
		if run[0] == "ffmpeg" && containsMediaArg(run, "0:v:0") {
			t.Fatalf("explicit cover must prevent embedded artwork extraction: %#v", runner.runs)
		}
	}
}

func TestProcessExtractedTreeRejectsBannerEmbeddedCover(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"01 - Banner.flac", "02 - Cover.flac"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("audio"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	extractAttempts := 0
	dimensionProbes := 0
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	runner.run = func(name string, args []string) ([]byte, error) {
		if name == "ffprobe" {
			if containsMediaArg(args, "stream=width,height") {
				dimensionProbes++
				if dimensionProbes == 1 {
					return []byte(`{"streams":[{"width":861,"height":268}]}`), nil
				}
				return []byte(`{"streams":[{"width":600,"height":600}]}`), nil
			}
			return []byte(`{"format":{"duration":"12","tags":{}}}`), nil
		}
		if name == "ffmpeg" && containsMediaArg(args, "0:v:0") {
			extractAttempts++
			return nil, os.WriteFile(args[len(args)-1], []byte("cover"), 0600)
		}
		output := args[len(args)-1]
		if strings.HasSuffix(output, ".mp3") {
			return nil, os.WriteFile(output, []byte("audio"), 0600)
		}
		if strings.HasSuffix(output, ".webp") {
			return nil, os.WriteFile(output, []byte("cover"), 0600)
		}
		return nil, nil
	}
	store := &fakeMediaStore{objects: map[string][]byte{}, puts: map[string][]byte{}}
	if err := NewMediaImportProcessor(db, store, runner, "").processExtractedTree(context.Background(), session.ID, root, nil); err != nil {
		t.Fatal(err)
	}
	var stored model.AlbumImportSession
	if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.PayloadJSON, "cover_key") || extractAttempts != 2 || dimensionProbes != 2 {
		t.Fatalf("expected banner rejection followed by square cover, payload=%s extracts=%d probes=%d", stored.PayloadJSON, extractAttempts, dimensionProbes)
	}
}

func TestMediaImportProcessorArchiveRetrySkipsCompletedDerivedAudio(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	archive := model.AlbumImportFile{ImportID: session.ID, RelativePath: "album.zip", FileName: "album.zip", Role: AlbumImportFileRoleArchive, DetectedFormat: "zip", SourceKey: "source/album.zip", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending}
	if err := db.Create(&archive).Error; err != nil {
		t.Fatal(err)
	}
	ffmpegs := 0
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe", "7zz": "/bin/7zz"}}
	runner.run = func(name string, args []string) ([]byte, error) {
		if name == "7zz" && args[0] == "l" {
			return []byte("Path = Disc 1/01 - First.flac\nSize = 4\nPacked Size = 4\n\nPath = Disc 1/02 - Second.flac\nSize = 4\nPacked Size = 4\n\n"), nil
		}
		if name == "7zz" && args[0] == "x" {
			for _, arg := range args {
				if strings.HasPrefix(arg, "-o") {
					output := filepath.Join(strings.TrimPrefix(arg, "-o"), "Disc 1", "01 - First.flac")
					return nil, os.MkdirAll(filepath.Dir(output), 0700)
				}
			}
		}
		if name == "ffprobe" {
			return []byte(`{"format":{"duration":"12"}}`), nil
		}
		if name == "ffmpeg" && strings.HasSuffix(args[len(args)-1], ".mp3") {
			ffmpegs++
			if ffmpegs == 2 {
				return nil, errors.New("second track transient failure")
			}
			for _, arg := range args {
				if strings.HasSuffix(arg, ".mp3") {
					return nil, os.WriteFile(arg, []byte("derived"), 0600)
				}
			}
		}
		return nil, nil
	}
	original := runner.run
	runner.run = func(name string, args []string) ([]byte, error) {
		result, err := original(name, args)
		if name == "7zz" && len(args) > 0 && args[0] == "x" && err == nil {
			for _, arg := range args {
				if strings.HasPrefix(arg, "-o") {
					_ = os.WriteFile(filepath.Join(strings.TrimPrefix(arg, "-o"), "Disc 1", "01 - First.flac"), []byte("audio"), 0600)
					_ = os.WriteFile(filepath.Join(strings.TrimPrefix(arg, "-o"), "Disc 1", "02 - Second.flac"), []byte("audio"), 0600)
				}
			}
		}
		return result, err
	}
	store := &fakeMediaStore{objects: map[string][]byte{archive.SourceKey: []byte("zip")}, puts: map[string][]byte{}}
	processor := NewMediaImportProcessor(db, store, runner, "")
	job := model.AlbumImportJob{ImportID: session.ID}
	if err := processor.Process(context.Background(), job, nil); err != nil {
		t.Fatal(err)
	}
	if err := processor.Process(context.Background(), job, nil); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.AlbumImportFile{}).Where("import_id = ? AND role = ?", session.ID, AlbumImportFileRoleAudio).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 || ffmpegs != 3 || len(store.puts) != 2 {
		t.Fatalf("archive retry duplicated completed output: records=%d ffmpegs=%d objects=%d", count, ffmpegs, len(store.puts))
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
	if _, err := processor.processCUEAudio(context.Background(), session.ID, source, "Disc 1/album.flac", tracks); err != nil {
		t.Fatal(err)
	}
	var files []model.AlbumImportFile
	if err := db.Where("import_id = ? AND role = ?", session.ID, AlbumImportFileRoleAudio).Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Title == "Tagged title" || files[0].PlaybackKey == files[1].PlaybackKey {
		t.Fatalf("unexpected cue files: %#v", files)
	}
	if files[0].DurationSeconds != 10 || files[1].DurationSeconds != 113.5 {
		t.Fatalf("CUE durations must be derived from range, got %#v", files)
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

func TestMediaImportProcessorSplitsUploadedFolderCUESources(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{InputMode: AlbumImportInputModeFolder, Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	gbkCue, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("FILE \"audio/album.flac\" WAVE\nTRACK 01 AUDIO\nTITLE \"第一首\"\nINDEX 01 00:00:00\nTRACK 02 AUDIO\nTITLE \"第二首\"\nINDEX 01 00:10:00\n"))
	if err != nil {
		t.Fatal(err)
	}
	files := []model.AlbumImportFile{
		{ImportID: session.ID, RelativePath: "Album/Disc 1/disc.cue", FileName: "disc.cue", Role: AlbumImportFileRoleCue, DetectedFormat: "cue", SourceKey: "source/disc-1.cue", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending},
		{ImportID: session.ID, RelativePath: "Album/Disc 1/audio/album.flac", FileName: "album.flac", Role: AlbumImportFileRoleAudio, DetectedFormat: "flac", SourceKey: "source/disc-1.flac", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending},
		{ImportID: session.ID, RelativePath: "Album/Disc 2/disc.cue", FileName: "disc.cue", Role: AlbumImportFileRoleCue, DetectedFormat: "cue", SourceKey: "source/disc-2.cue", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending},
		{ImportID: session.ID, RelativePath: "Album/Disc 2/album.flac", FileName: "album.flac", Role: AlbumImportFileRoleAudio, DetectedFormat: "flac", SourceKey: "source/disc-2.flac", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending},
	}
	for index := range files {
		if err := db.Create(&files[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	store := &fakeMediaStore{objects: map[string][]byte{
		"source/disc-1.cue": gbkCue, "source/disc-1.flac": []byte("audio"),
		"source/disc-2.cue": []byte("FILE \"album.flac\" WAVE\nTRACK 01 AUDIO\nTITLE \"Disc Two\"\nINDEX 01 00:00:00\n"), "source/disc-2.flac": []byte("audio"),
	}, puts: map[string][]byte{}}
	if err := NewMediaImportProcessor(db, store, runner, "").Process(context.Background(), model.AlbumImportJob{ImportID: session.ID}, nil); err != nil {
		t.Fatal(err)
	}
	var derived []model.AlbumImportFile
	if err := db.Where("import_id = ? AND role = ? AND playback_key <> ''", session.ID, AlbumImportFileRoleAudio).Find(&derived).Error; err != nil {
		t.Fatal(err)
	}
	titles := map[string]bool{}
	keys := map[string]bool{}
	discTwo := false
	for _, derivedFile := range derived {
		titles[derivedFile.Title] = true
		keys[derivedFile.PlaybackKey] = true
		discTwo = discTwo || derivedFile.DiscNumber == 2
	}
	if len(derived) != 3 || titles["Tagged title"] || len(keys) != 3 || !discTwo {
		t.Fatalf("unexpected CUE-derived files: %#v", derived)
	}
	var storedSession model.AlbumImportSession
	if err := db.First(&storedSession, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedSession.ProgressCurrent != 3 || storedSession.ProgressTotal != 3 {
		t.Fatalf("unexpected CUE progress: %#v", storedSession)
	}
	ffmpegs := 0
	for _, run := range runner.runs {
		if run[0] == "ffmpeg" && containsMediaArg(run, "320k") {
			ffmpegs++
			if !containsMediaArg(run, "-ss") || !containsMediaArg(run, "-to") {
				t.Fatalf("whole source was transcoded instead of CUE split: %#v", runner.runs)
			}
		}
	}
	if ffmpegs != 3 {
		t.Fatalf("expected exactly three CUE splits, got %#v", runner.runs)
	}
}

func TestMediaImportProcessorFallsBackWhenUploadedCUEHasNoMatchingAudio(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	files := []model.AlbumImportFile{
		{ImportID: session.ID, RelativePath: "disc.cue", FileName: "disc.cue", Role: AlbumImportFileRoleCue, DetectedFormat: "cue", SourceKey: "source/disc.cue", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending},
		{ImportID: session.ID, RelativePath: "album.flac", FileName: "album.flac", Role: AlbumImportFileRoleAudio, DetectedFormat: "flac", SourceKey: "source/album.flac", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending},
	}
	for index := range files {
		if err := db.Create(&files[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	store := &fakeMediaStore{objects: map[string][]byte{"source/disc.cue": []byte("FILE \"missing.flac\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n"), "source/album.flac": []byte("audio")}, puts: map[string][]byte{}}
	if err := NewMediaImportProcessor(db, store, runner, "").Process(context.Background(), model.AlbumImportJob{ImportID: session.ID}, nil); err != nil {
		t.Fatal(err)
	}
	for _, run := range runner.runs {
		if run[0] == "ffmpeg" && (containsMediaArg(run, "-ss") || containsMediaArg(run, "-to")) {
			t.Fatalf("unmatched CUE must fall back to whole-file processing: %#v", runner.runs)
		}
	}
}

func TestProcessCUEAudioContinuesAfterOneTrackFails(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "album.flac")
	if err := os.WriteFile(source, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	runner.run = func(name string, args []string) ([]byte, error) {
		if name == "ffprobe" {
			return []byte(`{"format":{"duration":"30"}}`), nil
		}
		if name == "ffmpeg" && len(args) > 1 {
			for index, arg := range args[:len(args)-1] {
				if arg == "-ss" && args[index+1] == "10" {
					return nil, errors.New("second track failed")
				}
			}
		}
		for _, arg := range args {
			if strings.HasSuffix(arg, ".mp3") {
				return nil, os.WriteFile(arg, []byte("derived"), 0600)
			}
		}
		return nil, nil
	}
	processor := NewMediaImportProcessor(db, &fakeMediaStore{objects: map[string][]byte{}, puts: map[string][]byte{}}, runner, "")
	count, err := processor.processCUEAudio(context.Background(), session.ID, source, "Disc 1/album.flac", []cueTrack{{file: "album.flac", number: 1, title: "One", startSeconds: 0}, {file: "album.flac", number: 2, title: "Two", startSeconds: 10}})
	if err != nil || count != 1 {
		t.Fatalf("expected one successful CUE track, count=%d err=%v", count, err)
	}
	var tracks []model.AlbumImportFile
	if err := db.Where("import_id = ? AND role = ?", session.ID, AlbumImportFileRoleAudio).Order("track_number").Find(&tracks).Error; err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, track := range tracks {
		statuses[track.FileName] = track.ProcessingStatus
	}
	if len(tracks) != 2 || statuses["01 - One.mp3"] != "completed" || statuses["02 - Two.mp3"] != AlbumImportFileProcessingStatusFailed {
		t.Fatalf("expected completed and failed CUE tracks, got %#v", tracks)
	}
}

func TestMediaImportProcessorRetriesUploadedCUESourceAfterAllTracksFail(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	cue := model.AlbumImportFile{ImportID: session.ID, RelativePath: "disc.cue", FileName: "disc.cue", Role: AlbumImportFileRoleCue, DetectedFormat: "cue", SourceKey: "source/disc.cue", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending}
	audio := model.AlbumImportFile{ImportID: session.ID, RelativePath: "album.flac", FileName: "album.flac", Role: AlbumImportFileRoleAudio, DetectedFormat: "flac", SourceKey: "source/album.flac", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending}
	for _, file := range []*model.AlbumImportFile{&cue, &audio} {
		if err := db.Create(file).Error; err != nil {
			t.Fatal(err)
		}
	}
	attempt := 1
	ffmpegRuns := 0
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	runner.run = func(name string, args []string) ([]byte, error) {
		if name == "ffprobe" {
			return []byte(`{"format":{"duration":"30"}}`), nil
		}
		if name == "ffmpeg" {
			ffmpegRuns++
			if attempt == 1 {
				return nil, errors.New("transcode failed")
			}
			for _, arg := range args {
				if strings.HasSuffix(arg, ".mp3") {
					return nil, os.WriteFile(arg, []byte("derived"), 0600)
				}
			}
		}
		return nil, nil
	}
	processor := NewMediaImportProcessor(db, &fakeMediaStore{objects: map[string][]byte{
		"source/disc.cue":   []byte("FILE \"album.flac\" WAVE\nTRACK 01 AUDIO\nTITLE \"One\"\nINDEX 01 00:00:00\n"),
		"source/album.flac": []byte("audio"),
	}, puts: map[string][]byte{}}, runner, "")
	job := model.AlbumImportJob{ImportID: session.ID}
	if err := processor.Process(context.Background(), job, nil); err == nil {
		t.Fatal("expected first CUE processing attempt to fail")
	}
	attempt = 2
	if err := processor.Process(context.Background(), job, nil); err != nil {
		t.Fatalf("expected retried CUE source to process: %v", err)
	}
	if ffmpegRuns != 3 {
		t.Fatalf("expected retry to invoke playback transcode and waveform extraction, runs=%d", ffmpegRuns)
	}
	var source model.AlbumImportFile
	if err := db.First(&source, "id = ?", audio.ID).Error; err != nil {
		t.Fatal(err)
	}
	if source.ProcessingStatus != "completed" {
		t.Fatalf("expected source completed after retry, got %#v", source)
	}
	var derivedCount int64
	if err := db.Model(&model.AlbumImportFile{}).Where("import_id = ? AND source_key = ''", session.ID).Count(&derivedCount).Error; err != nil {
		t.Fatal(err)
	}
	if derivedCount != 1 {
		t.Fatalf("CUE retry created duplicate derived records: %d", derivedCount)
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
		"Path = \\absolute\\track.flac\nSize = 1\nPacked Size = 1\n",
		"Path = ..\\track.flac\nSize = 1\nPacked Size = 1\n",
		"Path = C:\\music\\track.flac\nSize = 1\nPacked Size = 1\n",
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

func TestValidateArchiveListingRejectsEncryptedArchive(t *testing.T) {
	listing := "Path = Disc/track.mp3\nSize = 1\nPacked Size = 1\nEncrypted = +\n"
	err := validateArchiveListing([]byte(listing))
	if err == nil || !strings.Contains(err.Error(), "压缩包已加密") {
		t.Fatalf("expected encrypted archive rejection, got %v", err)
	}
}

func TestValidateArchiveListingIgnoresSevenZipArchiveHeader(t *testing.T) {
	listing := `
Path = /tmp/atoman-archive-import-123/source.zip
Type = zip
Physical Size = 1024

----------
Path = Disc 1/01 - First.flac
Size = 100
Packed Size = 80
`

	if err := validateArchiveListing([]byte(listing)); err != nil {
		t.Fatalf("expected 7zz archive header to be ignored, got %v", err)
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

func TestParseAudioProbeReadsTitleTrackDiscAndAlbumArtistTags(t *testing.T) {
	metadata := parseAudioProbe([]byte(`{"format":{"duration":"245.5","tags":{"TITLE":"Tagged title","ALBUM":"Late Registration","ARTIST":"Guest","ALBUM_ARTIST":"Kanye West","TRACKNUMBER":"03/12","DISC":"2/2"}}}`))
	if metadata.duration != 245.5 || metadata.title != "Tagged title" || metadata.album != "Late Registration" || metadata.artist != "Kanye West" || metadata.trackNumber != 3 || metadata.discNumber != 2 {
		t.Fatalf("unexpected audio metadata: %#v", metadata)
	}
}

func TestMergeAudioProbeMetadataUsesFallbackValues(t *testing.T) {
	primary := audioProbeMetadata{title: "Tagged", duration: 10, channels: 2}
	fallback := audioProbeMetadata{title: "Fallback", duration: 20, album: "Album", artist: "Artist", codec: "mp3", sampleRate: 44100}
	merged := mergeAudioProbeMetadata(primary, fallback)
	if merged.title != "Tagged" || merged.duration != 10 || merged.album != "Album" || merged.artist != "Artist" || merged.codec != "mp3" || merged.sampleRate != 44100 {
		t.Fatalf("unexpected merged metadata: %#v", merged)
	}
}

func TestAlbumImportPayloadAlbumTitleFallsBackToCommitRequest(t *testing.T) {
	payload := map[string]any{
		"album":          map[string]any{"title": ""},
		"commit_request": map[string]any{"album": map[string]any{"title": "Section.80"}},
	}
	if got := albumImportPayloadAlbumTitle(payload); got != "Section.80" {
		t.Fatalf("album title = %q", got)
	}
}

func TestPersistDerivedTracksKeepsOnlyMajorityAlbum(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusAnalyzing, Stage: AlbumImportStageAnalyzing, PayloadJSON: `{}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	files := []model.AlbumImportFile{
		{ImportID: session.ID, FileName: "01.mp3", RelativePath: "01.mp3", Role: AlbumImportFileRoleAudio, PlaybackKey: "one", Title: "One", TrackNumber: 1, ProcessingStatus: "completed", MetadataJSON: `{"album":"Late Registration"}`},
		{ImportID: session.ID, FileName: "02.mp3", RelativePath: "02.mp3", Role: AlbumImportFileRoleAudio, PlaybackKey: "two", Title: "Two", TrackNumber: 2, ProcessingStatus: "completed", MetadataJSON: `{"album":" late   registration "}`},
		{ImportID: session.ID, FileName: "03.mp3", RelativePath: "03.mp3", Role: AlbumImportFileRoleAudio, PlaybackKey: "three", Title: "Wrong", TrackNumber: 3, ProcessingStatus: "completed", MetadataJSON: `{"album":"DAMN."}`},
		{ImportID: session.ID, FileName: "04.mp3", RelativePath: "04.mp3", Role: AlbumImportFileRoleAudio, PlaybackKey: "four", Title: "No Tag", TrackNumber: 4, ProcessingStatus: "completed", MetadataJSON: `{}`},
	}
	if err := db.Create(&files).Error; err != nil {
		t.Fatal(err)
	}
	processor := NewMediaImportProcessor(db, &fakeMediaStore{}, &fakeMediaCommandRunner{}, "")
	if err := processor.persistDerivedTracks(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	var reloaded model.AlbumImportSession
	if err := db.First(&reloaded, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(reloaded.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	tracks, _ := payload["derived_tracks"].([]any)
	if len(tracks) != 3 || payload["derived_album_title"] != "Late Registration" {
		t.Fatalf("unexpected derived payload: %#v", payload)
	}
	var ignored model.AlbumImportFile
	if err := db.First(&ignored, "id = ?", files[2].ID).Error; err != nil {
		t.Fatal(err)
	}
	if ignored.ProcessingStatus != "ignored" || ignored.ErrorMessage != "属于其他专辑：DAMN." {
		t.Fatalf("unexpected ignored file: %#v", ignored)
	}
}

func TestAlbumImportTrackInfoFromFileNameIsConservative(t *testing.T) {
	tests := []struct {
		name      string
		wantTitle string
		wantDisc  int
		wantTrack int
	}{
		{name: "01 - Intro.flac", wantTitle: "Intro", wantTrack: 1},
		{name: "2-01 Main Theme.flac", wantTitle: "Main Theme", wantDisc: 2, wantTrack: 1},
		{name: "03. Outro.flac", wantTitle: "Outro", wantTrack: 3},
		{name: "01 Hidden Track.flac", wantTitle: "Hidden Track", wantTrack: 1},
		{name: "03. A.D.H.D.mp3", wantTitle: "A.D.H.D", wantTrack: 3},
		{name: "99 Problems.flac", wantTitle: "99 Problems", wantTrack: 99},
		{name: "1979.flac", wantTitle: "1979"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			title, disc, track := albumImportTrackInfoFromFileName(test.name)
			if title != test.wantTitle || disc != test.wantDisc || track != test.wantTrack {
				t.Fatalf("track info = %q/%d/%d, want %q/%d/%d", title, disc, track, test.wantTitle, test.wantDisc, test.wantTrack)
			}
		})
	}
}

func TestTitleFromFileNameForTrackUsesArchiveSequenceForUnpaddedNumbers(t *testing.T) {
	if got := titleFromFileNameForTrack("10 Song.mp3", 10); got != "Song" {
		t.Fatalf("expected matching sequence prefix to be removed, got %q", got)
	}
	if got := titleFromFileNameForTrack("99 Problems.mp3", 1); got != "99 Problems" {
		t.Fatalf("expected unmatched numeric title to be preserved, got %q", got)
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
	if len(runner.runs) != 4 || runner.runs[0][0] != "ffprobe" || runner.runs[1][0] != "ffmpeg" || !containsMediaArg(runner.runs[1], "320k") || runner.runs[2][0] != "ffprobe" || runner.runs[3][0] != "ffmpeg" {
		t.Fatalf("expected source probe, 320k transcode, playback probe, then waveform extraction, got %#v", runner.runs)
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
	if err := processor.Process(context.Background(), job, func() error { return nil }); err != nil {
		t.Fatalf("completed uploaded audio must be an idempotent no-op: %v", err)
	}
	ffmpegs := 0
	for _, run := range runner.runs {
		if run[0] == "ffmpeg" {
			ffmpegs++
		}
	}
	if ffmpegs != 2 || len(store.puts) != 1 {
		t.Fatalf("completed uploaded audio was reprocessed: ffmpegs=%d objects=%d", ffmpegs, len(store.puts))
	}
}

func TestMediaImportProcessorProcessesUploadedCoverAndPersistsTracks(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	audio := model.AlbumImportFile{ImportID: session.ID, RelativePath: "01 - Intro.flac", FileName: "01 - Intro.flac", Role: AlbumImportFileRoleAudio, DetectedFormat: "flac", SourceKey: "source/intro.flac", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending}
	cover := model.AlbumImportFile{ImportID: session.ID, RelativePath: "cover.png", FileName: "cover.png", Role: AlbumImportFileRoleCover, DetectedFormat: "png", SourceKey: "source/cover.png", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending}
	if err := db.Create(&audio).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cover).Error; err != nil {
		t.Fatal(err)
	}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	store := &fakeMediaStore{objects: map[string][]byte{audio.SourceKey: []byte("audio"), cover.SourceKey: []byte("cover")}, puts: map[string][]byte{}}
	processor := NewMediaImportProcessor(db, store, runner, "https://assets.example.com")

	if err := processor.Process(context.Background(), model.AlbumImportJob{ImportID: session.ID}, nil); err != nil {
		t.Fatal(err)
	}

	var stored model.AlbumImportSession
	if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.PayloadJSON, "cover_key") || !strings.Contains(stored.PayloadJSON, "derived_tracks") {
		t.Fatalf("expected cover and tracks in session payload, got %s", stored.PayloadJSON)
	}
	var storedCover model.AlbumImportFile
	if err := db.First(&storedCover, "id = ?", cover.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedCover.ProcessingStatus != "completed" {
		t.Fatalf("expected processed cover, got %#v", storedCover)
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
