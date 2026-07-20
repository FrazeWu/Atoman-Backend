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
