package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestConfiguredR2BookEvidenceObjectLifecycle(t *testing.T) {
	if os.Getenv("ATOMAN_R2_INTEGRATION") != "1" {
		t.Skip("set ATOMAN_R2_INTEGRATION=1 to run against the configured R2 bucket")
	}
	for _, key := range []string{"S3_ENDPOINT", "S3_BUCKET", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		require.NotEmpty(t, os.Getenv(key), "%s must be configured", key)
	}

	client, err := InitS3Client()
	require.NoError(t, err)
	bucket := os.Getenv("S3_BUCKET")
	key := fmt.Sprintf("books/private/publication-evidence/integration/%s.pdf", uuid.NewString())
	cleanup := func() {
		_, _ = client.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	}
	t.Cleanup(cleanup)

	first := []byte("%PDF-1.7 first evidence")
	_, err = client.PutObject(&s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(first), ContentType: aws.String("application/pdf")})
	require.NoError(t, err)

	readObject, err := client.GetObject(&s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	require.NoError(t, err)
	readFirst, err := io.ReadAll(readObject.Body)
	_ = readObject.Body.Close()
	require.NoError(t, err)
	require.Equal(t, first, readFirst)

	second := []byte("%PDF-1.7 replaced evidence")
	_, err = client.PutObject(&s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(second), ContentType: aws.String("application/pdf")})
	require.NoError(t, err)
	readObject, err = client.GetObject(&s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	require.NoError(t, err)
	readSecond, err := io.ReadAll(readObject.Body)
	_ = readObject.Body.Close()
	require.NoError(t, err)
	require.Equal(t, second, readSecond)

	_, err = client.DeleteObjectWithContext(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	require.NoError(t, err)
	_, err = client.GetObject(&s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	require.Error(t, err)
}
