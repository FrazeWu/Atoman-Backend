//go:build linux

package papers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/paperspubgate"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/stretchr/testify/require"
)

// Explicitly opt-in; the runner provisions an isolated MinIO, test PostgreSQL,
// and a real PDF compiled from a fixed benign TeX fixture. No production keys.
func TestP0RealPDFStorageAndAuthenticatedDownload(t *testing.T) {
	if os.Getenv("PAPERS_P0_PDF_ACCEPTANCE") != "1" {
		t.Skip("disposable PDF acceptance environment required")
	}
	endpoint := os.Getenv("PAPERS_P0_IAM_ENDPOINT")
	u, err := url.Parse(endpoint)
	require.NoError(t, err)
	require.Equal(t, "http", u.Scheme)
	require.Equal(t, "127.0.0.1", u.Hostname())
	require.Empty(t, u.Path)
	require.Empty(t, u.RawQuery)
	require.Nil(t, u.User)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	require.Greater(t, port, 1024)
	require.NotContains(t, []int{5432, 9000, 9100, 9101}, port)
	bucket := os.Getenv("PAPERS_P0_IAM_BUCKET")
	require.True(t, strings.HasPrefix(bucket, "papers-private-p0-"))
	require.True(t, strings.HasPrefix(os.Getenv("PAPERS_P0_GATEWAY_WRITER_USER"), "papersp0gate"))
	require.True(t, strings.HasPrefix(os.Getenv("PAPERS_P0_IAM_READER_USER"), "papersp0read"))
	makeClient := func(user, secret string) *s3.S3 {
		require.GreaterOrEqual(t, len(secret), 24)
		return s3.New(session.Must(session.NewSession(&aws.Config{
			Endpoint: aws.String(endpoint), Region: aws.String("us-east-1"),
			S3ForcePathStyle: aws.Bool(true), DisableSSL: aws.Bool(true),
			Credentials: credentials.NewStaticCredentials(user, secret, ""), MaxRetries: aws.Int(0),
		})))
	}
	writeClient := makeClient(os.Getenv("PAPERS_P0_GATEWAY_WRITER_USER"), os.Getenv("PAPERS_P0_GATEWAY_WRITER_SECRET"))
	readClient := makeClient(os.Getenv("PAPERS_P0_IAM_READER_USER"), os.Getenv("PAPERS_P0_IAM_READER_SECRET"))
	writer, err := papersassets.NewS3ConditionalAPI(writeClient)
	require.NoError(t, err)
	reader, err := papersassets.NewS3ConditionalAPI(readClient)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	probe, err := papersassets.NewPrivateS3Registry(writer, bucket, "atoman-public")
	require.NoError(t, err)
	require.NoError(t, probe.Probe(ctx))
	locked, err := writer.LockCompliant(ctx, bucket, 1)
	require.NoError(t, err)
	require.True(t, locked)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	staging := filepath.Join(root, "staging")
	require.NoError(t, os.Mkdir(staging, 0700))
	gate, err := paperspubgate.NewPDFGateway(writer, bucket, staging, uint32(os.Getuid()))
	require.NoError(t, err)
	socket := filepath.Join(root, "pdf.sock")
	server, err := paperspubgate.NewUnixServer(gate, socket, os.Getgid())
	require.NoError(t, err)
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve() }()
	t.Cleanup(func() { require.NoError(t, server.Close()); require.NoError(t, <-stopped) })
	transport, err := paperspubgate.NewTransport(socket, bucket, reader)
	require.NoError(t, err)
	publisher, err := papersassets.NewPDFRegistry(transport, bucket, "atoman-public")
	require.NoError(t, err)
	results, err := papersassets.NewPDFRegistry(reader, bucket, "atoman-public")
	require.NoError(t, err)
	raw, err := os.ReadFile(os.Getenv("PAPERS_P0_PDF_FIXTURE"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(raw), "%PDF-"))
	require.Greater(t, len(raw), 1024)
	fixture, err := os.Open(os.Getenv("PAPERS_P0_PDF_FIXTURE"))
	require.NoError(t, err)
	defer fixture.Close()

	f := newCompileHTTPFixture(t, true)
	q, err := papersqueue.NewWithDurablePDF(f.db, 30*time.Second)
	require.NoError(t, err)
	f.svc.compile.Jobs = q
	f.svc.compile.Results = results
	base := f.base()
	req := f.request("POST", base+"/compiles", `{"base_revision":1,"idempotency_key":"real-pdf-storage"}`, f.ownerToken)
	require.Equal(t, http.StatusAccepted, req.Code, req.Body.String())
	job := parseJob(t, req)
	claim, ok, err := q.ClaimNext(ctx, "isolated-pdf-acceptance")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, job.ID, claim.Job.ID)
	path := base + "/compiles/" + job.ID.String() + "/pdf"
	require.Equal(t, http.StatusNotFound, f.request("GET", path, "", f.ownerToken).Code)
	ref, err := publisher.PublishPDF(ctx, job.ID, claim.Epoch, fixture)
	require.NoError(t, err)
	replay, err := publisher.PublishPDF(ctx, job.ID, claim.Epoch, fixture)
	require.NoError(t, err)
	require.Equal(t, ref, replay)
	key := fmt.Sprintf("papers/immutable/v1/pdf-results/%s/%d/%s.pdf", job.ID, claim.Epoch, strings.TrimPrefix(ref.Digest, "sha256:"))
	denied, err := reader.AnonymousReadDenied(ctx, bucket, key)
	require.NoError(t, err)
	require.True(t, denied)
	// Reader credentials cannot bypass the append-only gateway.
	err = reader.PutIfAbsent(ctx, bucket, key, []byte("forbidden"), "application/octet-stream")
	require.Error(t, err)
	_, err = readClient.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	require.Error(t, err)
	// The fixture was compiled separately; this acceptance exercises the real
	// fenced queue commit, immutable publication, and authenticated HTTP read.
	result := paperssandbox.Result{
		JobID: claim.SandboxID, Status: paperssandbox.Completed, UnderlyingStatus: paperssandbox.Completed,
		ExitKnown: true, ExitCode: 0, SentryObserved: true, LimitsVerified: true, ArtifactVerified: true, CleanupVerified: true,
		PDFDigest: ref.Digest, PDFSize: ref.Size,
	}
	require.NoError(t, q.Complete(ctx, claim, result))
	status := parseJob(t, f.request("GET", base+"/compiles/"+job.ID.String(), "", f.ownerToken))
	require.Equal(t, papersqueue.StatusSucceeded, status.Status)
	require.True(t, status.PDFAvailable)
	got := f.request("GET", path, "", f.ownerToken)
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	require.Equal(t, raw, got.Body.Bytes())
	require.Equal(t, "application/pdf", got.Header().Get("Content-Type"))
	require.Equal(t, "private, no-store", got.Header().Get("Cache-Control"))
	require.Equal(t, http.StatusUnauthorized, f.request("GET", path, "", "").Code)
	require.Equal(t, http.StatusNotFound, f.request("GET", path, "", f.otherToken).Code)
	require.NoError(t, f.db.Model(&model.PaperAccessGrant{}).Where("user_id=?", f.owner.UUID).Update("status", "revoked").Error)
	require.NotEqual(t, http.StatusOK, f.request("GET", path, "", f.ownerToken).Code)
	t.Logf("REAL_PDF_STORAGE_AUTHENTICATED_ROUNDTRIP_PASS bytes=%d digest=%s", len(raw), ref.Digest)
}
