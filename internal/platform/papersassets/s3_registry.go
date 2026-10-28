package papersassets

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
)

var (
	ErrConditionalUnsupported = errors.New("papers assets: S3 provider conditional writes not verified")
	bucketNameRE              = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
)

const (
	// Do not allow user-chosen object keys/prefixes, including '..' traversal.
	privateRegistryPrefix = "papers/immutable/v1/"
	maxManifestBytes      = MaxSnapshotBytes + 65536
)

// PrivateS3Registry is an internal, unexposed, private content-addressed
// manifest registry. It intentionally does not allow object deletion,
// public-read, presigned URLs, arbitrary key paths, or source-file uploads.
//
// Provider capability MUST be verified before publishing. Persistent
// immutability also requires bucket IAM to deny deletes and unconditional
// writes; code-level fencing alone cannot constrain bucket administrators.
// PrivateBucketAuditor enforces a minimal defense against accidentally
// selecting a public or non-versioned bucket. The actual production policy
// must additionally prohibit object deletion and nonconditional writes.
type PrivateBucketAuditor interface {
	VersioningEnabled(context.Context, string) (bool, error)
	AnonymousReadDenied(context.Context, string, string) (bool, error)
}

type PrivateS3Registry struct {
	client   ConditionalObjectAPI
	bucket   string
	verified atomic.Bool
}

func NewPrivateS3Registry(client ConditionalObjectAPI, bucket, sharedBucket string) (*PrivateS3Registry, error) {
	if client == nil || sharedBucket == "" || !bucketNameRE.MatchString(bucket) || bucket == sharedBucket ||
		bucket == "public" || strings.Contains(bucket, "..") ||
		strings.HasPrefix(bucket, ".") || strings.HasSuffix(bucket, ".") {
		return nil, ErrInvalid
	}
	return &PrivateS3Registry{client: client, bucket: bucket}, nil
}

func (r *PrivateS3Registry) objectKey(kind, digest string) (string, error) {
	if r == nil || !digestRE.MatchString(digest) {
		return "", ErrInvalid
	}
	if kind != "snapshots" && kind != "environments" {
		return "", ErrInvalid
	}
	return privateRegistryPrefix + kind + "/" + strings.TrimPrefix(digest, "sha256:") + ".json", nil
}

