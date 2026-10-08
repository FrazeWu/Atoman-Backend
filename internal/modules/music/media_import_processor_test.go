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

func TestLyricsPayloadFromFileAcceptsNonnegativeJSONCredits(t *testing.T) {
	raw := "{\"t\":0,\"c\":[{\"tx\":\"作词: \"},{\"tx\":\"张智\"}]}\n{\"t\":1000,\"c\":[{\"tx\":\"作曲: 张智\"}]}\n[00:52.00]孩子啊你要去哪里"
	payload := lyricsPayloadFromFile("张智 - 巴克图口岸.lrc", []byte(raw))
	if err := validateImportedLyrics("巴克图口岸.lrc", payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Content, "[by:作词: 张智]") || payload.Format != "lrc" {
		t.Fatalf("unexpected lyrics: %#v", payload)
	}
}

func TestLyricsPayloadFromFileKeepsUntimedInstrumentCreditsAsPlain(t *testing.T) {
	raw := "{\"c\":[{\"tx\":\"作曲: \"},{\"tx\":\"张智\"}]}\n笛子：周昇\n吉他&冬不拉：叶尔波利\n键盘&人声：张智"
	payload := lyricsPayloadFromFile("张智 - 阿尔金山.lrc", []byte(raw))
	if payload.Format != "plain" || payload.Content != "作曲: 张智\n笛子：周昇\n吉他&冬不拉：叶尔波利\n键盘&人声：张智" {
		t.Fatalf("unexpected lyrics: %#v", payload)
	}
	if err := validateImportedLyrics("阿尔金山.lrc", payload); err != nil {
		t.Fatal(err)
	}
}

func TestLyricsPayloadFromFileNormalizesJSONCredits(t *testing.T) {
	raw := "{\"t\":-1000,\"c\":[{\"tx\":\"作词: \"},{\"tx\":\"梁弈源\"}]}\n{\"t\":-500,\"c\":[{\"tx\":\"作曲: \"},{\"tx\":\"张智\"}]}\n[00:00.00]\n[00:17.27]依奇克里克"
	payload := lyricsPayloadFromFile("张智 - 依奇克里克.lrc", []byte(raw))
	want := "[by:作词: 梁弈源]\n[by:作曲: 张智]\n[00:00.00]\n[00:17.27]依奇克里克"
	if payload.Content != want {
		t.Fatalf("content = %q, want %q", payload.Content, want)
	}
	lines, err := ParseLyricLines(payload.Content, "", payload.Format)
	if err != nil || len(lines) != 1 || *lines[0].TimeMS != 17270 {
		t.Fatalf("unexpected parsed lyrics: %#v, %v", lines, err)
	}
	lyrics := map[string]AlbumImportTrackLyricsPayload{}
	mergeLocalLyrics(lyrics, normalizedLyricName("张智 - 依奇克里克.lrc"), payload)
	if len(lyrics) != 1 {
		t.Fatal("normalized lyrics were discarded")
	}
}

func TestLoadUploadedLyricsReportsInvalidLRC(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	file := model.AlbumImportFile{ImportID: session.ID, FileName: "broken.lrc", RelativePath: "broken.lrc", Role: AlbumImportFileRoleLyrics, SourceKey: "lyrics", UploadStatus: AlbumImportFileUploadStatusUploaded}
	if err := db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	store := &fakeMediaStore{objects: map[string][]byte{"lyrics": []byte("invalid\n[00:01.00]lyrics")}}
	lyrics := NewMediaImportProcessor(db, store, nil, "").loadUploadedLyrics(context.Background(), session.ID)
	if err := db.First(&file, "id = ?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(lyrics) != 0 || file.ProcessingStatus != AlbumImportFileProcessingStatusFailed || !strings.Contains(file.ErrorMessage, "broken.lrc") || !strings.Contains(file.ErrorMessage, "timestamp") {
		t.Fatalf("unexpected lyrics or file status: %#v, %#v", lyrics, file)
	}
}

