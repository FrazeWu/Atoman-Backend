package music

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"atoman/internal/platform/apperr"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

type albumImportMultipartStore interface {
	CreateMultipartUpload(key string, contentType string) (string, error)
	PresignUploadPart(key string, uploadID string, partNumber int, expires time.Duration) (string, error)
	CompleteMultipartUpload(key string, uploadID string, parts []AlbumImportMultipartPartDTO) error
	AbortMultipartUpload(key string, uploadID string) error
	OpenObject(key string) (io.ReadCloser, error)
	DeleteObject(key string) error
}

type s3AlbumImportMultipartStore struct {
	client *s3.S3
	bucket string
}

func newS3AlbumImportMultipartStore(client *s3.S3) albumImportMultipartStore {
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if client == nil || bucket == "" {
		return nil
	}
	return &s3AlbumImportMultipartStore{client: client, bucket: bucket}
}

func (s *s3AlbumImportMultipartStore) CreateMultipartUpload(key string, contentType string) (string, error) {
	out, err := s.client.CreateMultipartUpload(&s3.CreateMultipartUploadInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", err
	}
	return aws.StringValue(out.UploadId), nil
}

func (s *s3AlbumImportMultipartStore) PresignUploadPart(key string, uploadID string, partNumber int, expires time.Duration) (string, error) {
	req, _ := s.client.UploadPartRequest(&s3.UploadPartInput{
		Bucket:     aws.String(s.bucket),
		Key:        aws.String(key),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int64(int64(partNumber)),
	})
	signedURL, err := req.Presign(expires)
	if err != nil {
		return "", err
	}
	if _, err := url.ParseRequestURI(signedURL); err != nil {
		return "", err
	}
	return signedURL, nil
}

func (s *s3AlbumImportMultipartStore) CompleteMultipartUpload(key string, uploadID string, parts []AlbumImportMultipartPartDTO) error {
	completedParts := make([]*s3.CompletedPart, 0, len(parts))
	for _, part := range parts {
		completedParts = append(completedParts, &s3.CompletedPart{
			ETag:       aws.String(part.ETag),
			PartNumber: aws.Int64(int64(part.PartNumber)),
		})
	}
	_, err := s.client.CompleteMultipartUpload(&s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
		MultipartUpload: &s3.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	return err
}

func (s *s3AlbumImportMultipartStore) AbortMultipartUpload(key string, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(&s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	return err
}

func (s *s3AlbumImportMultipartStore) OpenObject(key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func (s *s3AlbumImportMultipartStore) DeleteObject(key string) error {
	_, err := s.client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

func requireAlbumImportMultipartStore(store albumImportMultipartStore) error {
	if store == nil {
		return apperr.New(http.StatusServiceUnavailable, "storage.unavailable", "Storage is unavailable", nil)
	}
	return nil
}
