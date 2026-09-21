package service

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"

	"atoman/internal/model"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
)

func TestNeedsVideoPreviewJobForR2Video(t *testing.T) {
	video := model.Video{
		Base:        model.Base{ID: uuid.New()},
		StorageType: "local",
		VideoURL:    "https://cdn.example.com/video/imports/user/source.mp4",
	}

	if !needsVideoPreviewJob(video) {
		t.Fatal("expected an R2-backed video to require preview processing")
	}
}

func TestNeedsVideoPreviewJobSkipsExternalVideo(t *testing.T) {
	video := model.Video{StorageType: "external", VideoURL: "https://youtube.com/watch?v=video"}

	if needsVideoPreviewJob(video) {
		t.Fatal("expected an external video to skip preview processing")
	}
}

func TestFFmpegPreviewGeneratorResolvesR2ObjectKey(t *testing.T) {
	generator := FFmpegPreviewGenerator{PublicBase: "https://cdn.example.com/assets"}
	key, ok := generator.objectKey("https://cdn.example.com/assets/video/imports/source.mp4")
	if !ok || key != "video/imports/source.mp4" {
		t.Fatalf("expected R2 object key, got %q (ok=%t)", key, ok)
	}
}

type previewObjectStoreFake struct {
	source []byte
	keys   []string
}

func (s *previewObjectStoreFake) GetObject(input *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
	if aws.StringValue(input.Key) != "video/imports/source.mp4" {
		return nil, fmt.Errorf("unexpected source key: %s", aws.StringValue(input.Key))
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(s.source))}, nil
}

func (s *previewObjectStoreFake) PutObject(input *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
	s.keys = append(s.keys, aws.StringValue(input.Key))
	return &s3.PutObjectOutput{}, nil
}

func TestFFmpegPreviewGeneratorUploadsR2CoverAndThumbnails(t *testing.T) {
	workingDir := t.TempDir()
	sourcePath := workingDir + "/source.mp4"
	if output, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x180:d=1", "-c:v", "libx264", "-pix_fmt", "yuv420p", sourcePath).CombinedOutput(); err != nil {
		t.Fatalf("create test video: %v: %s", err, output)
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	store := &previewObjectStoreFake{source: source}
	generator := FFmpegPreviewGenerator{Store: store, Bucket: "videos", PublicBase: "https://cdn.example.com"}
	video := model.Video{
		Base:        model.Base{ID: uuid.New()},
		UserID:      uuid.New(),
		StorageType: "local",
		VideoURL:    "https://cdn.example.com/video/imports/source.mp4",
	}

	result, err := generator.GenerateWithMetadata(video)
	if err != nil {
		t.Fatal(err)
	}
	if result.ThumbnailURL == "" || len(result.Thumbnails) == 0 || result.DurationSec != 1 {
		t.Fatalf("unexpected preview result: %#v", result)
	}
	if len(store.keys) < 2 {
		t.Fatalf("expected cover and thumbnail uploads, got %v", store.keys)
	}
}
