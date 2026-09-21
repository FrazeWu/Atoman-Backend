package service

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"atoman/internal/model"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

// VideoPreviewObjectStore is the small R2/S3 surface needed by the preview worker.
type VideoPreviewObjectStore interface {
	GetObject(*s3.GetObjectInput) (*s3.GetObjectOutput, error)
	PutObject(*s3.PutObjectInput) (*s3.PutObjectOutput, error)
}

type VideoPreviewResult struct {
	Thumbnails   []VideoPreviewThumbnail
	ThumbnailURL string
	DurationSec  int
}

type VideoPreviewMetadataGenerator interface {
	GenerateWithMetadata(video model.Video) (VideoPreviewResult, error)
}

type FFmpegPreviewGenerator struct {
	// UploadsRoot is retained for old local uploads and can be removed after migration.
	UploadsRoot string
	PublicBase  string
	Bucket      string
	Store       VideoPreviewObjectStore
}

func (g FFmpegPreviewGenerator) Generate(video model.Video) ([]VideoPreviewThumbnail, error) {
	result, err := g.GenerateWithMetadata(video)
	if err != nil {
		return nil, err
	}
	return result.Thumbnails, nil
}

func (g FFmpegPreviewGenerator) GenerateWithMetadata(video model.Video) (VideoPreviewResult, error) {
	if g.Store != nil && !strings.HasPrefix(strings.TrimSpace(video.VideoURL), "/uploads/") {
		return g.generateFromObjectStore(video)
	}
	return g.generateFromLocalUpload(video)
}

func (g FFmpegPreviewGenerator) generateFromObjectStore(video model.Video) (VideoPreviewResult, error) {
	key, ok := g.objectKey(video.VideoURL)
	if !ok {
		return VideoPreviewResult{}, fmt.Errorf("cannot resolve R2 object key from video URL: %s", video.VideoURL)
	}
	if strings.TrimSpace(g.Bucket) == "" {
		return VideoPreviewResult{}, fmt.Errorf("video preview bucket is not configured")
	}

	object, err := g.Store.GetObject(&s3.GetObjectInput{Bucket: aws.String(g.Bucket), Key: aws.String(key)})
	if err != nil {
		return VideoPreviewResult{}, fmt.Errorf("download video from R2: %w", err)
	}
	defer object.Body.Close()

	workingDir, err := os.MkdirTemp("", "atoman-video-preview-")
	if err != nil {
		return VideoPreviewResult{}, err
	}
	defer os.RemoveAll(workingDir)

	ext := filepath.Ext(key)
	if ext == "" {
		ext = ".video"
	}
	inputPath := filepath.Join(workingDir, "source"+ext)
	input, err := os.Create(inputPath)
	if err != nil {
		return VideoPreviewResult{}, err
	}
	if _, err := io.Copy(input, object.Body); err != nil {
		_ = input.Close()
		return VideoPreviewResult{}, fmt.Errorf("download video body: %w", err)
	}
	if err := input.Close(); err != nil {
		return VideoPreviewResult{}, err
	}

	outputDir := filepath.Join(workingDir, "previews")
	generated, err := g.generateFiles(video, inputPath, outputDir)
	if err != nil {
		return VideoPreviewResult{}, err
	}

	assetPrefix := "video/previews/" + video.UserID.String() + "/" + video.ID.String()
	for _, asset := range generated.assets {
		data, err := os.ReadFile(asset.path)
		if err != nil {
			return VideoPreviewResult{}, err
		}
		assetKey := assetPrefix + "/" + asset.name
		if _, err := g.Store.PutObject(&s3.PutObjectInput{
			Bucket:       aws.String(g.Bucket),
			Key:          aws.String(assetKey),
			Body:         bytes.NewReader(data),
			ContentType:  aws.String("image/webp"),
			CacheControl: aws.String("public, max-age=31536000, immutable"),
		}); err != nil {
			return VideoPreviewResult{}, fmt.Errorf("upload preview asset to R2: %w", err)
		}
		if asset.name == "cover.webp" {
			generated.ThumbnailURL = g.publicURL(assetKey)
		}
		for index := range generated.Thumbnails {
			if generated.Thumbnails[index].URL == asset.name {
				generated.Thumbnails[index].URL = g.publicURL(assetKey)
			}
		}
	}
	generated.assets = nil
	return generated.VideoPreviewResult, nil
}

