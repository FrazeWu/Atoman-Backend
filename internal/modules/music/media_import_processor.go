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
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"atoman/internal/model"

	"github.com/google/uuid"
	"golang.org/x/text/encoding/simplifiedchinese"
	"gorm.io/gorm"
)

const (
	mediaArchiveMaxBytes        int64 = 30 * 1024 * 1024 * 1024
	mediaArchiveMaxEntries            = 5000
	mediaArchiveMaxRatio        int64 = 100
	embeddedCoverMinDimension         = 300
	embeddedCoverMinAspectRatio       = 0.8
	embeddedCoverMaxAspectRatio       = 1.25
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
	encrypted  bool
	hasSize    bool
	hasPacked  bool
}

// validateArchiveListing accepts only ordinary, bounded relative archive entries.
func validateArchiveListing(raw []byte) error {
	entries := []archiveListEntry{}
	entry := archiveListEntry{}
	flush := func() error {
		if entry.path == "" {
			return nil
		}
		// 7zz -slt starts with archive metadata (including its local temp path).
		// Real archive members always include at least one size field.
		if !entry.hasSize && !entry.hasPacked {
			entry = archiveListEntry{}
			return nil
		}
		normalizedPath := strings.ReplaceAll(entry.path, `\`, "/")
		if strings.HasPrefix(normalizedPath, "/") || regexp.MustCompile(`^[A-Za-z]:`).MatchString(normalizedPath) {
			return fmt.Errorf("unsafe archive path %q", entry.path)
		}
		clean := path.Clean(normalizedPath)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
			return fmt.Errorf("unsafe archive path %q", entry.path)
		}
		attrs := strings.ToLower(entry.attributes)
		if strings.Contains(attrs, "l") || strings.Contains(attrs, "symlink") || strings.Contains(attrs, "reparse") {
			return fmt.Errorf("unsafe archive entry %q", entry.path)
		}
		if entry.encrypted {
			return errors.New("压缩包已加密，请上传无密码压缩包或直接上传音频文件")
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
			entry.hasSize = true
		case "Packed Size":
			entry.packedSize, _ = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			entry.hasPacked = true
		case "Attributes":
			entry.attributes = strings.TrimSpace(value)
		case "Encrypted":
			entry.encrypted = strings.TrimSpace(value) == "+"
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
	enricher  AlbumImportMetadataEnricher
}

func NewMediaImportProcessor(db *gorm.DB, store mediaImportStore, runner MediaCommandRunner, playbackURLPrefix string) *MediaImportProcessor {
	return &MediaImportProcessor{db: db, store: store, runner: runner, urlPrefix: strings.TrimRight(strings.TrimSpace(playbackURLPrefix), "/")}
}

func (p *MediaImportProcessor) WithMetadataEnricher(enricher AlbumImportMetadataEnricher) *MediaImportProcessor {
	p.enricher = enricher
	return p
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
	var cover *model.AlbumImportFile
	hasCompletedAudio := false
	for _, file := range session.Files {
		if file.Role == AlbumImportFileRoleAudio && file.UploadStatus == AlbumImportFileUploadStatusUploaded && file.SourceKey != "" && file.ProcessingStatus != "completed" {
			files = append(files, file)
		}
		if file.Role == AlbumImportFileRoleAudio && file.UploadStatus == AlbumImportFileUploadStatusUploaded && file.SourceKey != "" && file.ProcessingStatus == "completed" {
			hasCompletedAudio = true
		}
		if file.Role == AlbumImportFileRoleCue && file.UploadStatus == AlbumImportFileUploadStatusUploaded {
			cues = append(cues, file)
		}
		if cover == nil && file.Role == AlbumImportFileRoleCover && file.UploadStatus == AlbumImportFileUploadStatusUploaded && file.SourceKey != "" {
			candidate := file
			cover = &candidate
		}
	}
	if cover != nil && cover.ProcessingStatus != "completed" {
		if err := p.processUploadedCover(ctx, session.ID, *cover); err != nil {
			_ = p.failFile(ctx, cover.ID, err)
		}
	}
	if len(files) == 0 {
		if hasCompletedAudio {
			return p.persistDerivedTracks(ctx, session.ID)
		}
		return errors.New("no uploaded audio files to process")
	}
	sort.SliceStable(files, func(i, j int) bool {
		return albumImportTrackPathLess(files[i].RelativePath, files[j].RelativePath)
	})
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
	if err := p.persistDerivedTracks(ctx, session.ID); err != nil {
		return err
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
			if count > 0 {
				if err := p.db.WithContext(ctx).Model(&model.AlbumImportFile{}).Where("id = ?", audios[index].ID).Updates(map[string]any{"processing_status": "completed", "error_message": ""}).Error; err != nil {
					return nil, 0, 0, err
				}
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
	localLyrics := map[string]AlbumImportTrackLyricsPayload{}
	cover := ""
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe extracted symlink %q", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative != "." && shouldIgnoreAlbumImportPath(relative) {
				return filepath.SkipDir
			}
			return nil
		}
		if shouldIgnoreAlbumImportPath(relative) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return nil
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
		case AlbumImportFileRoleLyrics:
			if raw, readErr := os.ReadFile(path); readErr == nil && len(raw) <= 2*1024*1024 {
				payload := lyricsPayloadFromFile(path, raw)
				localLyrics[normalizedLyricName(relative)] = payload
				disc, track := discAndTrackFromPath(relative)
				if track > 0 {
					localLyrics[lyricSequenceKey(disc, track)] = payload
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.SliceStable(audios, func(i, j int) bool {
		return albumImportTrackPathLess(audios[i].relative, audios[j].relative)
	})
	sort.Strings(cues)
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
		if cover == "" && p.processEmbeddedCover(ctx, sessionID, audio.path) == nil {
			cover = audio.path
		}
		if heartbeat != nil {
			if err := heartbeat(); err != nil {
				return err
			}
		}
		file, err := p.findOrCreateDerivedAudio(ctx, sessionID, filepath.ToSlash(audio.relative), filepath.Base(audio.path), strings.TrimPrefix(strings.ToLower(filepath.Ext(audio.path)), "."))
		if err != nil {
			return err
		}
		if file.ProcessingStatus == "completed" {
			processed++
			continue
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
	if err := p.persistDerivedTracksWithLyrics(ctx, sessionID, localLyrics); err != nil {
		return err
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
		file, err := p.findOrCreateDerivedAudio(ctx, sessionID, cueTrackRelativePath(relativePath, track.number), fmt.Sprintf("%02d - %s.mp3", track.number, track.title), "mp3")
		if err != nil {
			return successes, err
		}
		if file.ProcessingStatus == "completed" {
			successes++
			continue
		}
		if end <= track.startSeconds {
			_ = p.failFile(ctx, file.ID, errors.New("invalid CUE track range"))
			continue
		}
		if err := p.processLocalAudio(ctx, sessionID, &file, sourcePath, track.title, track.number, end-track.startSeconds, track.startSeconds, end); err != nil {
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

func cueTrackRelativePath(sourceRelativePath string, trackNumber int) string {
	return filepath.ToSlash(sourceRelativePath) + "#cue-track-" + strconv.Itoa(trackNumber)
}

func (p *MediaImportProcessor) findOrCreateDerivedAudio(ctx context.Context, sessionID uuid.UUID, relativePath, fileName, format string) (model.AlbumImportFile, error) {
	var file model.AlbumImportFile
	err := p.db.WithContext(ctx).Where("import_id = ? AND relative_path = ? AND role = ?", sessionID, relativePath, AlbumImportFileRoleAudio).First(&file).Error
	if err == nil {
		return file, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return file, err
	}
	file = model.AlbumImportFile{ImportID: sessionID, RelativePath: relativePath, FileName: fileName, Role: AlbumImportFileRoleAudio, DetectedFormat: format, UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending}
	return file, p.db.WithContext(ctx).Create(&file).Error
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

func (p *MediaImportProcessor) processUploadedCover(ctx context.Context, sessionID uuid.UUID, cover model.AlbumImportFile) error {
	dir, err := os.MkdirTemp("", "atoman-cover-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	source := filepath.Join(dir, "cover."+safeMediaExtension(cover.DetectedFormat))
	if err := p.downloadSource(source, cover.SourceKey); err != nil {
		return err
	}
	if err := p.processCover(ctx, sessionID, source); err != nil {
		return err
	}
	return p.db.WithContext(ctx).Model(&model.AlbumImportFile{}).Where("id = ?", cover.ID).Updates(map[string]any{
		"processing_status": "completed",
		"error_message":     "",
	}).Error
}

func (p *MediaImportProcessor) persistDerivedTracks(ctx context.Context, sessionID uuid.UUID) error {
	localLyrics := p.loadUploadedLyrics(ctx, sessionID)
	return p.persistDerivedTracksWithLyrics(ctx, sessionID, localLyrics)
}

func (p *MediaImportProcessor) persistDerivedTracksWithLyrics(ctx context.Context, sessionID uuid.UUID, localLyrics map[string]AlbumImportTrackLyricsPayload) error {
	var session model.AlbumImportSession
	if err := p.db.WithContext(ctx).First(&session, "id = ?", sessionID).Error; err != nil {
		return err
	}
	var files []model.AlbumImportFile
	if err := p.db.WithContext(ctx).
		Where("import_id = ? AND role = ? AND processing_status = ? AND playback_key <> ''", sessionID, AlbumImportFileRoleAudio, "completed").
		Order("disc_number ASC, track_number ASC, created_at ASC").
		Find(&files).Error; err != nil {
		return err
	}

	payload := map[string]any{}
	_ = json.Unmarshal([]byte(session.PayloadJSON), &payload)
	albumCounts := make(map[string]int)
	albumNames := make(map[string]string)
	albumOrder := make([]string, 0)
	for _, file := range files {
		album := albumImportFileAlbum(file)
		key := strings.ToLower(strings.Join(strings.Fields(album), " "))
		if key == "" {
			continue
		}
		if _, exists := albumCounts[key]; !exists {
			albumOrder = append(albumOrder, key)
			albumNames[key] = album
		}
		albumCounts[key]++
	}
	primaryAlbumKey := ""
	for _, key := range albumOrder {
		if primaryAlbumKey == "" || albumCounts[key] > albumCounts[primaryAlbumKey] {
			primaryAlbumKey = key
		}
	}
	if primaryAlbumKey != "" {
		payload["derived_album_title"] = albumNames[primaryAlbumKey]
	}

	metadataTracks := make([]AlbumImportMetadataTrack, 0, len(files))
	for _, file := range files {
		album := albumImportFileAlbum(file)
		albumKey := strings.ToLower(strings.Join(strings.Fields(album), " "))
		if primaryAlbumKey != "" && albumKey != "" && albumKey != primaryAlbumKey {
			reason := "属于其他专辑：" + album
			if err := p.db.WithContext(ctx).Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Updates(map[string]any{
				"processing_status": "ignored",
				"error_message":     reason,
			}).Error; err != nil {
				return err
			}
			continue
		}
		audioURL := ""
		if p.urlPrefix != "" {
			audioURL = p.urlPrefix + "/" + strings.TrimLeft(file.PlaybackKey, "/")
		}
		metadata := albumImportFileMetadata(file)
		metadataTracks = append(metadataTracks, AlbumImportMetadataTrack{
			Title: file.Title, Artist: stringValue(metadata["artist"]), Album: album,
			DiscNumber: normalizedDiscNumber(file.DiscNumber), TrackNumber: file.TrackNumber,
			DurationSeconds: file.DurationSeconds, Origin: file.RelativePath,
			AudioKey: file.PlaybackKey, AudioURL: audioURL,
		})
	}
	artist := majorityMetadataValue(metadataTracks, func(track AlbumImportMetadataTrack) string { return track.Artist })
	if artist == "" {
		artist = p.albumImportArtistName(ctx, payload)
	}
	result := AlbumImportMetadataResult{AlbumTitle: stringValue(payload["derived_album_title"]), Tracks: baseMetadataTracks(metadataTracks)}
	if p.enricher != nil {
		if enriched, enrichErr := p.enricher.Enrich(ctx, AlbumImportMetadataInput{
			AlbumTitle: result.AlbumTitle, Artist: artist, Tracks: metadataTracks, LocalLyrics: localLyrics,
		}); enrichErr == nil {
			result = enriched
		}
	} else {
		for index := range result.Tracks {
			if lyrics, ok := findLocalLyrics(localLyrics, metadataTracks[index]); ok {
				result.Tracks[index].Lyrics = &lyrics
				result.Tracks[index].LyricsSource = "local"
			}
		}
	}
	derivedTracks := make([]map[string]any, 0, len(result.Tracks))
	for _, track := range result.Tracks {
		derived := map[string]any{
			"title": track.Title, "disc_number": track.DiscNumber, "track_number": track.TrackNumber,
			"audio_key": track.AudioKey, "audio_url": track.AudioURL, "origin": track.Origin,
		}
		for _, file := range files {
			if file.PlaybackKey == track.AudioKey {
				derived["file_id"] = file.ID.String()
				break
			}
		}
		if track.Lyrics != nil {
			derived["lyrics"] = track.Lyrics
			derived["lyrics_source"] = track.LyricsSource
		}
		derivedTracks = append(derivedTracks, derived)
	}
	payload["derived_tracks"] = derivedTracks
	if result.AlbumTitle != "" {
		payload["derived_album_title"] = result.AlbumTitle
	}
	if result.ReleaseDate != "" {
		payload["derived_release_date"] = result.ReleaseDate
	}
	if result.AlbumType != "" {
		payload["derived_album_type"] = result.AlbumType
	}
	if result.CoverURL != "" && stringValue(payload["cover_key"]) == "" {
		payload["derived_cover"] = result.CoverURL
	}
	if result.SourceURL != "" {
		payload["metadata_source_url"] = result.SourceURL
	}
	if len(result.MissingArtists) > 0 {
		payload["missing_artists"] = result.MissingArtists
	} else {
		delete(payload, "missing_artists")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return p.db.WithContext(ctx).Model(&session).Update("payload_json", string(encoded)).Error
}

func albumImportFileAlbum(file model.AlbumImportFile) string {
	metadata := albumImportFileMetadata(file)
	return strings.TrimSpace(stringValue(metadata["album"]))
}

func albumImportFileMetadata(file model.AlbumImportFile) map[string]any {
	metadata := map[string]any{}
	if strings.TrimSpace(file.MetadataJSON) != "" {
		_ = json.Unmarshal([]byte(file.MetadataJSON), &metadata)
	}
	return metadata
}

func (p *MediaImportProcessor) loadUploadedLyrics(ctx context.Context, sessionID uuid.UUID) map[string]AlbumImportTrackLyricsPayload {
	result := map[string]AlbumImportTrackLyricsPayload{}
	var files []model.AlbumImportFile
	if p.store == nil || p.db.WithContext(ctx).Where("import_id = ? AND role = ? AND upload_status = ?", sessionID, AlbumImportFileRoleLyrics, AlbumImportFileUploadStatusUploaded).Find(&files).Error != nil {
		return result
	}
	for _, file := range files {
		reader, err := p.store.OpenObject(file.SourceKey)
		if err != nil {
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(reader, 2*1024*1024+1))
		_ = reader.Close()
		if readErr == nil && len(raw) <= 2*1024*1024 {
			payload := lyricsPayloadFromFile(file.FileName, raw)
			result[normalizedLyricName(file.RelativePath)] = payload
			disc, track := discAndTrackFromPath(file.RelativePath)
			if track > 0 {
				result[lyricSequenceKey(disc, track)] = payload
			}
			_ = p.db.WithContext(ctx).Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Updates(map[string]any{
				"processing_status": "completed", "error_message": "",
			}).Error
		}
	}
	return result
}

func lyricsPayloadFromFile(name string, raw []byte) AlbumImportTrackLyricsPayload {
	format := "plain"
	if strings.EqualFold(filepath.Ext(name), ".lrc") {
		format = "lrc"
	}
	return AlbumImportTrackLyricsPayload{Content: strings.TrimSpace(string(raw)), Format: format, EditSummary: "通过专辑导入添加歌词"}
}

func majorityMetadataValue(tracks []AlbumImportMetadataTrack, value func(AlbumImportMetadataTrack) string) string {
	counts := map[string]int{}
	values := map[string]string{}
	best := ""
	for _, track := range tracks {
		current := strings.TrimSpace(value(track))
		key := normalizedMusicText(current)
		if key == "" {
			continue
		}
		counts[key]++
		values[key] = current
		if best == "" || counts[key] > counts[best] {
			best = key
		}
	}
	return values[best]
}

func (p *MediaImportProcessor) albumImportArtistName(ctx context.Context, payload map[string]any) string {
	if name := strings.TrimSpace(stringValue(payload["artist_name"])); name != "" {
		return name
	}
	if p == nil || p.db == nil {
		return ""
	}
	artistID, err := uuid.Parse(strings.TrimSpace(stringValue(payload["artist_id"])))
	if err != nil {
		return ""
	}
	var artist model.Artist
	if p.db.WithContext(ctx).Select("name").First(&artist, "id = ?", artistID).Error != nil {
		return ""
	}
	return strings.TrimSpace(artist.Name)
}

func (p *MediaImportProcessor) processEmbeddedCover(ctx context.Context, sessionID uuid.UUID, source string) error {
	output := filepath.Join(filepath.Dir(source), ".atoman-embedded-cover-"+uuid.NewString()+".webp")
	defer os.Remove(output)
	if _, err := p.runner.Run(ctx, "ffmpeg", "-y", "-i", source, "-map", "0:v:0", "-frames:v", "1", "-c:v", "libwebp", output); err != nil {
		return err
	}
	probe, err := p.runner.Run(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "json", output)
	if err != nil {
		return err
	}
	width, height := parseImageDimensions(probe)
	if width < embeddedCoverMinDimension || height < embeddedCoverMinDimension {
		return fmt.Errorf("embedded artwork is too small: %dx%d", width, height)
	}
	aspectRatio := float64(width) / float64(height)
	if aspectRatio < embeddedCoverMinAspectRatio || aspectRatio > embeddedCoverMaxAspectRatio {
		return fmt.Errorf("embedded artwork has invalid aspect ratio: %dx%d", width, height)
	}
	return p.processCover(ctx, sessionID, output)
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

func (p *MediaImportProcessor) processLocalAudio(ctx context.Context, sessionID uuid.UUID, file *model.AlbumImportFile, sourcePath, overrideTitle string, overrideTrack int, knownDuration float64, rangeSeconds ...float64) error {
	probe, err := p.runner.Run(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration,format_name,bit_rate,size:format_tags=title,album,artist,album_artist,albumartist,track,tracknumber,track_number,disc,discnumber,disc_number:stream=codec_name,sample_rate,bits_per_raw_sample,bits_per_sample,channels,bit_rate", "-of", "json", sourcePath)
	if err != nil {
		return fmt.Errorf("ffprobe %s: %w", file.FileName, err)
	}
	metadata := parseAudioProbe(probe)
	duration, taggedTitle := metadata.duration, metadata.title
	if knownDuration > 0 {
		duration = knownDuration
	}
	dir, err := os.MkdirTemp("", "atoman-media-output-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	outputPath := filepath.Join(dir, "playback.mp3")
	transcodeArgs := []string{"-y", "-v", "error", "-i", sourcePath,
		"-map", "0:a:0", "-vn", "-c:a", "libmp3lame", "-b:a", "320k", outputPath}
	if len(rangeSeconds) == 2 {
		transcodeArgs = []string{"-y", "-v", "error", "-ss", strconv.FormatFloat(rangeSeconds[0], 'f', -1, 64), "-to", strconv.FormatFloat(rangeSeconds[1], 'f', -1, 64), "-i", sourcePath,
			"-map", "0:a:0", "-vn", "-c:a", "libmp3lame", "-b:a", "320k", outputPath}
	}
	if _, err := p.runner.Run(ctx, "ffmpeg", transcodeArgs...); err != nil {
		return fmt.Errorf("ffmpeg %s: %w", file.FileName, err)
	}
	waveformArgs := []string{"-v", "error"}
	if len(rangeSeconds) == 2 {
		waveformArgs = append(waveformArgs, "-ss", strconv.FormatFloat(rangeSeconds[0], 'f', -1, 64), "-to", strconv.FormatFloat(rangeSeconds[1], 'f', -1, 64))
	}
	waveformArgs = append(waveformArgs, "-i", sourcePath,
		"-map", "0:a:0", "-vn", "-ac", "1", "-ar", "8000", "-f", "s16le", "pipe:1")
	waveformPCM, waveformErr := p.runner.Run(ctx, "ffmpeg", waveformArgs...)
	waveformPeaks := waveformPeaksFromPCM(waveformPCM, WaveformPeakCount)
	if waveformErr != nil || len(waveformPeaks) != WaveformPeakCount {
		waveformPeaks = make([]int, WaveformPeakCount)
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
	if metadata.discNumber > 0 {
		disc = metadata.discNumber
	}
	if metadata.trackNumber > 0 {
		track = metadata.trackNumber
	}
	if overrideTrack > 0 {
		track = overrideTrack
	}
	metadataValues := metadata.archiveMetadata()
	metadataValues["waveform_peaks"] = waveformPeaks
	metadataJSON, _ := json.Marshal(metadataValues)
	return p.db.WithContext(ctx).Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Updates(map[string]any{
		"playback_key": playbackKey, "title": title, "disc_number": disc, "track_number": track,
		"duration_seconds": duration, "metadata_json": string(metadataJSON), "processing_status": "completed", "error_message": "",
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
	metadata := parseAudioProbe(raw)
	return metadata.duration, metadata.title
}

type audioProbeMetadata struct {
	duration    float64
	title       string
	album       string
	artist      string
	discNumber  int
	trackNumber int
	container   string
	codec       string
	bitRate     int
	sampleRate  int
	bitDepth    int
	channels    int
}

func parseAudioProbe(raw []byte) audioProbeMetadata {
	var probe struct {
		Format struct {
			Duration   string            `json:"duration"`
			FormatName string            `json:"format_name"`
			BitRate    string            `json:"bit_rate"`
			Tags       map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			CodecName        string `json:"codec_name"`
			SampleRate       string `json:"sample_rate"`
			BitsPerRawSample string `json:"bits_per_raw_sample"`
			BitsPerSample    string `json:"bits_per_sample"`
			Channels         int    `json:"channels"`
			BitRate          string `json:"bit_rate"`
		} `json:"streams"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return audioProbeMetadata{}
	}
	duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	metadata := audioProbeMetadata{duration: duration, container: strings.TrimSpace(probe.Format.FormatName)}
	metadata.bitRate, _ = strconv.Atoi(probe.Format.BitRate)
	if len(probe.Streams) > 0 {
		stream := probe.Streams[0]
		metadata.codec = strings.TrimSpace(stream.CodecName)
		metadata.sampleRate, _ = strconv.Atoi(stream.SampleRate)
		metadata.bitDepth, _ = strconv.Atoi(stream.BitsPerRawSample)
		if metadata.bitDepth == 0 {
			metadata.bitDepth, _ = strconv.Atoi(stream.BitsPerSample)
		}
		metadata.channels = stream.Channels
		if metadata.bitRate == 0 {
			metadata.bitRate, _ = strconv.Atoi(stream.BitRate)
		}
	}
	for key, value := range probe.Format.Tags {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "title":
			metadata.title = strings.TrimSpace(value)
		case "album":
			metadata.album = strings.TrimSpace(value)
		case "artist":
			if metadata.artist == "" {
				metadata.artist = strings.TrimSpace(value)
			}
		case "album_artist", "albumartist":
			metadata.artist = strings.TrimSpace(value)
		case "track", "tracknumber", "track_number":
			metadata.trackNumber = albumImportTagNumber(value)
		case "disc", "discnumber", "disc_number":
			metadata.discNumber = albumImportTagNumber(value)
		}
	}
	return metadata
}

