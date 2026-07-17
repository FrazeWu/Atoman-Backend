package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

func TestParseObjectURL(t *testing.T) {
	key, ok := parseObjectURL("http://localhost:9100/atoman-assets/video/files/u1/file.mp4", "http://localhost:9100/atoman-assets")
	if !ok {
		t.Fatal("expected URL to parse")
	}
	if key != "video/files/u1/file.mp4" {
		t.Fatalf("unexpected key: %s", key)
	}
}

func TestBuildLegacyVideoKey(t *testing.T) {
	createdAt := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)

	next, ok := buildLegacyVideoKey("video/files/u1/file.mp4", "u1", "files", createdAt)

	if !ok {
		t.Fatal("expected legacy video key")
	}
	if next != "video/files/users/u1/2026/05/file.mp4" {
		t.Fatalf("unexpected key: %s", next)
	}
}

func TestBuildMusicAudioKey(t *testing.T) {
	next := buildMusicAudioKey("music/kanye_west/2049/01 INTRO.wav", "album-1", "song-1")

	if next != "music/albums/album-1/tracks/song-1.wav" {
		t.Fatalf("unexpected key: %s", next)
	}
}

func TestBuildMusicCoverKey(t *testing.T) {
	next := buildMusicCoverKey("music/kanye_west/2049/cover.jpg", "album-1")

	if next != "music/albums/album-1/cover.jpg" {
		t.Fatalf("unexpected key: %s", next)
	}
}

func TestShouldMigrateMusicKey(t *testing.T) {
	if !shouldMigrateMusicKey("music/audio/uploads/users/user-1/2026/07/song.mp3") {
		t.Fatal("expected upload object to require migration")
	}
	if shouldMigrateMusicKey("music/albums/album-1/tracks/song-1.mp3") {
		t.Fatal("expected album object to be current")
	}
}

func TestCopyObjectDoesNotSetACL(t *testing.T) {
	sess := session.Must(session.NewSession(&aws.Config{
		Credentials:      credentials.NewStaticCredentials("key", "secret", ""),
		DisableSSL:       aws.Bool(true),
		Endpoint:         aws.String("http://localhost"),
		Region:           aws.String("us-east-1"),
		S3ForcePathStyle: aws.Bool(true),
	}))
	client := s3.New(sess)
	var input *s3.CopyObjectInput
	client.Handlers.Send.Clear()
	client.Handlers.Send.PushBack(func(r *request.Request) {
		input = r.Params.(*s3.CopyObjectInput)
		r.HTTPResponse = &http.Response{
			StatusCode: 200,
			Body: io.NopCloser(strings.NewReader(`<CopyObjectResult>
	<ETag>"etag"</ETag>
	<LastModified>2026-01-01T00:00:00Z</LastModified>
</CopyObjectResult>`)),
		}
	})

	if err := copyObject(client, "bucket", "old key/file.mp4", "new-key.mp4"); err != nil {
		t.Fatalf("copyObject returned error: %v", err)
	}
	if input == nil {
		t.Fatal("expected CopyObjectInput")
	}
	if input.ACL != nil {
		t.Fatalf("expected ACL to be nil, got %q", aws.StringValue(input.ACL))
	}
}
