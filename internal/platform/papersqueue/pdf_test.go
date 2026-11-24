package papersqueue

import (
	"atoman/internal/model"
	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/paperssandbox"
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestDurablePDFRequiredForSuccess(t *testing.T) {
	_, db, owner, project := setupQueue(t)
	q, err := NewWithDurablePDF(db, 15*time.Second)
	require.NoError(t, err)
	ctx := context.Background()
	j := submit(t, q, input(owner, project, "missing-pdf", PriorityManual, 2))
	c := claim(t, q, "pdf-worker", ClaimRun)
	require.NoError(t, q.Complete(ctx, c, success(c)))
	actual, err := q.Get(ctx, owner, j.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, actual.Status)
	require.Equal(t, "verified_pdf_not_persisted", actual.FailureCode)
	require.Empty(t, actual.PDFDigest)
	j = submit(t, q, input(owner, project, "durable-pdf", PriorityManual, 2))
	c = claim(t, q, "pdf-worker", ClaimRun)
	r := success(c)
	r.PDFDigest = "sha256:" + strings.Repeat("a", 64)
	r.PDFSize = 1024
	require.NoError(t, q.Complete(ctx, c, r))
	actual, err = q.Get(ctx, owner, j.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, actual.Status)
	require.Equal(t, r.PDFDigest, actual.PDFDigest)
	require.Equal(t, r.PDFSize, actual.PDFSize)
	// A stale attempt cannot change an already committed result.
	r.PDFDigest = "sha256:" + strings.Repeat("b", 64)
	require.ErrorIs(t, q.Complete(ctx, c, r), ErrLeaseLost)
}

func TestSyncMapCommitIsFencedWithSuccessfulPDF(t *testing.T) {
	_, db, owner, project := setupQueue(t)
	require.NoError(t, db.AutoMigrate(&model.PaperCompileSourceMap{}))
	q, err := NewWithDurablePDF(db, 15*time.Second)
	require.NoError(t, err)
	j := submit(t, q, input(owner, project, "source-map-success", PriorityManual, 2))
	c := claim(t, q, "sync-worker", ClaimRun)
	r := success(c)
	r.PDFDigest = "sha256:" + strings.Repeat("a", 64)
	r.PDFSize = 1024
	r.SourceMap = []byte(`{"version":1,"files":["main.tex"],"points":[[0,3,1,1000,2000]]}`)
	require.NoError(t, q.Complete(context.Background(), c, r))
	var m model.PaperCompileSourceMap
	require.NoError(t, db.Where("job_id=?", j.ID).Take(&m).Error)
	require.Equal(t, r.PDFDigest, m.PDFDigest)
	require.Equal(t, c.Epoch, m.Epoch)
	require.Equal(t, r.SourceMap, m.Data)
	require.ErrorIs(t, q.Complete(context.Background(), c, r), ErrLeaseLost)
}

func TestCompileHistoryRetainsLastVerifiedPDF(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	old := submit(t, q, input(owner, project, "old-verified", PriorityManual, 2))
	require.NoError(t, db.Model(&model.PaperCompileJob{}).Where("id=?", old.ID).Updates(map[string]any{"status": StatusSucceeded, "lease_epoch": 1, "pdf_size": 1024, "pdf_digest": "sha256:" + strings.Repeat("a", 64), "created_at": time.Now().Add(-time.Hour)}).Error)
	for i := 0; i < 11; i++ {
		j := submit(t, q, input(owner, project, fmt.Sprintf("failed-%d", i), PriorityManual, 2))
		require.NoError(t, db.Model(&model.PaperCompileJob{}).Where("id=?", j.ID).Update("status", StatusFailed).Error)
	}
	jobs, err := q.ListForProject(context.Background(), owner, project, 10)
	require.NoError(t, err)
	require.Len(t, jobs, 11)
	require.Equal(t, old.ID, jobs[10].ID)
}

func TestCompileDiagnosticsCommittedOnlyWithVerifiedCleanup(t *testing.T) {
	q, db, owner, project := setupQueue(t)
	j := submit(t, q, input(owner, project, "diagnostic-verified", PriorityManual, 2))
	c := claim(t, q, "diagnostics-worker", ClaimRun)
	r := success(c)
	r.Status = paperssandbox.CompileFailed
	r.UnderlyingStatus = paperssandbox.CompileFailed
	r.ExitCode = 1
	r.ArtifactVerified = false
	r.Diagnostics = []papersassets.CompileDiagnostic{{Tool: "latex", File: "main.tex", Line: 2, Message: "Undefined control sequence."}}
	require.NoError(t, q.Complete(context.Background(), c, r))
	var stored model.PaperCompileJob
	require.NoError(t, db.Where("id=?", j.ID).Take(&stored).Error)
	require.Equal(t, StatusFailed, stored.Status)
	require.Equal(t, r.Diagnostics, papersassets.DecodeCompileDiagnostics(stored.DiagnosticsJSON))
	require.ErrorIs(t, q.Complete(context.Background(), c, r), ErrLeaseLost)
	j = submit(t, q, input(owner, project, "diagnostic-unverified", PriorityManual, 2))
	c = claim(t, q, "diagnostics-worker", ClaimRun)
	r.JobID = c.SandboxID
	r.CleanupVerified = false
	require.NoError(t, q.Complete(context.Background(), c, r))
	stored = model.PaperCompileJob{}
	require.NoError(t, db.Where("id=?", j.ID).Take(&stored).Error)
	require.Empty(t, stored.DiagnosticsJSON)
}