func (m audioProbeMetadata) archiveMetadata() map[string]any {
	return map[string]any{
		"album":       m.album,
		"artist":      m.artist,
		"container":   m.container,
		"codec":       m.codec,
		"bit_rate":    m.bitRate,
		"sample_rate": m.sampleRate,
		"bit_depth":   m.bitDepth,
		"channels":    m.channels,
	}
}

func albumImportTagNumber(value string) int {
	matched := regexp.MustCompile(`^\s*(\d+)`).FindStringSubmatch(value)
	if len(matched) != 2 {
		return 0
	}
	number, _ := strconv.Atoi(matched[1])
	return number
}

func parseImageDimensions(raw []byte) (int, int) {
	var probe struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	if json.Unmarshal(raw, &probe) != nil || len(probe.Streams) == 0 {
		return 0, 0
	}
	return probe.Streams[0].Width, probe.Streams[0].Height
}

func titleFromFileName(name string) string {
	title, _, _ := albumImportTrackInfoFromFileName(name)
	return title
}

func discAndTrackFromPath(relativePath string) (int, int) {
	disc, track := 1, 0
	matched := regexp.MustCompile(`(?i)(?:disc|cd)\s*(\d+)`).FindStringSubmatch(relativePath)
	if len(matched) == 2 {
		if parsed, err := strconv.Atoi(matched[1]); err == nil && parsed > 0 {
			disc = parsed
		}
	}
	_, fileDisc, fileTrack := albumImportTrackInfoFromFileName(filepath.Base(relativePath))
	if fileDisc > 0 {
		disc = fileDisc
	}
	if fileTrack > 0 {
		track = fileTrack
	}
	return disc, track
}