func (g FFmpegPreviewGenerator) generateFromLocalUpload(video model.Video) (VideoPreviewResult, error) {
	cleanURLPath := filepath.ToSlash(filepath.Clean(video.VideoURL))
	if !strings.HasPrefix(cleanURLPath, "/uploads/") {
		return VideoPreviewResult{}, fmt.Errorf("preview worker cannot resolve video source: %s", video.VideoURL)
	}

	root := g.UploadsRoot
	if root == "" {
		root = "."
	}
	inputPath := filepath.Join(root, strings.TrimPrefix(cleanURLPath, "/"))
	uploadsRoot, err := filepath.Abs(filepath.Join(root, "uploads"))
	if err != nil {
		return VideoPreviewResult{}, err
	}
	inputAbs, err := filepath.Abs(inputPath)
	if err != nil {
		return VideoPreviewResult{}, err
	}
	relInput, err := filepath.Rel(uploadsRoot, inputAbs)
	if err != nil {
		return VideoPreviewResult{}, err
	}
	if relInput == "." || strings.HasPrefix(relInput, ".."+string(os.PathSeparator)) || relInput == ".." || filepath.IsAbs(relInput) {
		return VideoPreviewResult{}, fmt.Errorf("preview worker input escaped uploads root: %s", video.VideoURL)
	}

	outputDir := filepath.Join(root, "uploads", "video", "previews", video.UserID.String(), video.ID.String())
	generated, err := g.generateFiles(video, inputPath, outputDir)
	if err != nil {
		return VideoPreviewResult{}, err
	}
	for index := range generated.Thumbnails {
		generated.Thumbnails[index].URL = "/uploads/video/previews/" + video.UserID.String() + "/" + video.ID.String() + "/" + generated.Thumbnails[index].URL
	}
	generated.ThumbnailURL = "/uploads/video/previews/" + video.UserID.String() + "/" + video.ID.String() + "/cover.webp"
	generated.assets = nil
	return generated.VideoPreviewResult, nil
}

type generatedPreviewAsset struct {
	name string
	path string
}

type videoPreviewResultWithAssets struct {
	VideoPreviewResult
	assets []generatedPreviewAsset
}

func (g FFmpegPreviewGenerator) generateFiles(video model.Video, inputPath, outputDir string) (videoPreviewResultWithAssets, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return videoPreviewResultWithAssets{}, err
	}
	for _, pattern := range []string{"thumb-*.webp", "cover.webp"} {
		files, _ := filepath.Glob(filepath.Join(outputDir, pattern))
		for _, file := range files {
			_ = os.Remove(file)
		}
	}

	duration := video.DurationSec
	if duration <= 0 {
		duration = probeDuration(inputPath)
	}
	interval := 10
	if duration > 1200 {
		interval = (duration + 119) / 120
	}

	coverPath := filepath.Join(outputDir, "cover.webp")
	if output, err := exec.Command("ffmpeg", "-y", "-i", inputPath, "-vf", "scale=1280:-2:force_original_aspect_ratio=decrease", "-frames:v", "1", coverPath).CombinedOutput(); err != nil {
		return videoPreviewResultWithAssets{}, fmt.Errorf("ffmpeg cover failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	pattern := filepath.Join(outputDir, "thumb-%03d.webp")
	if output, err := exec.Command("ffmpeg", "-y", "-i", inputPath, "-vf", fmt.Sprintf("fps=1/%d,scale=160:90", interval), "-frames:v", "120", pattern).CombinedOutput(); err != nil {
		if fallbackErr := createFallbackThumbnail(coverPath, filepath.Join(outputDir, "thumb-001.webp")); fallbackErr != nil {
			return videoPreviewResultWithAssets{}, fmt.Errorf("ffmpeg previews failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}

	files, err := filepath.Glob(filepath.Join(outputDir, "thumb-*.webp"))
	if err != nil {
		return videoPreviewResultWithAssets{}, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		if err := createFallbackThumbnail(coverPath, filepath.Join(outputDir, "thumb-001.webp")); err != nil {
			return videoPreviewResultWithAssets{}, fmt.Errorf("ffmpeg produced no preview thumbnails: %w", err)
		}
		files = []string{filepath.Join(outputDir, "thumb-001.webp")}
	}

	thumbnails := make([]VideoPreviewThumbnail, 0, len(files))
	assets := []generatedPreviewAsset{{name: "cover.webp", path: coverPath}}
	for index, file := range files {
		name := filepath.Base(file)
		assets = append(assets, generatedPreviewAsset{name: name, path: file})
		thumbnails = append(thumbnails, VideoPreviewThumbnail{
			TimeSec: index * interval,
			URL:     name,
			Width:   160,
			Height:  90,
		})
	}
	return videoPreviewResultWithAssets{
		VideoPreviewResult: VideoPreviewResult{Thumbnails: thumbnails, DurationSec: duration},
		assets:             assets,
	}, nil
}

func (g FFmpegPreviewGenerator) objectKey(rawURL string) (string, bool) {
	trimmed := strings.TrimSpace(rawURL)
	prefix := strings.TrimRight(strings.TrimSpace(g.PublicBase), "/")
	if prefix != "" && strings.HasPrefix(trimmed, prefix+"/") {
		key := strings.TrimPrefix(trimmed, prefix+"/")
		return key, key != ""
	}
	return "", false
}

func (g FFmpegPreviewGenerator) publicURL(key string) string {
	return strings.TrimRight(g.PublicBase, "/") + "/" + strings.TrimLeft(key, "/")
}

func probeDuration(inputPath string) int {
	output, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", inputPath).Output()
	if err != nil {
		return 0
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	return int(seconds + 0.5)
}

func createFallbackThumbnail(coverPath, thumbnailPath string) error {
	output, err := exec.Command("ffmpeg", "-y", "-i", coverPath, "-vf", "scale=160:90", "-frames:v", "1", thumbnailPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create fallback thumbnail: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
