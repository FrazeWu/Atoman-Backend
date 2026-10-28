package papersassets

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func p0S3Client(t *testing.T) (*s3.S3, string) {
	t.Helper()
	if os.Getenv("PAPERS_P0_S3_CONFIRM") != "1" {
		t.Skip("disposable P0 MinIO test not requested")
	}
	endpoint := os.Getenv("PAPERS_P0_S3_TEST_ENDPOINT")
	u, err := url.Parse(endpoint)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	require.Equal(t, "http", u.Scheme)
	require.Equal(t, "127.0.0.1", u.Hostname())
	require.Empty(t, u.Path)
	require.Empty(t, u.RawQuery)
	require.Greater(t, port, 1024)
	require.NotEqual(t, 9000, port)
	require.NotEqual(t, 9100, port)
	require.NotEqual(t, 9101, port)
	id := os.Getenv("PAPERS_P0_S3_TEST_ACCESS_ID")
	secret := os.Getenv("PAPERS_P0_S3_TEST_SECRET")
	require.True(t, strings.HasPrefix(id, "papersp0"))
	require.GreaterOrEqual(t, len(secret), 24)
	sess := session.Must(session.NewSession(&aws.Config{
		Endpoint:   aws.String(endpoint),
		Region:     aws.String("us-east-1"),
		DisableSSL: aws.Bool(true), S3ForcePathStyle: aws.Bool(true),
		Credentials: credentials.NewStaticCredentials(id, secret, ""),
		MaxRetries:  aws.Int(0),
	}))
	return s3.New(sess), endpoint
}

func TestP0RealMinIOConditionalRegistry(t *testing.T) {
	cli, endpoint := p0S3Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	nonce := make([]byte, 7)
	_, err := rand.Read(nonce)
	require.NoError(t, err)
	bucket := "papers-private-p0-" + hex.EncodeToString(nonce)
	_, err = cli.CreateBucketWithContext(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = cli.PutBucketVersioningWithContext(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucket), VersioningConfiguration: &s3.VersioningConfiguration{Status: aws.String("Enabled")},
	})
	require.NoError(t, err)
	versioning, err := cli.GetBucketVersioningWithContext(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Equal(t, "Enabled", aws.StringValue(versioning.Status))

	provider, err := NewS3ConditionalAPI(cli)
	require.NoError(t, err)
	reg, err := NewPrivateS3Registry(provider, bucket, "atoman-shared-public")
	require.NoError(t, err)
	require.ErrorIs(t, func() error { _, e := reg.PublishSnapshot(ctx, fixtureSnapshot()); return e }(), ErrConditionalUnsupported)
	require.NoError(t, reg.Probe(ctx))

	snap := fixtureSnapshot()
	const concurrent = 24
	var wg sync.WaitGroup
	digests := make(chan string, concurrent)
	failures := make(chan error, concurrent)
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			digest, err := reg.PublishSnapshot(ctx, snap)
			if err != nil {
				failures <- err
				return
			}
			digests <- digest
		}()
	}
	wg.Wait()
	close(digests)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	var digest string
	count := 0
	for next := range digests {
		if digest == "" {
			digest = next
		}
		require.Equal(t, digest, next)
		count++
	}
	require.Equal(t, concurrent, count)
	loaded, err := reg.LoadSnapshot(ctx, digest)
	require.NoError(t, err)
	require.Equal(t, snap.ProjectID, loaded.ProjectID)
	require.Len(t, loaded.Files, len(snap.Files))
	key, err := reg.objectKey("snapshots", digest)
	require.NoError(t, err)

	// Same-key conditional write MUST return PreconditionFailed (HTTP 412).
	err = provider.PutIfAbsent(ctx, bucket, key, []byte("different bytes"), "application/json")
	require.ErrorIs(t, err, ErrAlreadyExists)
	stored, err := provider.Read(ctx, bucket, key, maxManifestBytes)
	require.NoError(t, err)
	require.Equal(t, digest, HashBytes(stored))

	// An unauthenticated GET of a private object must never expose its bytes.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/"+bucket+"/"+key, nil)
	require.NoError(t, err)
	anonymous, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer anonymous.Body.Close()
	require.Equal(t, http.StatusForbidden, anonymous.StatusCode, "private object must not be anonymously readable")
	_, err = io.Copy(io.Discard, io.LimitReader(anonymous.Body, 1024))
	require.NoError(t, err)

	envDigest, err := reg.PublishEnvironment(ctx, testEnv())
	require.NoError(t, err)
	env, err := reg.LoadEnvironment(ctx, envDigest)
	require.NoError(t, err)
	require.Equal(t, testEnv(), env)

	// Test-only privileged tamper: an administrator can still overwrite a
	// versioned key without bucket policy / Object Lock. Registry MUST
	// detect corrupted current bytes instead of accepting a valid-looking key.
	_, err = cli.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Body: bytes.NewReader([]byte("admin modified current version")),
	})
	require.NoError(t, err)
	_, err = reg.LoadSnapshot(ctx, digest)
	require.ErrorIs(t, err, ErrIntegrity)
	_, err = reg.PublishSnapshot(ctx, snap)
	require.ErrorIs(t, err, ErrIntegrity)
	fmt.Printf("P0_S3_CONDITIONAL_24_WRITERS_PRIVATE_READ_DIGEST_TAMPER_DETECTED bucket=%s\n", bucket)
}

