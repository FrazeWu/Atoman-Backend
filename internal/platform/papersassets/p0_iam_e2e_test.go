package papersassets

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func p0IAMClient(t *testing.T, access, secret string) *s3.S3 {
	t.Helper()
	_, endpoint := p0S3Client(t)
	require.True(t, strings.HasPrefix(access, "papersp0"))
	require.GreaterOrEqual(t, len(secret), 24)
	return s3.New(session.Must(session.NewSession(&aws.Config{
		Endpoint: aws.String(endpoint),
		Region:   aws.String("us-east-1"), DisableSSL: aws.Bool(true),
		S3ForcePathStyle: aws.Bool(true),
		Credentials:      credentials.NewStaticCredentials(access, secret, ""),
		MaxRetries:       aws.Int(0),
	})))
}
func TestP0IAMNonAdminWriterReaderAndWriteOnce(t *testing.T) {
	if os.Getenv("PAPERS_P0_IAM_CONFIRM") != "1" {
		t.Skip("ephemeral IAM policy test not enabled")
	}
	admin, _ := p0S3Client(t)
	bucket := os.Getenv("PAPERS_P0_IAM_BUCKET")
	require.True(t, strings.HasPrefix(bucket, "papers-private-p0-"))
	pub := p0IAMClient(t, os.Getenv("PAPERS_P0_IAM_PUBLISHER_USER"), os.Getenv("PAPERS_P0_IAM_PUBLISHER_SECRET"))
	reader := p0IAMClient(t, os.Getenv("PAPERS_P0_IAM_READER_USER"), os.Getenv("PAPERS_P0_IAM_READER_SECRET"))
	writerAPI, err := NewS3ConditionalAPI(pub)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	registry, err := NewPrivateS3Registry(writerAPI, bucket, "atoman-public")
	require.NoError(t, err)
	require.NoError(t, registry.Probe(ctx), "non-admin publisher requires only its granted bucket metadata, read and conditional writes")
	key := privateRegistryPrefix + "_iam/" + uuid.NewString()
	original := []byte("private-p0-conditionally-published")
	err = writerAPI.PutIfAbsent(ctx, bucket, key, original, "application/octet-stream")
	require.NoError(t, err)
	// Both conditional conflict and blind writes are separately tested:
	// only 412 on conditional collision is valid, never silent overwrite.
	err = writerAPI.PutIfAbsent(ctx, bucket, key, []byte("replace"), "application/octet-stream")
	require.ErrorIs(t, err, ErrAlreadyExists)
	_, err = pub.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Body: bytes.NewReader([]byte("blind overwrite forbidden")),
	})
	require.Error(t, err, "publisher must NOT be able to issue unconditional overwrite")
	blindNewKey := privateRegistryPrefix + "_iam/unconditional-new-" + uuid.NewString()
	_, err = pub.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(blindNewKey),
		Body: bytes.NewReader([]byte("blind new object forbidden")),
	})
	require.Error(t, err, "publisher must NOT be able to create a new object with blind PUT")
	existing, err := writerAPI.Read(ctx, bucket, key, 256)
	require.NoError(t, err)
	require.Equal(t, original, existing)
	// The non-admin reader may GET by an approved private digest path.
	readerAPI, err := NewS3ConditionalAPI(reader)
	require.NoError(t, err)
	got, err := readerAPI.Read(ctx, bucket, key, 256)
	require.NoError(t, err)
	require.Equal(t, original, got)
	_, err = reader.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(privateRegistryPrefix + "_iam/reader-forbidden"),
		Body: bytes.NewReader([]byte("no write rights")),
	})
	require.Error(t, err)
	_, err = reader.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	require.Error(t, err)
	_, err = pub.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	require.Error(t, err)
	// Protected archive versions cannot be removed by either user.
	current, err := admin.ListObjectVersionsWithContext(ctx, &s3.ListObjectVersionsInput{
		Bucket: aws.String(bucket), Prefix: aws.String(key),
	})
	require.NoError(t, err)
	require.Len(t, current.Versions, 1)
	require.Empty(t, current.DeleteMarkers)
	id := aws.StringValue(current.Versions[0].VersionId)
	_, err = pub.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), VersionId: aws.String(id),
	})
	require.Error(t, err)
	_, err = reader.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), VersionId: aws.String(id),
	})
	require.Error(t, err)
	// Protected data outside the approved prefix is invisible to both users.
	foreignKey := "outside-papers-" + uuid.NewString()
	_, err = admin.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(foreignKey),
		Body: bytes.NewReader([]byte("sibling data must not leak")),
	})
	require.NoError(t, err)
	_, err = pub.GetObjectWithContext(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(foreignKey)})
	require.Error(t, err)
	_, err = reader.GetObjectWithContext(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(foreignKey)})
	require.Error(t, err)
	verified, err := readerAPI.Read(ctx, bucket, key, 256)
	require.NoError(t, err)
	require.Equal(t, original, verified)
	fmt.Println("P0_NON_ADMIN_IAM_CONDITIONAL_ONLY_PUBLISH_READ_ONLY_READER_DELETE_REJECTED")
}

