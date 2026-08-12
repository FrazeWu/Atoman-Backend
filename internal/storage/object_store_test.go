package storage

import (
	"bytes"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

type recordingObjectStore struct {
	put    *s3.PutObjectInput
	delete *s3.DeleteObjectInput
}

func (s *recordingObjectStore) PutObject(input *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
	s.put = input
	return &s3.PutObjectOutput{}, nil
}

func (s *recordingObjectStore) DeleteObject(input *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error) {
	s.delete = input
	return &s3.DeleteObjectOutput{}, nil
}

func TestObjectStoreHelpersBuildRequests(t *testing.T) {
	store := &recordingObjectStore{}
	if err := PutPublicObject(store, "bucket", "key", "video/mp4", bytes.NewReader([]byte("body"))); err != nil {
		t.Fatalf("put object: %v", err)
	}
	if aws.StringValue(store.put.Bucket) != "bucket" || aws.StringValue(store.put.Key) != "key" || aws.StringValue(store.put.ACL) != "public-read" {
		t.Fatalf("unexpected put request: %#v", store.put)
	}
	if err := DeleteObject(store, "bucket", "key"); err != nil {
		t.Fatalf("delete object: %v", err)
	}
	if aws.StringValue(store.delete.Bucket) != "bucket" || aws.StringValue(store.delete.Key) != "key" {
		t.Fatalf("unexpected delete request: %#v", store.delete)
	}
}
