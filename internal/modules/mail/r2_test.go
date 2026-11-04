package mail

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

func validR2Env() map[string]string {
	return map[string]string{
		"S3_ENDPOINT":           "https://example-account.r2.cloudflarestorage.com",
		"S3_REGION":             "auto",
		"AWS_ACCESS_KEY_ID":     "example-access-key",
		"AWS_SECRET_ACCESS_KEY": "example-secret",
		"S3_BUCKET":             "atoman",
	}
}

func fromMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestR2ConfigUsesAtomanExistingCredentialsAndPrivateBuckets(t *testing.T) {
	m := validR2Env()
	cfg, err := LoadR2Config(fromMap(m))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessKeyID != m["AWS_ACCESS_KEY_ID"] || cfg.SecretKey != m["AWS_SECRET_ACCESS_KEY"] {
		t.Fatal("mail must reuse existing Atoman R2 credentials")
	}
	if cfg.BlobBucket != DefaultMailBlobBucket || cfg.PendingBucket != DefaultMailPendingBucket {
		t.Fatalf("unexpected mail buckets: %s, %s", cfg.BlobBucket, cfg.PendingBucket)
	}
	if cfg.BlobBucket == cfg.MediaBucket || cfg.PendingBucket == cfg.MediaBucket {
		t.Fatal("mail must not use Atoman public media bucket")
	}
	client, err := cfg.NewS3Client()
	if err != nil || client == nil {
		t.Fatalf("new mail R2 client: %v", err)
	}
}

func TestR2ConfigFailClosed(t *testing.T) {
	tests := []struct {
		name, key, value string
	}{
		{"no_endpoint", "S3_ENDPOINT", ""},
		{"insecure_endpoint", "S3_ENDPOINT", "http://example-account.r2.cloudflarestorage.com"},
		{"wrong_service", "S3_ENDPOINT", "https://example.invalid"},
		{"endpoint_path", "S3_ENDPOINT", "https://example-account.r2.cloudflarestorage.com/public"},
		{"user_info", "S3_ENDPOINT", "https://user:pass@example-account.r2.cloudflarestorage.com"},
		{"missing_key", "AWS_ACCESS_KEY_ID", ""},
		{"missing_secret", "AWS_SECRET_ACCESS_KEY", ""},
		{"wrong_region", "S3_REGION", "eu-west-1"},
		{"same_buckets", "MAIL_R2_PENDING_BUCKET", DefaultMailBlobBucket},
		{"media_bucket_blob", "MAIL_R2_BLOB_BUCKET", "atoman"},
		{"media_bucket_pending", "MAIL_R2_PENDING_BUCKET", "atoman"},
		{"invalid_bucket", "MAIL_R2_PENDING_BUCKET", "../private"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := validR2Env()
			m[tc.key] = tc.value
			if _, err := LoadR2Config(fromMap(m)); err == nil {
				t.Fatal("invalid mail R2 configuration was accepted")
			}
		})
	}
}

func TestR2ConfigCanSpecifyDifferentPrivateBuckets(t *testing.T) {
	m := validR2Env()
	m["MAIL_R2_BLOB_BUCKET"] = "atoman-mail-test-prod"
	m["MAIL_R2_PENDING_BUCKET"] = "atoman-mail-test-pending"
	cfg, err := LoadR2Config(fromMap(m))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BlobBucket != m["MAIL_R2_BLOB_BUCKET"] || cfg.PendingBucket != m["MAIL_R2_PENDING_BUCKET"] {
		t.Fatal("mail bucket overrides not respected")
	}
}

type fakeBucketHead struct {
	seen   []string
	failAt string
}

func (f *fakeBucketHead) HeadBucket(req *s3.HeadBucketInput) (*s3.HeadBucketOutput, error) {
	bucket := aws.StringValue(req.Bucket)
	f.seen = append(f.seen, bucket)
	if bucket == f.failAt {
		return nil, errors.New("access denied")
	}
	return &s3.HeadBucketOutput{}, nil
}

func TestVerifyBucketsOnlyHeadsPrivateMailBuckets(t *testing.T) {
	cfg, err := LoadR2Config(fromMap(validR2Env()))
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeBucketHead{}
	if err := VerifyBuckets(client, cfg); err != nil {
		t.Fatal(err)
	}
	want := []string{DefaultMailBlobBucket, DefaultMailPendingBucket}
	if !reflect.DeepEqual(client.seen, want) {
		t.Fatalf("bucket head checks: got %v, want %v", client.seen, want)
	}
}

func TestVerifyBucketsReportsPermissionFailureWithoutSecret(t *testing.T) {
	cfg, err := LoadR2Config(fromMap(validR2Env()))
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeBucketHead{failAt: DefaultMailPendingBucket}
	err = VerifyBuckets(client, cfg)
	if err == nil || !strings.Contains(err.Error(), DefaultMailPendingBucket) {
		t.Fatal("missing bucket permission error")
	}
	if strings.Contains(err.Error(), cfg.SecretKey) || strings.Contains(err.Error(), cfg.AccessKeyID) {
		t.Fatal("credential leaked into error")
	}
}

// This is a read-only production credential check, never run by default.
// It does not list, upload, download, or delete any objects.
func TestExistingAtomanCredentialsCanHeadPrivateMailBuckets(t *testing.T) {
	if os.Getenv("ATOMAN_MAIL_R2_INTEGRATION") != "1" {
		t.Skip("opt in with ATOMAN_MAIL_R2_INTEGRATION=1 in an authorized isolated environment")
	}
	cfg, err := LoadR2ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	client, err := cfg.NewS3Client()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBuckets(client, cfg); err != nil {
		t.Fatal(err)
	}
}