func albumImportTrackInfoFromFileName(name string) (string, int, int) {
	base := strings.TrimSpace(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)))
	multiDisc := regexp.MustCompile(`(?i)^\s*(\d{1,2})\s*[-_.]\s*(\d{1,3})(?:\s*[-_.]\s*|\s+)(.+?)\s*$`).FindStringSubmatch(base)
	if len(multiDisc) == 4 {
		disc, _ := strconv.Atoi(multiDisc[1])
		track, _ := strconv.Atoi(multiDisc[2])
		return strings.TrimSpace(multiDisc[3]), disc, track
	}
	explicitTrack := regexp.MustCompile(`(?i)^\s*(?:track\s*)?(\d{1,3})\s*(?:[-_]\s*|\.\s+)(.+?)\s*$`).FindStringSubmatch(base)
	if len(explicitTrack) == 3 {
		track, _ := strconv.Atoi(explicitTrack[1])
		return strings.TrimSpace(explicitTrack[2]), 0, track
	}
	zeroPaddedTrack := regexp.MustCompile(`^\s*(0\d{1,2})\s+(.+?)\s*$`).FindStringSubmatch(base)
	if len(zeroPaddedTrack) == 3 {
		track, _ := strconv.Atoi(zeroPaddedTrack[1])
		return strings.TrimSpace(zeroPaddedTrack[2]), 0, track
	}
	leadingTrack := regexp.MustCompile(`^\s*(\d{1,3})(?:\s|[-_.])`).FindStringSubmatch(base)
	track := 0
	if len(leadingTrack) == 2 {
		track, _ = strconv.Atoi(leadingTrack[1])
	}
	return base, 0, track
}

func albumImportTrackPathLess(left, right string) bool {
	leftDisc, leftTrack := discAndTrackFromPath(left)
	rightDisc, rightTrack := discAndTrackFromPath(right)
	if leftDisc != rightDisc {
		return leftDisc < rightDisc
	}
	if leftTrack > 0 && rightTrack > 0 && leftTrack != rightTrack {
		return leftTrack < rightTrack
	}
	if leftTrack > 0 && rightTrack == 0 {
		return true
	}
	if leftTrack == 0 && rightTrack > 0 {
		return false
	}
	return strings.ToLower(filepath.ToSlash(left)) < strings.ToLower(filepath.ToSlash(right))
}
