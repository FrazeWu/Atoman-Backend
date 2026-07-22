package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
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
	for _, tool := range []string{"ffmpeg", "ffprobe", "7zz"} {
		if _, err := runner.LookPath(tool); err != nil {
			return errMissingMediaTool{name: tool}
		}
	}
	return nil
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
	if err := ValidateMediaToolchain(p.runner); err != nil {
		return err
	}
	var session model.AlbumImportSession
	if err := p.db.WithContext(ctx).Preload("Files").First(&session, "id = ?", job.ImportID).Error; err != nil {
		return err
	}
	files := make([]model.AlbumImportFile, 0)
	for _, file := range session.Files {
		if file.Role == AlbumImportFileRoleAudio && file.UploadStatus == AlbumImportFileUploadStatusUploaded && file.ProcessingStatus != "completed" {
			files = append(files, file)
		}
	}
	if len(files) == 0 {
		return errors.New("no uploaded audio files to process")
	}
	if err := p.setSession(ctx, session.ID, AlbumImportStatusAnalyzing, AlbumImportStageAnalyzing, 0, int64(len(files))); err != nil {
		return err
	}
	successes := 0
	for index := range files {
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
		if err := p.setSession(ctx, session.ID, AlbumImportStatusTranscoding, AlbumImportStageTranscoding, int64(successes), int64(len(files))); err != nil {
			return err
		}
	}
	if successes == 0 {
		return errors.New("no audio tracks were processed successfully")
	}
	return nil
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
	probe, err := p.runner.Run(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration:format_tags=title", "-of", "json", sourcePath)
	if err != nil {
		return fmt.Errorf("ffprobe %s: %w", file.FileName, err)
	}
	duration, taggedTitle := parseProbe(probe)
	outputPath := filepath.Join(dir, "playback.mp3")
	if _, err := p.runner.Run(ctx, "ffmpeg", "-y", "-i", sourcePath, "-vn", "-c:a", "libmp3lame", "-b:a", "320k", outputPath); err != nil {
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
	if title == "" {
		title = titleFromFileName(file.FileName)
	}
	disc, track := discAndTrackFromPath(file.RelativePath)
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
