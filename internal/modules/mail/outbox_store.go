package mail

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

// MailPendingStore stores only encrypted snapshots in the *private* R2 bucket.
type MailPendingStore interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}

type r2PendingStore struct {
	client *s3.S3
	bucket string
}

func NewR2MailPendingStore(cfg R2Config) (MailPendingStore, error) {
	cli, e := cfg.NewS3Client()
	if e != nil {
		return nil, e
	}
	return &r2PendingStore{client: cli, bucket: cfg.PendingBucket}, nil
}
func (s *r2PendingStore) Put(ctx context.Context, key string, value []byte) error {
	if s == nil || s.client == nil || key == "" || len(value) == 0 || len(value) > maxMailSnapshotBytes+1024 {
		return errors.New("Invalid private mail snapshot")
	}
	_, e := s.client.PutObjectWithContext(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(value), ContentType: aws.String("application/octet-stream")})
	if e != nil {
		return errors.New("Private mail snapshot storage unavailable")
	}
	return nil
}
func (s *r2PendingStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s == nil || s.client == nil || key == "" {
		return nil, errors.New("Private mail snapshot storage unavailable")
	}
	item, e := s.client.GetObjectWithContext(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if e != nil || item == nil || item.Body == nil {
		return nil, errors.New("Private mail snapshot unavailable")
	}
	defer item.Body.Close()
	blob, e := io.ReadAll(io.LimitReader(item.Body, maxMailSnapshotBytes+1024+1))
	if e != nil || len(blob) > maxMailSnapshotBytes+1024 {
		return nil, errors.New("Private mail snapshot unreadable")
	}
	return blob, nil
}
func (s *r2PendingStore) Delete(ctx context.Context, key string) error {
	if s == nil || s.client == nil || key == "" {
		return errors.New("Private mail snapshot storage unavailable")
	}
	_, e := s.client.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if e != nil {
		return errors.New("Private mail snapshot removal unavailable")
	}
	return nil
}
