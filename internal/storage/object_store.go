package storage

import (
	"io"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

// ObjectWriter is the smallest shared contract needed by ordinary uploads.
type ObjectWriter interface {
	PutObject(*s3.PutObjectInput) (*s3.PutObjectOutput, error)
}

// ObjectDeleter is the smallest shared contract needed by object cleanup.
type ObjectDeleter interface {
	DeleteObject(*s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error)
}

func PutPublicObject(store ObjectWriter, bucket, key, contentType string, body io.ReadSeeker) error {
	_, err := store.PutObject(&s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        body,
		ContentType: aws.String(contentType),
		ACL:         aws.String("public-read"),
	})
	return err
}

func DeleteObject(store ObjectDeleter, bucket, key string) error {
	_, err := store.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	return err
}
