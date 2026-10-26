package storage

import (
	"context"
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
)

func TestFailedMusicObjectDeletionIsDurableAndRetried(t *testing.T) {
	db := testdb.OpenPostgres(t, "music_cleanup_retry")
	testdb.Migrate(t, db, &model.MusicCleanupJob{})
	ConfigureMusicCleanupQueue(db)
	t.Cleanup(func() { ConfigureMusicCleanupQueue(nil) })
	t.Setenv("S3_BUCKET", "atoman-test")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if calls.Add(1) == 1 {
			http.Error(w, "temporary S3 failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := s3.New(session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"), Endpoint: aws.String(server.URL),
		S3ForcePathStyle: aws.Bool(true), DisableSSL: aws.Bool(true),
		Credentials: credentials.NewStaticCredentials("access", "secret", ""),
		MaxRetries:  aws.Int(0),
	})))
	key := "music/audio/users/test/cleanup.flac"
	DeleteMusicObjects(client, []string{key, key})
	var job model.MusicCleanupJob
	if err := db.Where("key = ?", key).First(&job).Error; err != nil {
		t.Fatalf("failed deletion was not persisted: %v", err)
	}
	if job.Key != key || calls.Load() != 1 {
		t.Fatalf("unexpected persisted cleanup job: %+v calls=%d", job, calls.Load())
	}
	handled, err := RunMusicObjectCleanupOnce(context.Background(), db, client)
	if err != nil || !handled {
		t.Fatalf("durable cleanup failed: handled=%v err=%v", handled, err)
	}
	var count int64
	if err := db.Model(&model.MusicCleanupJob{}).Where("key = ?", key).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || calls.Load() != 2 {
		t.Fatalf("cleanup job not acknowledged: remaining=%d calls=%d", count, calls.Load())
	}
}
