package papers

import (
	"context"
	"net/http"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/papersassets"
	"github.com/stretchr/testify/require"
)

type testPDFReader struct {
	raw   []byte
	ref   papersassets.PDFRef
	calls int
}

func (p *testPDFReader) LoadPDF(_ context.Context, ref papersassets.PDFRef) ([]byte, error) {
	p.calls++
	p.ref = ref
	return p.raw, nil
}
func TestCompilePDFPrivateVerifiedOwnerOnly(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	base := f.base()
	pdf := []byte("%PDF-1.7\nprivate synthetic fixture\n%%EOF\n")
	reader := &testPDFReader{raw: pdf}
	f.svc.compile.Results = reader
	req := f.request("POST", base+"/compiles", `{"base_revision":1,"idempotency_key":"pdf-test"}`, f.ownerToken)
	require.Equal(t, http.StatusAccepted, req.Code, req.Body.String())
	job := parseJob(t, req)
	path := base + "/compiles/" + job.ID.String() + "/pdf"
	require.Equal(t, http.StatusNotFound, f.request("GET", path, "", f.ownerToken).Code)
	require.Equal(t, http.StatusNotFound, f.request("GET", path, "", f.otherToken).Code)
	require.Zero(t, reader.calls)
	require.NoError(t, f.db.Model(&model.PaperCompileJob{}).Where("id=?", job.ID).Updates(map[string]any{
		"status": "succeeded", "lease_epoch": 3, "pdf_digest": papersassets.HashBytes(pdf), "pdf_size": len(pdf),
	}).Error)
	got := f.request("GET", path, "", f.ownerToken)
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	require.Equal(t, pdf, got.Body.Bytes())
	require.Equal(t, "application/pdf", got.Header().Get("Content-Type"))
	require.Equal(t, "private, no-store", got.Header().Get("Cache-Control"))
	require.Equal(t, job.ID, reader.ref.JobID)
	require.Equal(t, int64(3), reader.ref.Epoch)
	require.Equal(t, http.StatusNotFound, f.request("GET", path, "", f.otherToken).Code)
	require.Equal(t, 1, reader.calls)
	reader.raw = []byte("%PDF-1.7\ncorrupted\n%%EOF\n")
	require.Equal(t, http.StatusServiceUnavailable, f.request("GET", path, "", f.ownerToken).Code)
	require.NoError(t, f.db.Model(&model.PaperAccessGrant{}).Where("user_id=?", f.owner.UUID).Update("status", "revoked").Error)
	require.NotEqual(t, http.StatusOK, f.request("GET", path, "", f.ownerToken).Code)
	require.Equal(t, 2, reader.calls)
}
