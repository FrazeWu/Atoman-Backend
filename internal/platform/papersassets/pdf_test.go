package papersassets

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type pdfTestStore struct {
	objects map[string][]byte
	writes  int
}

func (s *pdfTestStore) PutFileIfAbsent(_ context.Context, _ string, key string, f *os.File, n int64) error {
	s.writes++
	if _, ok := s.objects[key]; ok {
		return ErrAlreadyExists
	}
	raw, err := io.ReadAll(io.NewSectionReader(f, 0, n))
	if err != nil {
		return err
	}
	s.objects[key] = raw
	return nil
}
func (s *pdfTestStore) OpenArchive(_ context.Context, _ string, key string) (io.ReadCloser, int64, error) {
	raw, ok := s.objects[key]
	if !ok {
		return nil, 0, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(raw)), int64(len(raw)), nil
}
func TestPDFRegistryFencedImmutableRoundTrip(t *testing.T) {
	ctx := context.Background()
	transport := &pdfTestStore{objects: map[string][]byte{}}
	registry, err := NewPDFRegistry(transport, "papers-private-test", "atoman-public")
	require.NoError(t, err)
	raw := []byte("%PDF-1.7\nsynthetic test content\n%%EOF\n")
	f, err := os.CreateTemp(t.TempDir(), "artifact")
	require.NoError(t, err)
	defer f.Close()
	_, err = f.Write(raw)
	require.NoError(t, err)
	job := uuid.New()
	ref, err := registry.PublishPDF(ctx, job, 3, f)
	require.NoError(t, err)
	require.Equal(t, job, ref.JobID)
	require.Equal(t, int64(3), ref.Epoch)
	require.Equal(t, HashBytes(raw), ref.Digest)
	got, err := registry.LoadPDF(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, raw, got)
	duplicate, err := registry.PublishPDF(ctx, job, 3, f)
	require.NoError(t, err)
	require.Equal(t, ref, duplicate)
	require.Len(t, transport.objects, 1)
	wrong := ref
	wrong.Epoch = 4
	_, err = registry.LoadPDF(ctx, wrong)
	require.Error(t, err)
	wrong = ref
	wrong.JobID = uuid.New()
	_, err = registry.LoadPDF(ctx, wrong)
	require.Error(t, err)
	key, err := ref.key()
	require.NoError(t, err)
	transport.objects[key][10] ^= 1
	_, err = registry.LoadPDF(ctx, ref)
	require.ErrorIs(t, err, ErrIntegrity)
	_, err = registry.PublishPDF(ctx, job, 3, f)
	require.ErrorIs(t, err, ErrIntegrity)
}
func TestPDFRegistryRejectsInvalidInput(t *testing.T) {
	transport := &pdfTestStore{objects: map[string][]byte{}}
	r, err := NewPDFRegistry(transport, "papers-private-test", "atoman-public")
	require.NoError(t, err)
	f, err := os.CreateTemp(t.TempDir(), "not-pdf")
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteString("not a PDF document")
	require.NoError(t, err)
	_, err = r.PublishPDF(context.Background(), uuid.New(), 1, f)
	require.Error(t, err)
	require.Zero(t, transport.writes)
	_, err = NewPDFRegistry(transport, "shared-bucket", "shared-bucket")
	require.Error(t, err)
}
