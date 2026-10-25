package storage

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPromoteMusicUploadAssetRequiresOwnerAndCopiesOwnedAsset(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.test/assets")
	t.Setenv("S3_BUCKET", "atoman-test")
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.MediaAsset{})

	owner := model.User{Username: "asset-owner-" + uuid.NewString()[:8], Email: uuid.NewString() + "@test.com", Password: "hash", IsActive: true}
	other := model.User{Username: "asset-other-" + uuid.NewString()[:8], Email: uuid.NewString() + "@test.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&other).Error)
	sourceKey := "music/covers/uploads/users/" + owner.UUID.String() + "/2026/10/cover.webp"
	assetURL := "https://cdn.example.test/assets/" + sourceKey
	require.NoError(t, db.Create(&model.MediaAsset{
		UserID: &owner.UUID, Purpose: "music.cover", URL: assetURL, Key: sourceKey,
		ContentType: "image/webp", Size: 32,
	}).Error)

	var copyCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		copyCalls.Add(1)
		if r.Header.Get("X-Amz-Copy-Source") != "atoman-test/"+sourceKey {
			t.Errorf("unexpected copy source %q", r.Header.Get("X-Amz-Copy-Source"))
		}
		if r.URL.Path != "/atoman-test/music/albums/album-1/covers/cover-1.webp" {
			t.Errorf("unexpected copy destination %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>"etag"</ETag><LastModified>2026-10-25T00:00:00Z</LastModified></CopyObjectResult>`)
	}))
	defer server.Close()
	s3Client := s3.New(session.Must(session.NewSession(&aws.Config{
		Region:           aws.String("us-east-1"),
		Endpoint:         aws.String(server.URL),
		S3ForcePathStyle: aws.Bool(true),
		Credentials:      credentials.NewStaticCredentials("access", "secret", ""),
		DisableSSL:       aws.Bool(true),
	})))

	destinationKey := "music/albums/album-1/covers/cover-1.webp"
	promoted, err := PromoteMusicUploadAsset(db, s3Client, owner.UUID, assetURL, destinationKey)
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example.test/assets/"+destinationKey, promoted.URL)
	require.Equal(t, sourceKey, promoted.SourceKey)
	require.Equal(t, destinationKey, promoted.DestinationKey)

	_, err = PromoteMusicUploadAsset(db, s3Client, other.UUID, assetURL, destinationKey)
	require.Error(t, err)
	require.EqualValues(t, 1, copyCalls.Load(), "a different user must not trigger an S3 copy")

	_, err = PromoteMusicUploadAsset(db, s3Client, owner.UUID, assetURL, "music/albums/album-1/tracks/song-1/audio.mp3")
	require.Error(t, err, "a cover upload must not be promoted to an audio target")
	require.EqualValues(t, 1, copyCalls.Load())
}
