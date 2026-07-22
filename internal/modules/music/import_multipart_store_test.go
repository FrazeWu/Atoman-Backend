package music

import (
	"testing"

	"github.com/aws/aws-sdk-go/service/s3"
)

func TestNewS3AlbumImportMultipartStoreUsesSourceBucketWithLegacyFallback(t *testing.T) {
	tests := []struct {
		name         string
		sourceBucket string
		legacyBucket string
		wantBucket   string
	}{
		{name: "source bucket", sourceBucket: "music-source", legacyBucket: "legacy", wantBucket: "music-source"},
		{name: "legacy fallback", legacyBucket: "legacy", wantBucket: "legacy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MUSIC_SOURCE_BUCKET", test.sourceBucket)
			t.Setenv("S3_BUCKET", test.legacyBucket)
			store, ok := newS3AlbumImportMultipartStore(&s3.S3{}).(*s3AlbumImportMultipartStore)
			if !ok || store.bucket != test.wantBucket {
				t.Fatalf("expected bucket %q, got %#v", test.wantBucket, store)
			}
		})
	}
}

func TestNewMusicImportMediaStoreUsesDedicatedPlaybackBucketWithFallback(t *testing.T) {
	t.Setenv("MUSIC_SOURCE_BUCKET", "source")
	t.Setenv("MUSIC_PLAYBACK_BUCKET", "playback")
	t.Setenv("S3_BUCKET", "legacy")
	store, ok := NewMusicImportMediaStore(&s3.S3{}).(*s3MediaImportStore)
	source, sourceOK := store.source.(*s3AlbumImportMultipartStore)
	playback, playbackOK := store.playback.(*s3AlbumImportMultipartStore)
	if !ok || !sourceOK || !playbackOK || source.bucket != "source" || playback.bucket != "playback" {
		t.Fatalf("unexpected dedicated store: %#v", store)
	}
	t.Setenv("MUSIC_PLAYBACK_BUCKET", "")
	store = NewMusicImportMediaStore(&s3.S3{}).(*s3MediaImportStore)
	playback = store.playback.(*s3AlbumImportMultipartStore)
	if playback.bucket != "legacy" {
		t.Fatalf("fallback bucket = %q", playback.bucket)
	}
}
