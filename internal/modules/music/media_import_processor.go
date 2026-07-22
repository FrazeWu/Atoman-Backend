package music

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"atoman/internal/model"

	"github.com/google/uuid"
	"golang.org/x/text/encoding/simplifiedchinese"
	"gorm.io/gorm"
)

const (
	mediaArchiveMaxBytes   int64 = 30 * 1024 * 1024 * 1024
	mediaArchiveMaxEntries       = 5000
	mediaArchiveMaxRatio   int64 = 100
)

// MediaCommandRunner isolates external binaries so processing remains unit-testable.
type MediaCommandRunner interface {
	LookPath(string) (string, error)
	Run(context.Context, string, ...string) ([]byte, error)
}

type systemMediaCommandRunner struct{}

func NewSystemMediaCommandRunner() MediaCommandRunner { return systemMediaCommandRunner{} }

func (systemMediaCommandRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }
func (systemMediaCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type errMissingMediaTool struct{ name string }

func (e errMissingMediaTool) Error() string { return "required media tool is unavailable: " + e.name }

// ValidateMediaToolchain ensures the worker never claims jobs it cannot process.
func ValidateMediaToolchain(runner MediaCommandRunner) error {
	if runner == nil {
		return errors.New("media command runner is required")
	}
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := runner.LookPath(tool); err != nil {
			return errMissingMediaTool{name: tool}
		}
	}
	return nil
}

func ValidateArchiveToolchain(runner MediaCommandRunner) error {
	if runner == nil {
		return errors.New("media command runner is required")
	}
	if _, err := runner.LookPath("7zz"); err != nil {
		return errMissingMediaTool{name: "7zz"}
	}
	return nil
}

type archiveListEntry struct {
	path       string
	size       int64
	packedSize int64
	attributes string
}

