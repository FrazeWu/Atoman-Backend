package papersassets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/s3"
)

// MaxTrustedArchiveBytes is under the 5 GiB single-PUT S3 limit.
// Larger future TeX environments require a separately audited conditional
// multipart completion path. Keep the initial path strictly bounded.
const MaxTrustedArchiveBytes int64 = 4 << 30

// TrustedArchiveTransport is a narrow, private-only binary transport.
// Neither interface nor implementation exposes delete, list, presign,
// object version promotion, or unconditional overwrite.
type TrustedArchiveTransport interface {
	ConditionalObjectAPI
	VersioningEnabled(context.Context, string) (bool, error)
	AnonymousReadDenied(context.Context, string, string) (bool, error)
	LockCompliant(context.Context, string, int64) (bool, error)
	PutFileIfAbsent(context.Context, string, string, *os.File, int64) error
	OpenArchive(context.Context, string, string) (io.ReadCloser, int64, error)
}

// PutFileIfAbsent signs a single conditional PutObject request with an
// os.File ReadSeeker: no full archive is ever loaded into memory.
func (a *S3ConditionalAPI) PutFileIfAbsent(ctx context.Context, bucket, key string, src *os.File, size int64) error {
	if a == nil || a.client == nil || ctx == nil || src == nil ||
		size < 1 || size > MaxTrustedArchiveBytes {
		return ErrInvalid
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	req, _ := a.client.PutObjectRequest(&s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Body: src, ContentLength: aws.Int64(size),
		ContentType: aws.String("application/x-tar"),
	})
	req.SetContext(ctx)
	req.Handlers.Build.PushBack(func(r *request.Request) {
		r.HTTPRequest.Header.Set("If-None-Match", "*")
	})
	if err := req.Send(); err != nil {
		var failure awserr.RequestFailure
		if errors.As(err, &failure) && failure.StatusCode() == 412 {
			return ErrAlreadyExists
		}
		return fmt.Errorf("trusted archive conditional S3 put: %w", err)
	}
	return nil
}

func (a *S3ConditionalAPI) OpenArchive(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	if a == nil || a.client == nil || ctx == nil {
		return nil, 0, ErrInvalid
	}
	out, err := a.client.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	if err != nil {
		return nil, 0, err
	}
	if out == nil || out.Body == nil {
		return nil, 0, ErrIntegrity
	}
	length := aws.Int64Value(out.ContentLength)
	if length > MaxTrustedArchiveBytes || length < 0 {
		out.Body.Close()
		return nil, 0, ErrIntegrity
	}
	return out.Body, length, nil
}

// LockCompliant requires the bucket's DEFAULT retention to be COMPLIANCE,
// not governance. A protected historical version alone does not guarantee
// that a current key cannot be shadowed or delete-marked: production still
// needs explicit denial of blind PUT and delete marker operations.
func (a *S3ConditionalAPI) LockCompliant(ctx context.Context, bucket string, minDays int64) (bool, error) {
	if a == nil || a.client == nil || ctx == nil || minDays < 1 || minDays > 36500 {
		return false, ErrInvalid
	}
	res, err := a.client.GetObjectLockConfigurationWithContext(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(bucket)})
	if err != nil {
		return false, err
	}
	if res == nil || res.ObjectLockConfiguration == nil {
		return false, nil
	}
	cfg := res.ObjectLockConfiguration
	if aws.StringValue(cfg.ObjectLockEnabled) != "Enabled" || cfg.Rule == nil ||
		cfg.Rule.DefaultRetention == nil {
		return false, nil
	}
	defaultRetention := cfg.Rule.DefaultRetention
	return aws.StringValue(defaultRetention.Mode) == "COMPLIANCE" &&
		aws.Int64Value(defaultRetention.Days) >= minDays, nil
}
