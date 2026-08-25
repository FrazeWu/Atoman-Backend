package blog

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

type s3ExportAssetReader struct {
	client *s3.S3
	bucket string
}

func NewS3ExportAssetReader(client *s3.S3) ExportAssetReader {
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if client == nil || bucket == "" {
		return nil
	}
	return &s3ExportAssetReader{client: client, bucket: bucket}
}

func (r *s3ExportAssetReader) ReadExportAsset(ctx context.Context, key string, size int64) (io.ReadCloser, error) {
	if strings.TrimSpace(key) == "" || size < 0 {
		return nil, fmt.Errorf("invalid export asset")
	}
	object, err := r.client.GetObjectWithContext(ctx, &s3.GetObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	if object.ContentLength != nil && *object.ContentLength > size {
		_ = object.Body.Close()
		return nil, fmt.Errorf("export asset exceeds recorded size")
	}
	return object.Body, nil
}
