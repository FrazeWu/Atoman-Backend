//go:build linux

package papers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
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
	"atoman/internal/platform/papersworker"

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
	live := os.Getenv("PAPERS_P0_LIVE_PDF_CONFIRM") == "1"
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
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
	var raw []byte
	var fixture *os.File
	if !live {
		raw, err = os.ReadFile(os.Getenv("PAPERS_P0_PDF_FIXTURE"))
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(string(raw), "%PDF-"))
		require.Greater(t, len(raw), 1024)
		fixture, err = os.Open(os.Getenv("PAPERS_P0_PDF_FIXTURE"))
		require.NoError(t, err)
		defer fixture.Close()
	}

	f := newCompileHTTPFixture(t, true)
	q, err := papersqueue.NewWithDurablePDF(f.db, 30*time.Second)
	require.NoError(t, err)
	f.svc.compile.Jobs = q
	f.svc.compile.Results = results
	var executor *papersworker.Executor
	workroot := ""
	if live {
		// Only the explicitly isolated local rootfs harness may supply these
		// operator-pinned paths. No application or HTTP request can select them.
		require.Zero(t, os.Geteuid())
		workroot = os.Getenv("PAPERS_P0_LIVE_WORKROOT")
		baseDir := filepath.Dir(workroot)
		require.True(t, strings.HasPrefix(baseDir, "/var/tmp/atoman-papers-pdf-test."))
		require.Equal(t, "runs", filepath.Base(workroot))
		imagePrep := filepath.Join(baseDir, "image-prep")
		require.NoError(t, os.Mkdir(imagePrep, 0700))
		archive := filepath.Join(imagePrep, "normalized-rootfs.tar")
		normal, err := papersassets.NormalizeTrustedDockerExport(ctx, filepath.Join(baseDir, "texlive-rootfs.tar"), archive)
		require.NoError(t, err)
		require.Greater(t, normal.SizeBytes, int64(1<<20))
		hash, err := papersassets.HashFile(archive)
		require.NoError(t, err)
		env := papersassets.Environment{Version: 1, Name: "texlive-2026-p0", ImageDigest: "sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4", ArchiveDigest: hash, RunscRelease: "20261019.0"}
		digest, err := f.publisher.store.PublishEnvironment(env)
		require.NoError(t, err)
		// Existing local P0 release fixture; real archive bytes are independently
		// checked by the executor. Signed remote release path has its own suite.
		f.svc.compile.EnvironmentDigest = digest
		f.svc.compile.Release = testRelease{envDigest: digest}
		tool := func(name string) string {
			path, e := exec.LookPath(name)
			require.NoError(t, e)
			require.True(t, filepath.IsAbs(path))
			return path
		}
		helper, err := filepath.Abs("../../../scripts/papers/p0-cgroup-scope-exec.sh")
		require.NoError(t, err)
		executor, err = papersworker.New(papersworker.Config{
			WorkRoot: workroot, Store: f.publisher.store, ArchivePath: archive,
			PDFPublisher: publisher, BoundedWorkspace: true,
			RunscBinary: filepath.Join(baseDir, "bin", "runsc"), ScopeHelper: helper,
			SystemdRunBinary: tool("systemd-run"), SystemctlBinary: tool("systemctl"), MountpointBinary: tool("mountpoint"),
			FindmntBinary: tool("findmnt"), UmountBinary: tool("umount"), TarBinary: tool("tar"),
			Limits:  paperssandbox.Limits{MemoryMaxBytes: 768 << 20, PIDsMax: 256, CPUQuotaMicros: 100000, CPUPeriodMicros: 100000},
			Timeout: 2 * time.Minute, WrapperMemory: "1024M", WrapperCPUQuota: "100%", WrapperTasksMax: 256,
		})
		require.NoError(t, err)
	}
	base := f.base()
	req := f.request("POST", base+"/compiles", `{"base_revision":1,"idempotency_key":"real-pdf-storage"}`, f.ownerToken)
	require.Equal(t, http.StatusAccepted, req.Code, req.Body.String())
	job := parseJob(t, req)
	path := base + "/compiles/" + job.ID.String() + "/pdf"
	require.Equal(t, http.StatusNotFound, f.request("GET", path, "", f.ownerToken).Code)
	var ref papersassets.PDFRef
	if live {
		runner := papersqueue.Runner{Queue: q, Executor: executor}
		worked, err := runner.Step(ctx, "isolated-live-pdf-acceptance")
		require.NoError(t, err)
		require.True(t, worked)
		stored, err := q.Get(ctx, f.owner.UUID, job.ID)
		require.NoError(t, err)
		require.Equal(t, papersqueue.StatusSucceeded, stored.Status, stored.FailureCode)
		ref = papersassets.PDFRef{JobID: stored.ID, Epoch: stored.LeaseEpoch, Digest: stored.PDFDigest, Size: stored.PDFSize}
		require.NoError(t, ref.Validate())
		raw, err = results.LoadPDF(ctx, ref)
		require.NoError(t, err)
		require.Greater(t, len(raw), 1024)
		entries, err := os.ReadDir(workroot)
		require.NoError(t, err)
		require.Empty(t, entries, "successful real compile must remove the quota mount and workspace")
		t.Logf("LIVE_QUEUE_GVISOR_PDF_GATEWAY_PASS bytes=%d", len(raw))
	} else {
		claim, ok, err := q.ClaimNext(ctx, "isolated-pdf-acceptance")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, job.ID, claim.Job.ID)
		ref, err = publisher.PublishPDF(ctx, job.ID, claim.Epoch, fixture)
		require.NoError(t, err)
		replay, err := publisher.PublishPDF(ctx, job.ID, claim.Epoch, fixture)
		require.NoError(t, err)
		require.Equal(t, ref, replay)
		// The earlier fixture-only path deliberately uses synthetic evidence.
		result := paperssandbox.Result{JobID: claim.SandboxID, Status: paperssandbox.Completed, UnderlyingStatus: paperssandbox.Completed,
			ExitKnown: true, ExitCode: 0, SentryObserved: true, LimitsVerified: true, ArtifactVerified: true, CleanupVerified: true,
			PDFDigest: ref.Digest, PDFSize: ref.Size}
		require.NoError(t, q.Complete(ctx, claim, result))
	}
	key := fmt.Sprintf("papers/immutable/v1/pdf-results/%s/%d/%s.pdf", job.ID, ref.Epoch, strings.TrimPrefix(ref.Digest, "sha256:"))
	denied, err := reader.AnonymousReadDenied(ctx, bucket, key)
	require.NoError(t, err)
	require.True(t, denied)
	// Reader credentials cannot bypass the append-only gateway.
	err = reader.PutIfAbsent(ctx, bucket, key, []byte("forbidden"), "application/octet-stream")
	require.Error(t, err)
	_, err = readClient.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	require.Error(t, err)
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