func TestP0RealMinIOAuthRejectsSharedBucket(t *testing.T) {
	cli, _ := p0S3Client(t)
	adapter, err := NewS3ConditionalAPI(cli)
	require.NoError(t, err)
	_, err = NewPrivateS3Registry(adapter, "atoman-shared-public", "atoman-shared-public")
	require.ErrorIs(t, err, ErrInvalid)
	// A foreign project UUID cannot bypass manifest validation.
	bad := fixtureSnapshot()
	bad.ProjectID = uuid.Nil
	reg, err := NewPrivateS3Registry(adapter, "p0-private-validation", "atoman-shared-public")
	require.NoError(t, err)
	_, err = reg.PublishSnapshot(context.Background(), bad)
	require.ErrorIs(t, err, ErrInvalid)
}

// Actual distinct processes use independent AWS SDK clients. Every process
// must independently confirm provider preconditions and agree on one SHA-256
// address. No process should be able to overwrite the others.
func TestP0S3ChildPublisher(t *testing.T) {
	if os.Getenv("PAPERS_P0_S3_CHILD") != "1" {
		t.Skip("child publisher helper")
	}
	cli, _ := p0S3Client(t)
	provider, err := NewS3ConditionalAPI(cli)
	require.NoError(t, err)
	bucket := os.Getenv("PAPERS_P0_S3_CHILD_BUCKET")
	require.True(t, strings.HasPrefix(bucket, "papers-private-p0-"))
	reg, err := NewPrivateS3Registry(provider, bucket, "atoman-shared-public")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, reg.Probe(ctx))
	snap := Snapshot{Version: 1, ProjectID: uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		Revision: 9, Files: []SourceFile{{Path: "main.tex", Content: "\\documentclass{article}\\begin{document}shared P0 source\\end{document}"}}}
	digest, err := reg.PublishSnapshot(ctx, snap)
	require.NoError(t, err)
	require.Equal(t, HashBytes(mustJSON(t, snap)), digest)
}
func mustJSON(t *testing.T, s Snapshot) []byte {
	t.Helper()
	raw, err := json.Marshal(s)
	require.NoError(t, err)
	return raw
}

func TestP0RealMinIOCrossProcessConditionalPublish(t *testing.T) {
	cli, _ := p0S3Client(t)
	bucket := "papers-private-p0-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:14]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := cli.CreateBucketWithContext(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = cli.PutBucketVersioningWithContext(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucket), VersioningConfiguration: &s3.VersioningConfiguration{Status: aws.String("Enabled")},
	})
	require.NoError(t, err)
	const processes = 6
	var wg sync.WaitGroup
	outputs := make(chan error, processes)
	for i := 0; i < processes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestP0S3ChildPublisher$", "-test.count=1")
			cmd.Env = append(os.Environ(), "PAPERS_P0_S3_CHILD=1", "PAPERS_P0_S3_CHILD_BUCKET="+bucket)
			output, err := cmd.CombinedOutput()
			if err != nil {
				outputs <- fmt.Errorf("child failed: %w: %s", err, string(output))
			}
		}()
	}
	wg.Wait()
	close(outputs)
	for err := range outputs {
		require.NoError(t, err)
	}
	// One source key is recorded as one current object; if the provider
	// failed to honor If-None-Match, child probes or digest checks fail.
	cliObject, err := NewS3ConditionalAPI(cli)
	require.NoError(t, err)
	snap := Snapshot{Version: 1, ProjectID: uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		Revision: 9, Files: []SourceFile{{Path: "main.tex", Content: "\\documentclass{article}\\begin{document}shared P0 source\\end{document}"}}}
	digest := HashBytes(mustJSON(t, snap))
	reg, err := NewPrivateS3Registry(cliObject, bucket, "atoman-shared-public")
	require.NoError(t, err)
	loaded, err := reg.LoadSnapshot(ctx, digest)
	require.NoError(t, err)
	require.Equal(t, snap.ProjectID, loaded.ProjectID)
	fmt.Printf("P0_S3_CROSS_PROCESS_%d_CONDITIONAL_PUBLISHERS_PASSED\n", processes)
}