// validateArchiveListing accepts only ordinary, bounded relative archive entries.
func validateArchiveListing(raw []byte) error {
	entries := []archiveListEntry{}
	entry := archiveListEntry{}
	flush := func() error {
		if entry.path == "" {
			return nil
		}
		if filepath.IsAbs(entry.path) || strings.HasPrefix(entry.path, `\\`) || regexp.MustCompile(`^[A-Za-z]:`).MatchString(entry.path) {
			return fmt.Errorf("unsafe archive path %q", entry.path)
		}
		clean := filepath.Clean(strings.ReplaceAll(entry.path, `\\`, "/"))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
			return fmt.Errorf("unsafe archive path %q", entry.path)
		}
		attrs := strings.ToLower(entry.attributes)
		if strings.Contains(attrs, "l") || strings.Contains(attrs, "symlink") || strings.Contains(attrs, "reparse") {
			return fmt.Errorf("unsafe archive entry %q", entry.path)
		}
		entries = append(entries, entry)
		entry = archiveListEntry{}
		return nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		key, value, ok := strings.Cut(line, " = ")
		if !ok {
			continue
		}
		switch key {
		case "Path":
			entry.path = strings.TrimSpace(value)
		case "Size":
			entry.size, _ = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		case "Packed Size":
			entry.packedSize, _ = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		case "Attributes":
			entry.attributes = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	var total int64
	for _, item := range entries {
		if len(entries) > mediaArchiveMaxEntries {
			return errors.New("archive has too many entries")
		}
		if item.size < 0 || item.packedSize < 0 {
			return errors.New("invalid archive size")
		}
		if item.packedSize == 0 && item.size > 0 || item.packedSize > 0 && item.size > item.packedSize*mediaArchiveMaxRatio {
			return fmt.Errorf("archive compression ratio is too high for %q", item.path)
		}
		total += item.size
		if total > mediaArchiveMaxBytes {
			return errors.New("archive expands beyond size limit")
		}
	}
	return nil
}

type cueTrack struct {
	file         string
	number       int
	title        string
	startSeconds float64
}

func parseCUE(raw []byte) ([]cueTrack, error) {
	if !utf8.Valid(raw) {
		decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
		if err != nil {
			return nil, fmt.Errorf("decode CUE: %w", err)
		}
		raw = decoded
	}
	var file string
	tracks := []cueTrack{}
	var current *cueTrack
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		keyword, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch strings.ToUpper(keyword) {
		case "FILE":
			file = cueValue(rest)
		case "TRACK":
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}
			number, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			tracks = append(tracks, cueTrack{file: file, number: number})
			current = &tracks[len(tracks)-1]
		case "TITLE":
			if current != nil {
				current.title = cueValue(rest)
			}
		case "INDEX":
			fields := strings.Fields(rest)
			if current == nil || len(fields) != 2 || fields[0] != "01" {
				continue
			}
			parts := strings.Split(fields[1], ":")
			if len(parts) != 3 {
				continue
			}
			minutes, e1 := strconv.Atoi(parts[0])
			seconds, e2 := strconv.Atoi(parts[1])
			frames, e3 := strconv.Atoi(parts[2])
			if e1 == nil && e2 == nil && e3 == nil && seconds < 60 && frames < 75 {
				current.startSeconds = float64(minutes*60+seconds) + float64(frames)/75
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	valid := tracks[:0]
	for _, track := range tracks {
		if track.file != "" {
			valid = append(valid, track)
		}
	}
	if len(valid) == 0 {
		return nil, errors.New("CUE has no playable tracks")
	}
	return valid, nil
}

func cueValue(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "\"") {
		if end := strings.Index(value[1:], "\""); end >= 0 {
			return value[1 : end+1]
		}
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

type mediaImportStore interface {
	OpenObject(string) (io.ReadCloser, error)
	PutObject(string, string, io.Reader) error
}

// MediaImportProcessor turns uploaded source objects into isolated playback objects.
type MediaImportProcessor struct {
	db        *gorm.DB
	store     mediaImportStore
	runner    MediaCommandRunner
	urlPrefix string
}

func NewMediaImportProcessor(db *gorm.DB, store mediaImportStore, runner MediaCommandRunner, playbackURLPrefix string) *MediaImportProcessor {
	return &MediaImportProcessor{db: db, store: store, runner: runner, urlPrefix: strings.TrimRight(strings.TrimSpace(playbackURLPrefix), "/")}
}

func (p *MediaImportProcessor) Process(ctx context.Context, job model.AlbumImportJob, heartbeat func() error) error {
	if p == nil || p.db == nil || p.store == nil {
		return errors.New("media import processor dependencies are required")
	}
	var session model.AlbumImportSession
	if err := p.db.WithContext(ctx).Preload("Files").First(&session, "id = ?", job.ImportID).Error; err != nil {
		return err
	}
	if err := ValidateMediaToolchain(p.runner); err != nil {
		return err
	}
	for _, file := range session.Files {
		if file.Role == AlbumImportFileRoleArchive && file.UploadStatus == AlbumImportFileUploadStatusUploaded {
			if err := ValidateArchiveToolchain(p.runner); err != nil {
				return err
			}
			return p.processArchive(ctx, session, file, heartbeat)
		}
	}
	return p.processUploadedFiles(ctx, session, heartbeat)
}

func (p *MediaImportProcessor) processUploadedFiles(ctx context.Context, session model.AlbumImportSession, heartbeat func() error) error {
	files := make([]model.AlbumImportFile, 0)
	cues := make([]model.AlbumImportFile, 0)
	for _, file := range session.Files {
		if file.Role == AlbumImportFileRoleAudio && file.UploadStatus == AlbumImportFileUploadStatusUploaded && file.ProcessingStatus != "completed" {
			files = append(files, file)
		}
		if file.Role == AlbumImportFileRoleCue && file.UploadStatus == AlbumImportFileUploadStatusUploaded {
			cues = append(cues, file)
		}
	}
	if len(files) == 0 {
		return errors.New("no uploaded audio files to process")
	}
	successes := 0
	used, cueSuccesses, cueTracks, err := p.processUploadedCUESources(ctx, session.ID, files, cues)
	if err != nil {
		return err
	}
	total := int64(len(files) - len(used) + cueTracks)
	if err := p.setSession(ctx, session.ID, AlbumImportStatusAnalyzing, AlbumImportStageAnalyzing, 0, total); err != nil {
		return err
	}
	successes += cueSuccesses
	for index := range files {
		if used[files[index].ID] {
			continue
		}
		if heartbeat != nil {
			if err := heartbeat(); err != nil {
				return err
			}
		}
		if err := p.processAudio(ctx, session.ID, &files[index]); err != nil {
			_ = p.failFile(ctx, files[index].ID, err)
			continue
		}
		successes++
		if err := p.setSession(ctx, session.ID, AlbumImportStatusTranscoding, AlbumImportStageTranscoding, int64(successes), total); err != nil {
			return err
		}
	}
	if successes == 0 {
		return errors.New("no audio tracks were processed successfully")
	}
	return p.setSession(ctx, session.ID, AlbumImportStatusTranscoding, AlbumImportStageTranscoding, int64(successes), total)
}

func (p *MediaImportProcessor) processUploadedCUESources(ctx context.Context, sessionID uuid.UUID, audios, cues []model.AlbumImportFile) (map[uuid.UUID]bool, int, int, error) {
	used := map[uuid.UUID]bool{}
	successes := 0
	trackCount := 0
	if len(audios) == 0 || len(cues) == 0 {
		return used, successes, trackCount, nil
	}
	dir, err := os.MkdirTemp("", "atoman-cue-import-*")
	if err != nil {
		return nil, 0, 0, err
	}
	defer os.RemoveAll(dir)
	for _, cue := range cues {
		cuePath := filepath.Join(dir, "cues", filepath.FromSlash(cue.RelativePath))
		if err := os.MkdirAll(filepath.Dir(cuePath), 0700); err != nil {
			return nil, 0, 0, err
		}
		if err := p.downloadSource(cuePath, cue.SourceKey); err != nil {
			continue
		}
		raw, err := os.ReadFile(cuePath)
		if err != nil {
			continue
		}
		tracks, err := parseCUE(raw)
		if err != nil {
			continue
		}
		for index := range audios {
			matching := cueTracksForAudio(cue.RelativePath, audios[index].RelativePath, tracks)
			if len(matching) == 0 || used[audios[index].ID] {
				continue
			}
			sourcePath := filepath.Join(dir, "audio", filepath.FromSlash(audios[index].RelativePath))
			if err := os.MkdirAll(filepath.Dir(sourcePath), 0700); err != nil {
				return nil, 0, 0, err
			}
			if err := p.downloadSource(sourcePath, audios[index].SourceKey); err != nil {
				continue
			}
			used[audios[index].ID] = true
			trackCount += len(matching)
			count, _ := p.processCUEAudio(ctx, sessionID, sourcePath, audios[index].RelativePath, matching)
			successes += count
			if err := p.db.WithContext(ctx).Model(&model.AlbumImportFile{}).Where("id = ?", audios[index].ID).Updates(map[string]any{"processing_status": "completed", "error_message": ""}).Error; err != nil {
				return nil, 0, 0, err
			}
		}
	}
	return used, successes, trackCount, nil
}

func (p *MediaImportProcessor) processArchive(ctx context.Context, session model.AlbumImportSession, archive model.AlbumImportFile, heartbeat func() error) error {
	dir, err := os.MkdirTemp("", "atoman-archive-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	archivePath := filepath.Join(dir, "source."+safeMediaExtension(archive.DetectedFormat))
	if err := p.downloadSource(archivePath, archive.SourceKey); err != nil {
		return err
	}
	if err := p.setSession(ctx, session.ID, AlbumImportStatusExtracting, AlbumImportStageExtracting, 0, 1); err != nil {
		return err
	}
	listing, err := p.runner.Run(ctx, "7zz", "l", "-slt", archivePath)
	if err != nil {
		return fmt.Errorf("list archive %s: %w", archive.FileName, err)
	}
	if err := validateArchiveListing(listing); err != nil {
		return err
	}
	extracted := filepath.Join(dir, "extracted")
	if _, err := p.runner.Run(ctx, "7zz", "x", "-y", "-o"+extracted, archivePath); err != nil {
		return fmt.Errorf("extract archive %s: %w", archive.FileName, err)
	}
	return p.processExtractedTree(ctx, session.ID, extracted, heartbeat)
}

func (p *MediaImportProcessor) processExtractedTree(ctx context.Context, sessionID uuid.UUID, root string, heartbeat func() error) error {
	type localAudio struct{ path, relative string }
	audios := []localAudio{}
	cues := []string{}
	cover := ""
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe extracted symlink %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		role, _, err := detectAlbumImportFileRole(filepath.Base(path))
		if err != nil {
			return nil
		}
		switch role {
		case AlbumImportFileRoleAudio:
			audios = append(audios, localAudio{path, relative})
		case AlbumImportFileRoleCue:
			cues = append(cues, path)
		case AlbumImportFileRoleCover:
			if cover == "" {
				cover = path
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if cover != "" {
		_ = p.processCover(ctx, sessionID, cover)
	}
	processed := 0
	used := map[string]bool{}
	for _, cuePath := range cues {
		raw, err := os.ReadFile(cuePath)
		if err != nil {
			continue
		}
		tracks, err := parseCUE(raw)
		if err != nil {
			continue
		}
		cueRelative, err := filepath.Rel(root, cuePath)
		if err != nil {
			return err
		}
		for _, audio := range audios {
			matching := cueTracksForAudio(filepath.ToSlash(cueRelative), filepath.ToSlash(audio.relative), tracks)
			if len(matching) == 0 || used[audio.path] {
				continue
			}
			used[audio.path] = true
			count, _ := p.processCUEAudio(ctx, sessionID, audio.path, audio.relative, matching)
			processed += count
		}
	}
	for _, audio := range audios {
		if used[audio.path] {
			continue
		}
		if heartbeat != nil {
			if err := heartbeat(); err != nil {
				return err
			}
		}
		file := model.AlbumImportFile{ImportID: sessionID, RelativePath: audio.relative, FileName: filepath.Base(audio.path), Role: AlbumImportFileRoleAudio, DetectedFormat: strings.TrimPrefix(strings.ToLower(filepath.Ext(audio.path)), "."), UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending}
		if err := p.db.WithContext(ctx).Create(&file).Error; err != nil {
			return err
		}
		if err := p.processLocalAudio(ctx, sessionID, &file, audio.path, "", 0, 0); err != nil {
			_ = p.failFile(ctx, file.ID, err)
			continue
		}
		processed++
	}
	if processed == 0 {
		return errors.New("no audio tracks were processed successfully")
	}
	return p.setSession(ctx, sessionID, AlbumImportStatusTranscoding, AlbumImportStageTranscoding, int64(processed), int64(processed))
}

func cueTracksForAudio(cueRelativePath, audioRelativePath string, tracks []cueTrack) []cueTrack {
	cueDirectory := path.Dir(path.Clean(strings.ReplaceAll(cueRelativePath, `\`, "/")))
	audioPath := path.Clean(strings.ReplaceAll(audioRelativePath, `\`, "/"))
	matching := make([]cueTrack, 0, len(tracks))
	for _, track := range tracks {
		cueFile := strings.ReplaceAll(track.file, `\`, "/")
		if path.Clean(path.Join(cueDirectory, cueFile)) == audioPath {
			matching = append(matching, track)
		}
	}
	return matching
}

func (p *MediaImportProcessor) processCUEAudio(ctx context.Context, sessionID uuid.UUID, sourcePath, relativePath string, tracks []cueTrack) (int, error) {
	probe, err := p.runner.Run(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "json", sourcePath)
	if err != nil {
		return 0, err
	}
	duration, _ := parseProbe(probe)
	successes := 0
	for index, track := range tracks {
		end := duration
		if index+1 < len(tracks) {
			end = tracks[index+1].startSeconds
		}
		file := model.AlbumImportFile{ImportID: sessionID, RelativePath: relativePath, FileName: fmt.Sprintf("%02d - %s.mp3", track.number, track.title), Role: AlbumImportFileRoleAudio, DetectedFormat: "mp3", UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending}
		if err := p.db.WithContext(ctx).Create(&file).Error; err != nil {
			return successes, err
		}
		if end <= track.startSeconds {
			_ = p.failFile(ctx, file.ID, errors.New("invalid CUE track range"))
			continue
		}
		if err := p.processLocalAudio(ctx, sessionID, &file, sourcePath, track.title, track.number, track.startSeconds, end); err != nil {
			_ = p.failFile(ctx, file.ID, err)
			continue
		}
		successes++
	}
	if successes == 0 {
		return 0, errors.New("no CUE tracks were processed successfully")
	}
	return successes, nil
}

func (p *MediaImportProcessor) processCover(ctx context.Context, sessionID uuid.UUID, source string) error {
	output := filepath.Join(filepath.Dir(source), ".atoman-cover.webp")
	if _, err := p.runner.Run(ctx, "ffmpeg", "-y", "-i", source, "-c:v", "libwebp", output); err != nil {
		return err
	}
	file, err := os.Open(output)
	if err != nil {
		return err
	}
	defer file.Close()
	key := "music/album-imports/playback/sessions/" + sessionID.String() + "/cover/" + uuid.NewString() + ".webp"
	if err := p.store.PutObject(key, "image/webp", file); err != nil {
		return err
	}
	var session model.AlbumImportSession
	if err := p.db.WithContext(ctx).First(&session, "id = ?", sessionID).Error; err != nil {
		return err
	}
	payload := map[string]any{}
	_ = json.Unmarshal([]byte(session.PayloadJSON), &payload)
	payload["cover_key"] = key
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return p.db.WithContext(ctx).Model(&session).Update("payload_json", string(encoded)).Error
}

func (p *MediaImportProcessor) processAudio(ctx context.Context, sessionID uuid.UUID, file *model.AlbumImportFile) error {
	dir, err := os.MkdirTemp("", "atoman-media-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	sourcePath := filepath.Join(dir, "source."+safeMediaExtension(file.DetectedFormat))
	if err := p.downloadSource(sourcePath, file.SourceKey); err != nil {
		return err
	}
	return p.processLocalAudio(ctx, sessionID, file, sourcePath, "", 0, 0)
}

func (p *MediaImportProcessor) processLocalAudio(ctx context.Context, sessionID uuid.UUID, file *model.AlbumImportFile, sourcePath, overrideTitle string, overrideTrack int, rangeSeconds ...float64) error {
	probe, err := p.runner.Run(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration:format_tags=title", "-of", "json", sourcePath)
	if err != nil {
		return fmt.Errorf("ffprobe %s: %w", file.FileName, err)
	}
	duration, taggedTitle := parseProbe(probe)
	dir, err := os.MkdirTemp("", "atoman-media-output-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	outputPath := filepath.Join(dir, "playback.mp3")
	args := []string{"-y"}
	if len(rangeSeconds) == 2 {
		args = append(args, "-ss", strconv.FormatFloat(rangeSeconds[0], 'f', -1, 64), "-to", strconv.FormatFloat(rangeSeconds[1], 'f', -1, 64))
	}
	args = append(args, "-i", sourcePath, "-vn", "-c:a", "libmp3lame", "-b:a", "320k", outputPath)
	if _, err := p.runner.Run(ctx, "ffmpeg", args...); err != nil {
		return fmt.Errorf("ffmpeg %s: %w", file.FileName, err)
	}
	output, err := os.Open(outputPath)
	if err != nil {
		return err
	}
	defer output.Close()
	playbackKey := mediaPlaybackKey(sessionID, file.ID, "tracks", "mp3")
	if err := p.store.PutObject(playbackKey, "audio/mpeg", output); err != nil {
		return err
	}
	title := taggedTitle
	if overrideTitle != "" {
		title = overrideTitle
	}
	if title == "" {
		title = titleFromFileName(file.FileName)
	}
	disc, track := discAndTrackFromPath(file.RelativePath)
	if overrideTrack > 0 {
		track = overrideTrack
	}
	return p.db.WithContext(ctx).Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Updates(map[string]any{
		"playback_key": playbackKey, "title": title, "disc_number": disc, "track_number": track,
		"duration_seconds": duration, "processing_status": "completed", "error_message": "",
	}).Error
}

func (p *MediaImportProcessor) downloadSource(destination, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("source object key is required")
	}
	reader, err := p.store.OpenObject(key)
	if err != nil {
		return err
	}
	defer reader.Close()
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (p *MediaImportProcessor) failFile(ctx context.Context, id uuid.UUID, cause error) error {
	return p.db.WithContext(ctx).Model(&model.AlbumImportFile{}).Where("id = ?", id).Updates(map[string]any{"processing_status": AlbumImportFileProcessingStatusFailed, "error_message": cause.Error()}).Error
}

func (p *MediaImportProcessor) setSession(ctx context.Context, id uuid.UUID, status, stage string, current, total int64) error {
	return p.db.WithContext(ctx).Model(&model.AlbumImportSession{}).Where("id = ?", id).Updates(map[string]any{"status": status, "stage": stage, "progress_current": current, "progress_total": total}).Error
}

func safeMediaExtension(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if matched, _ := regexp.MatchString(`^[a-z0-9]{1,10}$`, value); matched {
		return value
	}
	return "bin"
}

func mediaPlaybackKey(sessionID, fileID uuid.UUID, kind, extension string) string {
	return "music/album-imports/playback/sessions/" + sessionID.String() + "/files/" + fileID.String() + "/" + kind + "/" + uuid.NewString() + "." + safeMediaExtension(extension)
}

func parseProbe(raw []byte) (float64, string) {
	var probe struct {
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return 0, ""
	}
	duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	return duration, strings.TrimSpace(probe.Format.Tags["title"])
}

func titleFromFileName(name string) string {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	base = regexp.MustCompile(`^\s*\d+\s*[-._ ]+`).ReplaceAllString(base, "")
	return strings.TrimSpace(base)
}

func discAndTrackFromPath(relativePath string) (int, int) {
	disc, track := 1, 0
	matched := regexp.MustCompile(`(?i)(?:disc|cd)\s*(\d+)`).FindStringSubmatch(relativePath)
	if len(matched) == 2 {
		if parsed, err := strconv.Atoi(matched[1]); err == nil && parsed > 0 {
			disc = parsed
		}
	}
	base := filepath.Base(relativePath)
	matched = regexp.MustCompile(`^\s*(\d+)`).FindStringSubmatch(base)
	if len(matched) == 2 {
		track, _ = strconv.Atoi(matched[1])
	}
	return disc, track
}
