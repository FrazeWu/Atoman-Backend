package papersassets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/s3"
)

var ErrAlreadyExists = errors.New("papers assets: S3 conditional write found existing key")

// ConditionalObjectAPI is the minimal private-object contract. In particular
// PutIfAbsent MUST be atomic on the object store, not Head+Put.
type ConditionalObjectAPI interface {
	PutIfAbsent(context.Context, string, string, []byte, string) error
	Read(context.Context, string, string, int64) ([]byte, error)
}

// S3ConditionalAPI signs AWS SDK v1 requests AFTER inserting If-None-Match.
// It never sends ACL=public-read, issues deletes, or generates public URLs.
type S3ConditionalAPI struct {
	client *s3.S3
}

func NewS3ConditionalAPI(client *s3.S3) (*S3ConditionalAPI, error) {
	if client == nil {
		return nil, ErrInvalid
	}
	return &S3ConditionalAPI{client: client}, nil
}

func (a *S3ConditionalAPI) PutIfAbsent(ctx context.Context, bucket, key string, data []byte, contentType string) error {
	if a == nil || a.client == nil || ctx == nil || len(data) == 0 || len(data) > MaxSnapshotBytes+65536 {
		return ErrInvalid
	}
	req, _ := a.client.PutObjectRequest(&s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))),
		ContentType: aws.String(contentType),
	})
	req.SetContext(ctx)
	// The header is injected at the Build stage (before SigV4 Sign). An
	// unsupported or ignored conditional header is detected by Probe.
	req.Handlers.Build.PushBack(func(r *request.Request) {
		r.HTTPRequest.Header.Set("If-None-Match", "*")
	})
	if err := req.Send(); err != nil {
		var failure awserr.RequestFailure
		if errors.As(err, &failure) && failure.StatusCode() == 412 {
			return ErrAlreadyExists
		}
		return fmt.Errorf("private S3 conditional put: %w", err)
	}
	return nil
}

func (a *S3ConditionalAPI) Read(ctx context.Context, bucket, key string, maxBytes int64) ([]byte, error) {
	if a == nil || a.client == nil || ctx == nil || maxBytes < 1 || maxBytes > MaxSnapshotBytes+65536 {
		return nil, ErrInvalid
	}
	res, err := a.client.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	if res == nil || res.Body == nil {
		return nil, ErrIntegrity
	}
	defer res.Body.Close()
	if res.ContentLength != nil && (*res.ContentLength < 1 || *res.ContentLength > maxBytes) {
		return nil, ErrIntegrity
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || int64(len(data)) > maxBytes {
		return nil, ErrIntegrity
	}
	return data, nil
}

// VersioningEnabled is a minimum recovery prerequisite, not an Object Lock
// guarantee. The caller still must enforce no-delete and conditional-write
// bucket policies with a separately scoped production credential.
func (a *S3ConditionalAPI) VersioningEnabled(ctx context.Context, bucket string) (bool, error) {
	if a == nil || a.client == nil || ctx == nil {
		return false, ErrInvalid
	}
	output, err := a.client.GetBucketVersioningWithContext(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
	if err != nil {
		return false, err
	}
	return output != nil && aws.StringValue(output.Status) == "Enabled", nil
}

// AnonymousReadDenied sends an unsigned GetObject against an already-known
// existing canary key. Only HTTP 403 counts as evidence of denied public
// access. Other statuses are ambiguous, therefore unsafe for publication.
func (a *S3ConditionalAPI) AnonymousReadDenied(ctx context.Context, bucket, key string) (bool, error) {
	if a == nil || a.client == nil || ctx == nil {
		return false, ErrInvalid
	}
	req, out := a.client.GetObjectRequest(&s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	req.SetContext(ctx)
	req.Config.Credentials = credentials.AnonymousCredentials
	err := req.Send()
	if err == nil {
		if out != nil && out.Body != nil {
			out.Body.Close()
		}
		return false, nil
	}
	var failure awserr.RequestFailure
	if errors.As(err, &failure) && failure.StatusCode() == 403 {
		return true, nil
	}
	return false, fmt.Errorf("private S3 anonymous read check: %w", err)
}