func TestP0RealMinIOProbeRejectsPublicBucket(t *testing.T) {
	cli, _ := p0S3Client(t)
	bucket := "papers-private-p0-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:14]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := cli.CreateBucketWithContext(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = cli.PutBucketVersioningWithContext(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucket), VersioningConfiguration: &s3.VersioningConfiguration{Status: aws.String("Enabled")},
	})
	require.NoError(t, err)
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, bucket)
	_, err = cli.PutBucketPolicyWithContext(ctx, &s3.PutBucketPolicyInput{Bucket: aws.String(bucket), Policy: aws.String(policy)})
	require.NoError(t, err)
	provider, err := NewS3ConditionalAPI(cli)
	require.NoError(t, err)
	reg, err := NewPrivateS3Registry(provider, bucket, "atoman-shared-public")
	require.NoError(t, err)
	require.ErrorIs(t, reg.Probe(ctx), ErrConditionalUnsupported)
	_, err = reg.PublishSnapshot(ctx, fixtureSnapshot())
	require.ErrorIs(t, err, ErrConditionalUnsupported)
	fmt.Println("P0_S3_PUBLIC_BUCKET_PROBE_REJECTED")
}

func TestP0RealMinIOProbeRejectsUnversionedBucket(t *testing.T) {
	cli, _ := p0S3Client(t)
	bucket := "papers-private-p0-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:14]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := cli.CreateBucketWithContext(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	provider, err := NewS3ConditionalAPI(cli)
	require.NoError(t, err)
	reg, err := NewPrivateS3Registry(provider, bucket, "atoman-shared-public")
	require.NoError(t, err)
	require.ErrorIs(t, reg.Probe(ctx), ErrConditionalUnsupported)
	fmt.Println("P0_S3_UNVERSIONED_BUCKET_REJECTED")
}

func TestP0RealMinIOTrustedSignedArchiveAndComplianceLock(t *testing.T) {
	cli, _ := p0S3Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	bucket := "papers-private-p0-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:14]
	_, err := cli.CreateBucketWithContext(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket), ObjectLockEnabledForBucket: aws.Bool(true),
	})
	require.NoError(t, err)
	_, err = cli.PutObjectLockConfigurationWithContext(ctx, &s3.PutObjectLockConfigurationInput{
		Bucket: aws.String(bucket),
		ObjectLockConfiguration: &s3.ObjectLockConfiguration{
			ObjectLockEnabled: aws.String("Enabled"),
			Rule: &s3.ObjectLockRule{DefaultRetention: &s3.DefaultRetention{
				Mode: aws.String("COMPLIANCE"), Days: aws.Int64(1),
			}},
		},
	})
	require.NoError(t, err)
	provider, err := NewS3ConditionalAPI(cli)
	require.NoError(t, err)
	base, err := NewPrivateS3Registry(provider, bucket, "atoman-shared-public")
	require.NoError(t, err)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	trusted, err := NewTrustedArchiveRegistry(base, map[string]ed25519.PublicKey{"p0-tex-publisher": public}, 1)
	require.NoError(t, err)
	require.NoError(t, trusted.ProbePublisher(ctx))
	// >4 MiB is intentionally larger than the JSON manifest limit:
	// the binary transport MUST stream rather than using PutIfAbsent([]byte).
	archive, env := makeLargeTrustedTarFixture(t)
	envDigest, err := trusted.PublishTrustedArchive(ctx, env, archive, "p0-tex-publisher", private)
	require.NoError(t, err)
	release, err := trusted.LoadRelease(ctx, envDigest)
	require.NoError(t, err)
	require.Equal(t, env, release.Claim.Environment)
	require.Greater(t, release.Claim.ArchiveBytes, int64(MaxSnapshotBytes))
	archiveKey, err := trustedArchiveKey(env.ArchiveDigest)
	require.NoError(t, err)
	duplicate, err := os.Open(archive)
	require.NoError(t, err)
	err = provider.PutFileIfAbsent(ctx, bucket, archiveKey, duplicate, release.Claim.ArchiveBytes)
	duplicate.Close()
	require.ErrorIs(t, err, ErrAlreadyExists)
	cacheDir := t.TempDir()
	require.NoError(t, os.Chmod(cacheDir, 0700))
	cache, err := NewTrustedArchiveCache(trusted, cacheDir)
	require.NoError(t, err)
	fetched, err := cache.FetchArchive(ctx, envDigest)
	require.NoError(t, err)
	require.Equal(t, env.ArchiveDigest, func() string { hash, err := HashFile(fetched); require.NoError(t, err); return hash }())
	gate := &TrustedCompileAssetReader{Sources: base, Releases: trusted}
	verified, err := gate.VerifyCompileArchive(ctx, envDigest, fetched)
	require.NoError(t, err)
	require.Equal(t, env, verified)
	versions, err := cli.ListObjectVersionsWithContext(ctx, &s3.ListObjectVersionsInput{
		Bucket: aws.String(bucket), Prefix: aws.String(archiveKey),
	})
	require.NoError(t, err)
	var versionID string
	for _, v := range versions.Versions {
		if aws.StringValue(v.Key) == archiveKey {
			versionID = aws.StringValue(v.VersionId)
			break
		}
	}
	require.NotEmpty(t, versionID)
	lock, err := cli.GetObjectRetentionWithContext(ctx, &s3.GetObjectRetentionInput{
		Bucket: aws.String(bucket), Key: aws.String(archiveKey), VersionId: aws.String(versionID),
	})
	require.NoError(t, err)
	require.Equal(t, "COMPLIANCE", aws.StringValue(lock.Retention.Mode))
	require.True(t, lock.Retention.RetainUntilDate.After(time.Now()))
	// Compliance prohibits permanent deletion of this protected version.
	_, err = cli.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(archiveKey), VersionId: aws.String(versionID),
	})
	require.Error(t, err)
	var rf awserr.RequestFailure
	require.True(t, errors.As(err, &rf))
	t.Logf("MinIO Object Lock denied protected-version removal: status=%d code=%s", rf.StatusCode(), rf.Code())
	// AWS S3 and S3-compatible vendors may choose different 4xx codes.
	// The invariant is that a protected version cannot be removed.
	require.GreaterOrEqual(t, rf.StatusCode(), 400)
	require.Less(t, rf.StatusCode(), 500)
	stillThere, err := cli.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(archiveKey), VersionId: aws.String(versionID),
	})
	require.NoError(t, err)
	require.NotNil(t, stillThere.Body)
	require.NoError(t, stillThere.Body.Close())
	fmt.Println("P0_TRUSTED_ARCHIVE_SIGNED_S3_COMPLIANCE_RETENTION_CACHE_VERIFIED")
}

