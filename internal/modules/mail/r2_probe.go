package mail

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
)

// MailR2ObjectProbeAPI is purposefully limited to synthetic object operations.
type MailR2ObjectProbeAPI interface {
	PutObject(*s3.PutObjectInput) (*s3.PutObjectOutput, error)
	GetObject(*s3.GetObjectInput) (*s3.GetObjectOutput, error)
	DeleteObject(*s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error)
	HeadObject(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error)
}

// ProbePrivateMailObjects requires an explicit caller. No code invokes this
// automatically on production startup. It never accesses the public bucket.
func ProbePrivateMailObjects(client MailR2ObjectProbeAPI, cfg R2Config) error {
	if client == nil {
		return errors.New("Mail private R2 client unavailable")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	for _, bucket := range []string{cfg.BlobBucket, cfg.PendingBucket} {
		if err := probeOneMailBucket(client, bucket); err != nil {
			return fmt.Errorf("Private mail bucket %s verification failed: %w", bucket, err)
		}
	}
	return nil
}

func probeOneMailBucket(api MailR2ObjectProbeAPI, bucket string) (outErr error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return errors.New("Cannot create random mail storage test key")
	}
	key := "__atoman-mail-probe/" + hex.EncodeToString(nonce)
	payload := []byte("Atoman mail synthetic storage permission test " + hex.EncodeToString(nonce))
	digest := sha256.Sum256(payload)
	// Clean up on failure too. Never enumerate or overwrite other mail objects.
	defer func() {
		if outErr != nil {
			_, _ = api.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		}
	}()
	if _, err := api.PutObject(&s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(payload), ContentType: aws.String("application/octet-stream")}); err != nil {
		return errors.New("Synthetic PutObject failed")
	}
	item, err := api.GetObject(&s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil || item == nil || item.Body == nil {
		return errors.New("Synthetic GetObject failed")
	}
	data, readErr := io.ReadAll(io.LimitReader(item.Body, 1024))
	closeErr := item.Body.Close()
	if readErr != nil || closeErr != nil || sha256.Sum256(data) != digest {
		return errors.New("Synthetic GetObject integrity check failed")
	}
	if _, err := api.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		return errors.New("Synthetic DeleteObject failed")
	}
	if _, err := api.HeadObject(&s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err == nil {
		return errors.New("Synthetic test object still exists after deletion")
	} else {
		var failure awserr.RequestFailure
		if errors.As(err, &failure) && failure.StatusCode() == 404 {
			return nil
		}
		var awsErr awserr.Error
		if errors.As(err, &awsErr) && (awsErr.Code() == "NotFound" || awsErr.Code() == "NoSuchKey") {
			return nil
		}
		return errors.New("Synthetic test object deletion cannot be confirmed")
	}
}
