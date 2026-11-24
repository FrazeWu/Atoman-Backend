//go:build linux

package paperspubgate

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"atoman/internal/platform/papersassets"
	"github.com/stretchr/testify/require"
)

func putAs(t *testing.T, gw *Gateway, key string, body []byte, uid uint32) int {
	t.Helper()
	req := httptest.NewRequest("PUT", "/v1/objects/"+key, bytes.NewReader(body))
	req.Header.Set("If-None-Match", "*")
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req.WithContext(gatewayContext(req.Context(), uid)))
	return rec.Code
}

func TestPrivatePublicationRolesSeparateSourcesEnvironmentsAndResults(t *testing.T) {
	uid := uint32(os.Getuid())
	raw := []byte(`{"source":"test"}`)
	pdf := []byte("%PDF-1.7\nverified synthetic artifact\n%%EOF\n")
	pdfKey := prefix + "pdf-results/671ca2b4-9f05-4490-bd6d-180286a5dc2c/3/" + strings.TrimPrefix(papersassets.HashBytes(pdf), "sha256:") + ".pdf"
	mem := &memoryBackend{objects: map[string][]byte{}}
	sourceDir, resultDir := t.TempDir(), t.TempDir()
	require.NoError(t, os.Chmod(sourceDir, 0700))
	require.NoError(t, os.Chmod(resultDir, 0700))
	source, err := NewSnapshotGateway(mem, "papers-private-role-test", sourceDir, uid)
	require.NoError(t, err)
	results, err := NewPDFGateway(mem, "papers-private-role-test", resultDir, uid)
	require.NoError(t, err)
	sourceKey := shaKey("snapshots", raw, ".json")
	envKey := shaKey("environments", raw, ".json")
	require.Equal(t, http.StatusCreated, putAs(t, source, sourceKey, raw, uid))
	require.Equal(t, http.StatusForbidden, putAs(t, source, envKey, raw, uid))
	require.Equal(t, http.StatusForbidden, putAs(t, source, pdfKey, pdf, uid))
	require.Equal(t, http.StatusForbidden, putAs(t, results, sourceKey, raw, uid))
	require.Equal(t, http.StatusForbidden, putAs(t, results, envKey, raw, uid))
	require.Equal(t, http.StatusForbidden, putAs(t, results, pdfKey, pdf, uid+1))
	require.Equal(t, http.StatusCreated, putAs(t, results, pdfKey, pdf, uid))
	require.Equal(t, http.StatusPreconditionFailed, putAs(t, results, pdfKey, pdf, uid))
	require.Equal(t, http.StatusUnprocessableEntity, putAs(t, results, pdfKey, []byte("%PDF-1.7\nwrong"), uid))
	require.Equal(t, pdf, mem.objects[pdfKey])
}