func TestP0IAMNonAdminMinimalRoles(t *testing.T) {
	if os.Getenv("PAPERS_P0_IAM_UNSAFE_TEST_MODE") != "1" {
		t.Skip("unsafe-provider demonstration not requested")
	}
	bucket := os.Getenv("PAPERS_P0_IAM_BUCKET")
	require.True(t, strings.HasPrefix(bucket, "papers-private-p0-"))
	writer := p0IAMClient(t, os.Getenv("PAPERS_P0_IAM_PUBLISHER_USER"), os.Getenv("PAPERS_P0_IAM_PUBLISHER_SECRET"))
	reader := p0IAMClient(t, os.Getenv("PAPERS_P0_IAM_READER_USER"), os.Getenv("PAPERS_P0_IAM_READER_SECRET"))
	admin, _ := p0S3Client(t)
	publisher, err := NewS3ConditionalAPI(writer)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	reg, err := NewPrivateS3Registry(publisher, bucket, "atoman-public")
	require.NoError(t, err)
	require.NoError(t, reg.Probe(ctx))
	key := privateRegistryPrefix + "_iam/role-" + uuid.NewString()
	content := []byte("non-admin-conditional-content")
	require.NoError(t, publisher.PutIfAbsent(ctx, bucket, key, content, "text/plain"))
	readAPI, err := NewS3ConditionalAPI(reader)
	require.NoError(t, err)
	got, err := readAPI.Read(ctx, bucket, key, 512)
	require.NoError(t, err)
	require.Equal(t, content, got)
	_, err = reader.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(privateRegistryPrefix + "_iam/reader-must-not-write"),
		Body: bytes.NewReader([]byte("forbidden")),
	})
	require.Error(t, err)
	for _, cl := range []*s3.S3{writer, reader} {
		_, err = cl.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		require.Error(t, err, "non-admin credentials must not create delete markers")
	}
	_, err = reader.GetObjectWithContext(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("outside-prefix-" + uuid.NewString())})
	require.Error(t, err)
	_, err = admin.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("outside-prefix-existing"),
		Body: bytes.NewReader([]byte("outside the reader prefix")),
	})
	require.NoError(t, err)
	_, err = reader.GetObjectWithContext(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("outside-prefix-existing")})
	require.Error(t, err)
	// Prove the precise gap: without s3:if-none-match IAM enforcement
	// this non-admin publisher can send an ordinary unconditional PUT.
	blindKey := privateRegistryPrefix + "_iam/blind-" + uuid.NewString()
	_, err = writer.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(blindKey), Body: bytes.NewReader([]byte("unconditional-put")),
	})
	require.NoError(t, err, "expected reproduction of unsupported MinIO policy condition")
	fmt.Println("P0_NONADMIN_ROLE_ISOLATION_PASS")
	fmt.Println("P0_SERVER_SIDE_CONDITIONAL_WRITE_IAM_GATE_FAILED_EXPECTED_BLIND_PUT_SUCCEEDED")
}