func TestP0RealMinIORejectsGovernanceOnlyArchiveBucket(t *testing.T) {
	cli, _ := p0S3Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	bucket := "papers-private-p0-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:14]
	_, err := cli.CreateBucketWithContext(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket), ObjectLockEnabledForBucket: aws.Bool(true),
	})
	require.NoError(t, err)
	_, err = cli.PutObjectLockConfigurationWithContext(ctx, &s3.PutObjectLockConfigurationInput{
		Bucket: aws.String(bucket),
		ObjectLockConfiguration: &s3.ObjectLockConfiguration{
			ObjectLockEnabled: aws.String("Enabled"),
			Rule: &s3.ObjectLockRule{DefaultRetention: &s3.DefaultRetention{
				Mode: aws.String("GOVERNANCE"), Days: aws.Int64(7),
			}},
		},
	})
	require.NoError(t, err)
	provider, err := NewS3ConditionalAPI(cli)
	require.NoError(t, err)
	base, err := NewPrivateS3Registry(provider, bucket, "atoman-shared-public")
	require.NoError(t, err)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	trusted, err := NewTrustedArchiveRegistry(base, map[string]ed25519.PublicKey{"p0-tex-publisher": pub}, 1)
	require.NoError(t, err)
	require.ErrorIs(t, trusted.ProbePublisher(ctx), ErrConditionalUnsupported)
	fmt.Println("P0_TRUSTED_ARCHIVE_GOVERNANCE_NOT_COMPLIANCE_REJECTED")
}

// This control proves the same SDK can delete a specific unprotected object
// version in disposable MinIO. Therefore the protected-version rejection
// in the signed archive test is not explained by an invalid SDK Delete API.
func TestP0RealMinIOUnprotectedVersionDeleteControl(t *testing.T) {
	cli, _ := p0S3Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	bucket := "papers-private-p0-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:14]
	_, err := cli.CreateBucketWithContext(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = cli.PutBucketVersioningWithContext(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucket), VersioningConfiguration: &s3.VersioningConfiguration{Status: aws.String("Enabled")},
	})
	require.NoError(t, err)
	inserted, err := cli.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("unprotected-control"),
		Body: bytes.NewReader([]byte("temporary control only")),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.StringValue(inserted.VersionId))
	_, err = cli.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("unprotected-control"), VersionId: inserted.VersionId,
	})
	require.NoError(t, err, "normal version-specific DELETE must work on an unprotected test object")
	_, err = cli.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("unprotected-control"), VersionId: inserted.VersionId,
	})
	require.Error(t, err)
	fmt.Println("P0_S3_VERSIONED_UNPROTECTED_DELETE_CONTROL_PASS")
}
