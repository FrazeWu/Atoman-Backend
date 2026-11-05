package mail

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

const (
	DefaultMailBlobBucket    = "atoman-mail-prod"
	DefaultMailPendingBucket = "atoman-mail-pending"
)

var bucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// R2Config reuses the existing Atoman S3 credentials without exposing them to
// browser clients or sharing the public media bucket with private email.
// Stalwart connects to the blob bucket independently; Atoman owns pending jobs.
type R2Config struct {
	Endpoint      string
	Region        string
	AccessKeyID   string
	SecretKey     string
	MediaBucket   string
	BlobBucket    string
	PendingBucket string
}

func LoadR2ConfigFromEnv() (R2Config, error) {
	return LoadR2Config(os.Getenv)
}

// LoadR2Config accepts a lookup function to make configuration tests hermetic.
func LoadR2Config(lookup func(string) string) (R2Config, error) {
	if lookup == nil {
		return R2Config{}, fmt.Errorf("mail R2 environment lookup is missing")
	}
	get := func(name string) string { return strings.TrimSpace(lookup(name)) }
	region := get("S3_REGION")
	if region == "" {
		region = "auto"
	}
	blobBucket := get("MAIL_R2_BLOB_BUCKET")
	if blobBucket == "" {
		blobBucket = DefaultMailBlobBucket
	}
	pendingBucket := get("MAIL_R2_PENDING_BUCKET")
	if pendingBucket == "" {
		pendingBucket = DefaultMailPendingBucket
	}
	accessKey, secretKey := get("ATOMAN_MAIL_R2_ACCESS_KEY_ID"), get("ATOMAN_MAIL_R2_SECRET_ACCESS_KEY")
	if accessKey == "" && secretKey == "" {
		// Compatibility for existing installations. Production release gates
		// still require verifying access to both private mail buckets.
		accessKey, secretKey = get("AWS_ACCESS_KEY_ID"), get("AWS_SECRET_ACCESS_KEY")
	} else if accessKey == "" || secretKey == "" {
		return R2Config{}, fmt.Errorf("both dedicated mail R2 credentials must be configured")
	}
	cfg := R2Config{
		Endpoint: get("S3_ENDPOINT"), Region: region,
		AccessKeyID: accessKey, SecretKey: secretKey,
		MediaBucket: get("S3_BUCKET"), BlobBucket: blobBucket, PendingBucket: pendingBucket,
	}
	return cfg, cfg.Validate()
}

func (c R2Config) Validate() error {
	if c.Endpoint == "" || c.AccessKeyID == "" || c.SecretKey == "" {
		return fmt.Errorf("Atoman R2 endpoint or existing credentials are missing")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u == nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
		!strings.HasSuffix(strings.ToLower(u.Hostname()), ".r2.cloudflarestorage.com") {
		return fmt.Errorf("Atoman mail requires a private HTTPS Cloudflare R2 S3 endpoint")
	}
	if c.Region != "auto" && c.Region != "us-east-1" {
		return fmt.Errorf("mail R2 region must be auto or us-east-1")
	}
	for _, name := range []string{c.BlobBucket, c.PendingBucket} {
		if !bucketNamePattern.MatchString(name) {
			return fmt.Errorf("invalid mail R2 bucket name")
		}
	}
	if c.BlobBucket == c.PendingBucket || c.BlobBucket == c.MediaBucket || c.PendingBucket == c.MediaBucket {
		return fmt.Errorf("mail R2 buckets must be distinct and separate from Atoman public media storage")
	}
	return nil
}

// NewS3Client intentionally does not call the existing media InitS3Client or
// ValidateS3Connection: those may use the public bucket or auto-create one.
func (c R2Config) NewS3Client() (*s3.S3, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	sess, err := session.NewSession(&aws.Config{
		Region:           aws.String(c.Region),
		Endpoint:         aws.String(c.Endpoint),
		S3ForcePathStyle: aws.Bool(true),
		Credentials:      credentials.NewStaticCredentials(c.AccessKeyID, c.SecretKey, ""),
		HTTPClient:       &http.Client{Timeout: 10 * time.Second},
		MaxRetries:       aws.Int(2),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize mail R2 client: %w", err)
	}
	return s3.New(sess), nil
}

// BucketHeadAPI is the only capability required for a read-only access check.
// No code path here creates buckets or changes R2 objects.
type BucketHeadAPI interface {
	HeadBucket(*s3.HeadBucketInput) (*s3.HeadBucketOutput, error)
}

func VerifyBuckets(client BucketHeadAPI, cfg R2Config) error {
	if client == nil {
		return fmt.Errorf("mail R2 client is not configured")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	for _, bucket := range []string{cfg.BlobBucket, cfg.PendingBucket} {
		if _, err := client.HeadBucket(&s3.HeadBucketInput{Bucket: aws.String(bucket)}); err != nil {
			return fmt.Errorf("mail R2 bucket %s is unavailable or existing credentials lack access: %w", bucket, err)
		}
	}
	return nil
}