// Probe verifies conditional write semantics using a fresh random key in
// the private bucket. It does NOT delete the probe object: test with an
// ephemeral private bucket; production needs a lifecycle policy for probes.
func (r *PrivateS3Registry) Probe(ctx context.Context) error {
	if r == nil || r.client == nil || ctx == nil {
		return ErrInvalid
	}
	r.verified.Store(false)
	auditor, ok := r.client.(PrivateBucketAuditor)
	if !ok {
		return ErrConditionalUnsupported
	}
	versioned, err := auditor.VersioningEnabled(ctx, r.bucket)
	if err != nil || !versioned {
		return fmt.Errorf("%w: private bucket versioning not verified: %v", ErrConditionalUnsupported, err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	key := privateRegistryPrefix + "_probes/" + hex.EncodeToString(nonce[:])
	first := []byte("papers-conditional-probe/v1/first/" + hex.EncodeToString(nonce[:]))
	second := []byte("papers-conditional-probe/v1/second/" + hex.EncodeToString(nonce[:]))
	if err := r.client.PutIfAbsent(ctx, r.bucket, key, first, "application/octet-stream"); err != nil {
		return fmt.Errorf("%w: initial write: %v", ErrConditionalUnsupported, err)
	}
	secondErr := r.client.PutIfAbsent(ctx, r.bucket, key, second, "application/octet-stream")
	if !errors.Is(secondErr, ErrAlreadyExists) {
		return fmt.Errorf("%w: second write did not return HTTP 412", ErrConditionalUnsupported)
	}
	read, err := r.client.Read(ctx, r.bucket, key, 256)
	if err != nil || !bytes.Equal(read, first) {
		return fmt.Errorf("%w: probe object was overwritten or not readable: %v", ErrConditionalUnsupported, err)
	}
	denied, err := auditor.AnonymousReadDenied(ctx, r.bucket, key)
	if err != nil || !denied {
		return fmt.Errorf("%w: anonymous read is not provably denied: %v", ErrConditionalUnsupported, err)
	}
	r.verified.Store(true)
	return nil
}

func retryableConditionalError(err error) bool {
	var failure awserr.RequestFailure
	if errors.As(err, &failure) {
		switch failure.StatusCode() {
		case 409, 429, 500, 502, 503, 504:
			return true
		}
	}
	var awsErr awserr.Error
	if errors.As(err, &awsErr) {
		switch awsErr.Code() {
		case "RequestError", "RequestTimeout", "RequestTimeoutException", "SlowDown", "InternalError", "ServiceUnavailable", "ConditionalRequestConflict":
			return true
		}
	}
	return false
}

func (r *PrivateS3Registry) publish(ctx context.Context, kind string, raw []byte) (string, error) {
	if r == nil || r.client == nil || ctx == nil {
		return "", ErrInvalid
	}
	if !r.verified.Load() {
		return "", ErrConditionalUnsupported
	}
	if len(raw) < 2 || len(raw) > maxManifestBytes {
		return "", ErrInvalid
	}
	digest := HashBytes(raw)
	key, err := r.objectKey(kind, digest)
	if err != nil {
		return "", err
	}
	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		putErr := r.client.PutIfAbsent(ctx, r.bucket, key, raw, "application/json")
		if putErr != nil && !errors.Is(putErr, ErrAlreadyExists) && !retryableConditionalError(putErr) {
			// A missing write permission is never convertible to "success"
			// merely because a readable matching object already exists.
			return "", putErr
		}
		// All PUT outcomes are ambiguous until a separate authenticated
		// GET confirms the exact immutable bytes, even on HTTP 200.
		got, getErr := r.load(ctx, kind, digest)
		if getErr == nil {
			if !bytes.Equal(got, raw) {
				return "", ErrIntegrity
			}
			return digest, nil
		}
		if errors.Is(getErr, ErrIntegrity) {
			return "", getErr // visible poisoned key: stop immediately
		}
		if putErr != nil && !errors.Is(putErr, ErrAlreadyExists) && !retryableConditionalError(putErr) {
			return "", putErr // permissions, invalid request, etc. cannot be retried
		}
		if attempt == maxAttempts-1 {
			if putErr != nil {
				return "", errors.Join(putErr, getErr)
			}
			return "", getErr
		}
		backoff := time.Duration(40*(1<<attempt)) * time.Millisecond
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", ErrIntegrity
}

func (r *PrivateS3Registry) load(ctx context.Context, kind, digest string) ([]byte, error) {
	if r == nil || r.client == nil || ctx == nil {
		return nil, ErrInvalid
	}
	key, err := r.objectKey(kind, digest)
	if err != nil {
		return nil, err
	}
	got, err := r.client.Read(ctx, r.bucket, key, maxManifestBytes)
	if err != nil {
		return nil, err
	}
	if HashBytes(got) != digest {
		return nil, ErrIntegrity
	}
	return got, nil
}

// Snapshot and environment API only expose canonical, validated objects.
// The caller must separately authorize the current project and digest
// binding using PostgreSQL before requesting these private assets.
func (r *PrivateS3Registry) PublishSnapshot(ctx context.Context, snapshot Snapshot) (string, error) {
	snapshot.Files = append([]SourceFile(nil), snapshot.Files...)
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	if err := snapshot.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return r.publish(ctx, "snapshots", raw)
}

func (r *PrivateS3Registry) LoadSnapshot(ctx context.Context, digest string) (Snapshot, error) {
	var snapshot Snapshot
	raw, err := r.load(ctx, "snapshots", digest)
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("%w: invalid snapshot manifest: %v", ErrIntegrity, err)
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("%w: invalid snapshot: %v", ErrIntegrity, err)
	}
	return snapshot, nil
}

func (r *PrivateS3Registry) PublishEnvironment(ctx context.Context, env Environment) (string, error) {
	if err := env.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return r.publish(ctx, "environments", raw)
}

func (r *PrivateS3Registry) LoadEnvironment(ctx context.Context, digest string) (Environment, error) {
	var env Environment
	raw, err := r.load(ctx, "environments", digest)
	if err != nil {
		return env, err
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return Environment{}, fmt.Errorf("%w: invalid environment manifest: %v", ErrIntegrity, err)
	}
	if err := env.Validate(); err != nil {
		return Environment{}, fmt.Errorf("%w: invalid environment: %v", ErrIntegrity, err)
	}
	return env, nil
}
