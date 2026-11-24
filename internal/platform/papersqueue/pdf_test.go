package papersqueue

import (
	"context"
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