func TestLyricsPayloadFromFileKeepsStandardAndInvalidContent(t *testing.T) {
	for _, content := range []string{
		"[ar:Artist]\n[00:01.00]lyrics",
		"{invalid json}\n[00:01.00]lyrics",
		"{\"t\":0,\"c\":[{\"tx\":\"lyrics\"}]}\n[00:01.00]lyrics",
		"{\"t\":-1000,\"c\":[]}\n[00:01.00]lyrics",
	} {
		payload := lyricsPayloadFromFile("song.lrc", []byte(content))
		if payload.Content != content {
			t.Fatalf("content changed: %q", payload.Content)
		}
		err := validateImportedLyrics("song.lrc", payload)
		if strings.HasPrefix(content, "{") && err == nil {
			t.Fatalf("invalid JSON lyric accepted: %q", content)
		}
		if strings.HasPrefix(content, "[") && err != nil {
			t.Fatal(err)
		}
	}
}

func TestProcessExtractedTreeReportsInvalidLyrics(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for name, content := range map[string]string{"song.flac": "audio", "song.lrc": "invalid lyrics"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store := &fakeMediaStore{objects: map[string][]byte{}, puts: map[string][]byte{}}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	err := NewMediaImportProcessor(db, store, runner, "").processExtractedTree(context.Background(), session.ID, root, nil)
	var attention *importNeedsAttentionError
	if !errors.As(err, &attention) || !strings.Contains(err.Error(), "song.lrc") || !strings.Contains(err.Error(), "timestamp") {
		t.Fatalf("expected actionable lyric error, got %v", err)
	}
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
	var processedArchive model.AlbumImportFile
	if err := db.First(&processedArchive, "id = ?", archive.ID).Error; err != nil {
		t.Fatal(err)
	}
	if processedArchive.ProcessingStatus != AlbumImportFileProcessingStatusCompleted {
		t.Fatalf("archive processing status = %q, want completed", processedArchive.ProcessingStatus)
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

func TestProcessExtractedTreePrefersTimedLyricsOverDuplicatePlainLyrics(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for name, content := range map[string]string{
		"01 - First.flac": "audio",
		"01 - First.lrc":  "{\"t\":-1000,\"c\":[{\"tx\":\"作词: 梁弈源\"}]}\n[00:01.00]timed lyrics",
		"01 - First.txt":  "plain lyrics",
		"cover.jpg":       "cover",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	store := &fakeMediaStore{objects: map[string][]byte{}, puts: map[string][]byte{}}
	if err := NewMediaImportProcessor(db, store, runner, "").processExtractedTree(context.Background(), session.ID, root, nil); err != nil {
		t.Fatal(err)
	}

	var stored model.AlbumImportSession
	if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stored.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	tracks, ok := payload["derived_tracks"].([]any)
	if !ok || len(tracks) != 1 {
		t.Fatalf("expected one derived track, got %#v", payload["derived_tracks"])
	}
	track, ok := tracks[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected derived track: %#v", tracks[0])
	}
	lyrics, ok := track["lyrics"].(map[string]any)
	if !ok || lyrics["format"] != "lrc" || lyrics["content"] != "[by:作词: 梁弈源]\n[00:01.00]timed lyrics" {
		t.Fatalf("expected timed local lyrics, got %#v", track["lyrics"])
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
	var attention *importNeedsAttentionError
	if err := processor.Process(context.Background(), job, nil); !errors.As(err, &attention) {
		t.Fatalf("expected actionable partial failure before retry, got %v", err)
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

func TestAlbumImportArchiveTitleRemovesArtistAndFormatSuffix(t *testing.T) {
	if got := albumImportArchiveTitle("郭顶 - 飞行器的执行周期[FLAC].rar", "郭顶"); got != "飞行器的执行周期" {
		t.Fatalf("archive title = %q", got)
	}
}

func TestAlbumImportDirectoryTitleUsesTopLevelAlbumFolder(t *testing.T) {
	files := []model.AlbumImportFile{
		{RelativePath: "菊花夜行军/交工乐队 - 两代人.mp3"},
		{RelativePath: "菊花夜行军/交工乐队 - 县道184.mp3"},
	}
	if got := albumImportDirectoryTitle(files, "交工乐队"); got != "菊花夜行军" {
		t.Fatalf("directory title = %q", got)
	}
}

func TestMediaImportProcessorDoesNotRegressCanceledSession(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{
		Status:      AlbumImportStatusCanceled,
		Stage:       AlbumImportStageCanceled,
		PayloadJSON: "{}",
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}

	processor := NewMediaImportProcessor(db, &fakeMediaStore{}, &fakeMediaCommandRunner{}, "")
	err := processor.setSession(
		context.Background(), session.ID, AlbumImportStatusExtracting,
		AlbumImportStageExtracting, 1, 1,
	)
	if err == nil {
		t.Fatal("expected progress update to reject canceled session")
	}

	var stored model.AlbumImportSession
	if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != AlbumImportStatusCanceled || stored.Stage != AlbumImportStageCanceled {
		t.Fatalf("canceled session was regressed: %#v", stored)
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

func TestPersistDerivedTracksUsesArchiveNameWhenAudioHasNoAlbumTag(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{
		Status:      AlbumImportStatusAnalyzing,
		Stage:       AlbumImportStageAnalyzing,
		PayloadJSON: `{"archive_name":"郭顶 - 飞行器的执行周期[FLAC].rar","artist_name":"郭顶"}`,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	file := model.AlbumImportFile{
		ImportID:         session.ID,
		FileName:         "01 - 凄美地.flac",
		RelativePath:     "01 - 凄美地.flac",
		Role:             AlbumImportFileRoleAudio,
		PlaybackKey:      "audio/01",
		Title:            "凄美地",
		TrackNumber:      1,
		ProcessingStatus: AlbumImportFileProcessingStatusCompleted,
		MetadataJSON:     `{}`,
	}
	if err := db.Create(&file).Error; err != nil {
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
	if payload["derived_album_title"] != "飞行器的执行周期" {
		t.Fatalf("derived album title = %#v", payload["derived_album_title"])
	}
}

func TestPersistDerivedTracksUsesTopLevelDirectoryWhenAudioHasNoAlbumTag(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{
		Status:      AlbumImportStatusAnalyzing,
		Stage:       AlbumImportStageAnalyzing,
		PayloadJSON: `{"artist_name":"交工乐队"}`,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	file := model.AlbumImportFile{
		ImportID:         session.ID,
		FileName:         "交工乐队 - 两代人.mp3",
		RelativePath:     "菊花夜行军/交工乐队 - 两代人.mp3",
		Role:             AlbumImportFileRoleAudio,
		PlaybackKey:      "audio/one",
		Title:            "两代人",
		TrackNumber:      1,
		ProcessingStatus: AlbumImportFileProcessingStatusCompleted,
		MetadataJSON:     `{}`,
	}
	if err := db.Create(&file).Error; err != nil {
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
	if payload["derived_album_title"] != "菊花夜行军" {
		t.Fatalf("derived album title = %#v", payload["derived_album_title"])
	}
}

func TestPersistDerivedMetadataResultPreservesLatestCommitRequest(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{
		Status:      AlbumImportStatusAnalyzing,
		Stage:       AlbumImportStageAnalyzing,
		PayloadJSON: `{"derived_album_type":"single","derived_release_date":"2020-01-01","derived_cover":"https://old.test/cover.jpg"}`,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	latestPayload := `{"commit_request":{"album":{"title":"用户改名","tracks":[{"title":"用户曲目","track_number":1}]}},"derived_album_type":"single","derived_release_date":"2020-01-01","derived_cover":"https://old.test/cover.jpg"}`
	if err := db.Model(&session).Update("payload_json", latestPayload).Error; err != nil {
		t.Fatal(err)
	}
	processor := NewMediaImportProcessor(db, &fakeMediaStore{}, &fakeMediaCommandRunner{}, "")
	if err := processor.persistDerivedMetadataResult(context.Background(), &session, nil, AlbumImportMetadataResult{
		Tracks: []AlbumImportDTOTrack{{Title: "Matched track", TrackNumber: 1, MatchStatus: "matched"}},
	}); err != nil {
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
	request, ok := payload["commit_request"].(map[string]any)
	if !ok || request["album"].(map[string]any)["title"] != "用户改名" {
		t.Fatalf("latest commit request was overwritten: %#v", payload)
	}
	if _, ok := payload["derived_album_type"]; ok {
		t.Fatalf("stale album type was not cleared: %#v", payload)
	}
	if _, ok := payload["derived_release_date"]; ok {
		t.Fatalf("stale release date was not cleared: %#v", payload)
	}
	if _, ok := payload["derived_cover"]; ok {
		t.Fatalf("stale derived cover was not cleared: %#v", payload)
	}
}

func TestMergeDerivedMetadataPayloadPreservesLockedMetadata(t *testing.T) {
	payload := map[string]any{
		"metadata_match_locked": true,
		"metadata_source":       "discogs",
		"metadata_genres":       []any{"Electronic"},
		"metadata_styles":       []any{"Ambient"},
		"derived_cover":         "https://cover.test/matched.jpg",
		"derived_tracks":        []any{map[string]any{"title": "Matched title", "match_status": model.MusicMatchMatched}},
	}

	mergeDerivedMetadataPayload(payload, []map[string]any{{"title": "Song"}}, AlbumImportMetadataResult{
		MatchStatus:   model.MusicMatchUnmatched,
		MetadataError: "worker fallback",
	})

	if payload["metadata_source"] != "discogs" || payload["derived_cover"] != "https://cover.test/matched.jpg" {
		t.Fatalf("locked metadata was overwritten: %#v", payload)
	}
	if payload["metadata_genres"].([]any)[0] != "Electronic" || payload["metadata_styles"].([]any)[0] != "Ambient" {
		t.Fatalf("locked tags were overwritten: %#v", payload)
	}
	tracks := payload["derived_tracks"].([]any)
	if tracks[0].(map[string]any)["title"] != "Matched title" {
		t.Fatalf("locked derived tracks were overwritten: %#v", payload)
	}
}

func TestAlbumImportProcessingInfersArtistFromTrackFilenamePrefix(t *testing.T) {
	if got := inferCommonAlbumImportArtist([]string{
		"交工乐队 - 两代人",
		"交工乐队 - 愁上愁下",
		"交工乐队 - 风神125",
	}); got != "交工乐队" {
		t.Fatalf("inferred artist = %q, want 交工乐队", got)
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
		{name: "04 - Live.mov", wantTitle: "Live", wantTrack: 4},
		{name: "05 - Session.avi", wantTitle: "Session", wantTrack: 5},
		{name: "06 - Demo.m4v", wantTitle: "Demo", wantTrack: 6},
		{name: "07 - Broadcast.mpg", wantTitle: "Broadcast", wantTrack: 7},
		{name: "08 - Concert.mpeg", wantTitle: "Concert", wantTrack: 8},
		{name: "09 - Tape.ts", wantTitle: "Tape", wantTrack: 9},
		{name: "10 - Clip.3gp", wantTitle: "Clip", wantTrack: 10},
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
	if got := normalizeAlbumImportTrackTitle("Kendrick Lamar,SZA - Loved Ones", "Kendrick Lamar"); got != "Loved Ones" {
		t.Fatalf("normalized collaborator title = %q", got)
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

func TestNormalizeAlbumImportTrackTitleRemovesKnownArtistPrefixOrSuffix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "artist prefix", input: "Artist - Song", want: "Song"},
		{name: "artist suffix", input: "Song - Artist", want: "Song"},
		{name: "unicode dash", input: "Artist – Song - Remix", want: "Song - Remix"},
		{name: "unknown artist", input: "Unknown - Song", want: "Unknown - Song"},
		{name: "hyphenated title", input: "Run - DMC", want: "Run - DMC"},
		{name: "hyphenated collaborator", input: "Artist,Ab-Soul - Welcome to C4", want: "Welcome to C4"},
		{name: "hyphenated subtitle", input: "Artist - The Real Hip Hop (Ab-Soul Freestyle)", want: "The Real Hip Hop (Ab-Soul Freestyle)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeAlbumImportTrackTitle(test.input, "Artist"); got != test.want {
				t.Fatalf("normalized title = %q, want %q", got, test.want)
			}
		})
	}
	if got := normalizeAlbumImportTrackTitle("Kendrick Lamar,SZA - Loved Ones", "Kendrick Lamar"); got != "Loved Ones" {
		t.Fatalf("normalized collaborator title = %q", got)
	}
}
func TestMediaImportProcessorTranscodesUploadedVideoAudioStreamAndUpdatesFile(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	file := model.AlbumImportFile{ImportID: session.ID, RelativePath: "Disc 1/01 - First.mp4", FileName: "01 - First.mp4", Role: AlbumImportFileRoleAudio, DetectedFormat: "mp4", SourceKey: "source/first.mp4", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending}
	if err := db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	job := model.AlbumImportJob{ImportID: session.ID}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe", "7zz": "/bin/7zz"}}
	store := &fakeMediaStore{objects: map[string][]byte{file.SourceKey: []byte("video")}, puts: map[string][]byte{}}
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
	if len(runner.runs) != 4 || runner.runs[0][0] != "ffprobe" || !containsMediaArg(runner.runs[0], "a:0") || runner.runs[1][0] != "ffmpeg" || !containsMediaArg(runner.runs[1], "0:a:0") || !containsMediaArg(runner.runs[1], "320k") || runner.runs[2][0] != "ffprobe" || runner.runs[3][0] != "ffmpeg" {
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

func TestMediaImportProcessorTranscodesMultipleVideoSourcesIntoMultipleTracks(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{Status: AlbumImportStatusQueued, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	files := []model.AlbumImportFile{
		{ImportID: session.ID, RelativePath: "01 - Intro.mp4", FileName: "01 - Intro.mp4", Role: AlbumImportFileRoleAudio, DetectedFormat: "mp4", SourceKey: "source/intro.mp4", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending},
		{ImportID: session.ID, RelativePath: "02 - Song.mkv", FileName: "02 - Song.mkv", Role: AlbumImportFileRoleAudio, DetectedFormat: "mkv", SourceKey: "source/song.mkv", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending},
	}
	for index := range files {
		if err := db.Create(&files[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeMediaCommandRunner{paths: map[string]string{"ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}}
	store := &fakeMediaStore{objects: map[string][]byte{
		"source/intro.mp4": []byte("video-1"),
		"source/song.mkv":  []byte("video-2"),
	}, puts: map[string][]byte{}}
	processor := NewMediaImportProcessor(db, store, runner, "")

	if err := processor.Process(context.Background(), model.AlbumImportJob{ImportID: session.ID}, nil); err != nil {
		t.Fatal(err)
	}
	var processed []model.AlbumImportFile
	if err := db.Where("import_id = ? AND role = ?", session.ID, AlbumImportFileRoleAudio).Order("relative_path").Find(&processed).Error; err != nil {
		t.Fatal(err)
	}
	if len(processed) != 2 {
		t.Fatalf("expected two processed video tracks, got %#v", processed)
	}
	for _, file := range processed {
		if file.ProcessingStatus != "completed" || file.PlaybackKey == "" {
			t.Fatalf("video source was not converted to playback audio: %#v", file)
		}
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

func TestMediaImportProcessorUsesCommittedArtistForMetadataMatching(t *testing.T) {
	processor := &MediaImportProcessor{}
	payload := map[string]any{
		"commit_request": map[string]any{
			"artist": map[string]any{"name": "Tyler, The Creator"},
		},
	}

	if got := processor.albumImportArtistName(context.Background(), payload); got != "Tyler, The Creator" {
		t.Fatalf("expected committed artist name, got %q", got)
	}
}
