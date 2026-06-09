package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"atoman/internal/model"
)

type FFmpegPreviewGenerator struct {
	UploadsRoot string
	PublicBase  string
}

func (g FFmpegPreviewGenerator) Generate(video model.Video) ([]VideoPreviewThumbnail, error) {
	cleanURLPath := filepath.ToSlash(filepath.Clean(video.VideoURL))
	if !strings.HasPrefix(cleanURLPath, "/uploads/") {
		return nil, fmt.Errorf("preview worker only supports local uploads in V2: %s", video.VideoURL)
	}

	root := g.UploadsRoot
	if root == "" {
		root = "."
	}

	inputPath := filepath.Join(root, strings.TrimPrefix(cleanURLPath, "/"))
	uploadsRoot, err := filepath.Abs(filepath.Join(root, "uploads"))
	if err != nil {
		return nil, err
	}
	inputAbs, err := filepath.Abs(inputPath)
	if err != nil {
		return nil, err
	}
	relInput, err := filepath.Rel(uploadsRoot, inputAbs)
	if err != nil {
		return nil, err
	}
	if relInput == "." || strings.HasPrefix(relInput, ".."+string(os.PathSeparator)) || relInput == ".." || filepath.IsAbs(relInput) {
		return nil, fmt.Errorf("preview worker input escaped uploads root: %s", video.VideoURL)
	}

	outputDir := filepath.Join(root, "uploads", "video", "previews", video.UserID.String(), video.ID.String())
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, err
	}

	pattern := filepath.Join(outputDir, "thumb-%03d.webp")
	interval := 10
	if video.DurationSec > 1200 {
		interval = (video.DurationSec + 119) / 120
	}

	cmd := exec.Command("ffmpeg", "-y", "-i", inputPath, "-vf", fmt.Sprintf("fps=1/%d,scale=160:90", interval), "-frames:v", "120", pattern)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	files, err := filepath.Glob(filepath.Join(outputDir, "thumb-*.webp"))
	if err != nil {
		return nil, err
	}

	thumbnails := make([]VideoPreviewThumbnail, 0, len(files))
	for index, file := range files {
		url := "/uploads/video/previews/" + video.UserID.String() + "/" + video.ID.String() + "/" + filepath.Base(file)
		thumbnails = append(thumbnails, VideoPreviewThumbnail{
			TimeSec: index * interval,
			URL:     url,
			Width:   160,
			Height:  90,
		})
	}

	return thumbnails, nil
}
