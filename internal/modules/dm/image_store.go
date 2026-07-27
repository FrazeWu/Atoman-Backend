package dm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

func NewImageStoreFromEnv(client *s3.S3) ImageStore {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("STORAGE_TYPE")), "local") {
		root := strings.TrimSpace(os.Getenv("DM_LOCAL_DIR"))
		if root == "" {
			root = ".data/dm"
		}
		return &localImageStore{root: root}
	}
	bucket := strings.TrimSpace(os.Getenv("DM_S3_BUCKET"))
	if client == nil || bucket == "" {
		return unavailableImageStore{}
	}
	return &s3ImageStore{client: client, bucket: bucket}
}

type localImageStore struct{ root string }

func (s *localImageStore) path(key string) (string, error) {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") {
		return "", fmt.Errorf("invalid private image key")
	}
	value := filepath.Join(s.root, filepath.FromSlash(key))
	root, err := filepath.Abs(s.root)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("private image key escapes root")
	}
	return abs, nil
}

func (s *localImageStore) Put(_ context.Context, key, _ string, body io.Reader, _ int64) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func (s *localImageStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}
func (s *localImageStore) SignedURL(context.Context, string, time.Duration) (string, error) {
	return "", fmt.Errorf("local images require authenticated read")
}
func (s *localImageStore) Delete(_ context.Context, key string) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	return os.Remove(path)
}
func (s *localImageStore) IsLocal() bool { return true }

type s3ImageStore struct {
	client *s3.S3
	bucket string
}

func (s *s3ImageStore) Put(_ context.Context, key, contentType string, body io.Reader, _ int64) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(&s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(data), ContentType: aws.String(contentType)})
	return err
}
func (s *s3ImageStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(&s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}
func (s *s3ImageStore) SignedURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	request, _ := s.client.GetObjectRequest(&s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return request.Presign(ttl)
}
func (s *s3ImageStore) Delete(_ context.Context, key string) error {
	_, err := s.client.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}
func (s *s3ImageStore) IsLocal() bool { return false }

type unavailableImageStore struct{}

func (unavailableImageStore) Put(context.Context, string, string, io.Reader, int64) error {
	return ErrStorageUnavailable
}
func (unavailableImageStore) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, ErrStorageUnavailable
}
func (unavailableImageStore) SignedURL(context.Context, string, time.Duration) (string, error) {
	return "", ErrStorageUnavailable
}
func (unavailableImageStore) Delete(context.Context, string) error { return ErrStorageUnavailable }
func (unavailableImageStore) IsLocal() bool                        { return false }
