//go:build linux

package paperspubgate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"atoman/internal/platform/papersassets"
	"github.com/aws/aws-sdk-go/aws/awserr"
)

// Transport is a drop-in TrustedArchiveTransport for an offline publisher.
// All publishing goes through an authenticated Unix socket; the embedded
// S3 client is read-only and cannot bypass the publication gateway.
type Transport struct {
	reader *papersassets.S3ConditionalAPI
	http   *http.Client
	socket string
	bucket string
}

var _ papersassets.TrustedArchiveTransport = (*Transport)(nil)

func NewTransport(socket, bucket string, reader *papersassets.S3ConditionalAPI) (*Transport, error) {
	if !filepath.IsAbs(socket) || reader == nil || bucket == "" {
		return nil, errors.New("invalid publisher transport")
	}
	netTransport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		},
	}
	return &Transport{reader: reader, http: &http.Client{Transport: netTransport}, socket: socket, bucket: bucket}, nil
}
func (t *Transport) publish(ctx context.Context, bucket, key string, body io.Reader, size int64) error {
	if t == nil || ctx == nil || bucket != t.bucket || size < 1 {
		return papersassets.ErrInvalid
	}
	_, _, maxBytes, err := classify(key)
	if err != nil || size > maxBytes {
		return papersassets.ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://papers-gateway/v1/objects/"+key, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("If-None-Match", "*")
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusCreated:
		return nil
	case http.StatusPreconditionFailed:
		return papersassets.ErrAlreadyExists
	case http.StatusTooManyRequests:
		return awserr.NewRequestFailure(awserr.New("SlowDown", "bounded publication gateway is busy", nil), 429, "papers-pubgate")
	default:
		return fmt.Errorf("publisher gateway rejected operation (HTTP %d)", resp.StatusCode)
	}
}
func (t *Transport) PutIfAbsent(ctx context.Context, bucket, key string, raw []byte, _ string) error {
	return t.publish(ctx, bucket, key, bytes.NewReader(raw), int64(len(raw)))
}
func (t *Transport) PutFileIfAbsent(ctx context.Context, bucket, key string, f *os.File, size int64) error {
	if f == nil {
		return papersassets.ErrInvalid
	}
	return t.publish(ctx, bucket, key, io.NewSectionReader(f, 0, size), size)
}
func (t *Transport) Read(ctx context.Context, bucket, key string, max int64) ([]byte, error) {
	if bucket != t.bucket {
		return nil, papersassets.ErrInvalid
	}
	return t.reader.Read(ctx, bucket, key, max)
}
func (t *Transport) VersioningEnabled(ctx context.Context, bucket string) (bool, error) {
	if bucket != t.bucket {
		return false, papersassets.ErrInvalid
	}
	return t.reader.VersioningEnabled(ctx, bucket)
}
func (t *Transport) AnonymousReadDenied(ctx context.Context, bucket, key string) (bool, error) {
	if bucket != t.bucket {
		return false, papersassets.ErrInvalid
	}
	return t.reader.AnonymousReadDenied(ctx, bucket, key)
}
func (t *Transport) LockCompliant(ctx context.Context, bucket string, days int64) (bool, error) {
	if bucket != t.bucket {
		return false, papersassets.ErrInvalid
	}
	return t.reader.LockCompliant(ctx, bucket, days)
}
func (t *Transport) OpenArchive(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	if bucket != t.bucket {
		return nil, 0, papersassets.ErrInvalid
	}
	return t.reader.OpenArchive(ctx, bucket, key)
}
