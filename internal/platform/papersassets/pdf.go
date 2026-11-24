package papersassets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"

	"github.com/google/uuid"
)

const MaxPDFBytes int64 = 50 << 20

var pdfKeyPattern = regexp.MustCompile(`^papers/immutable/v1/pdf-results/[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}/[1-9][0-9]{0,18}/([a-f0-9]{64})\.pdf$`)

// PDFRef binds a durable result to the authoritative job and fencing epoch.
// The caller may never supply a raw object key.
type PDFRef struct {
	JobID  uuid.UUID
	Epoch  int64
	Digest string
	Size   int64
}

func (r PDFRef) Validate() error {
	if r.JobID == uuid.Nil || r.Epoch < 1 || !digestRE.MatchString(r.Digest) ||
		r.Size < 8 || r.Size > MaxPDFBytes {
		return ErrInvalid
	}
	return nil
}

func (r PDFRef) key() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	return privateRegistryPrefix + "pdf-results/" + r.JobID.String() + "/" +
		strconv.FormatInt(r.Epoch, 10) + "/" + r.Digest[7:] + ".pdf", nil
}

// PDFKeyDigest allows the dedicated gateway to recognize only canonical
// result objects; it grants no access to source snapshots or signed releases.
func PDFKeyDigest(key string) (string, bool) {
	match := pdfKeyPattern.FindStringSubmatch(key)
	if match == nil {
		return "", false
	}
	return match[1], true
}

type PDFReader interface {
	LoadPDF(context.Context, PDFRef) ([]byte, error)
}

type PDFPublisher interface {
	PublishPDF(context.Context, uuid.UUID, int64, *os.File) (PDFRef, error)
}

type PDFTransport interface {
	PutFileIfAbsent(context.Context, string, string, *os.File, int64) error
	OpenArchive(context.Context, string, string) (io.ReadCloser, int64, error)
}

// PDFRegistry deliberately exposes no public URLs, arbitrary keys, overwrite,
// delete or list. Publisher transport MUST be a separately scoped conditional
// gateway; the web API uses only the read method with a read-only S3 identity.
type PDFRegistry struct {
	transport PDFTransport
	bucket    string
}

func NewPDFRegistry(transport PDFTransport, bucket, sharedBucket string) (*PDFRegistry, error) {
	if transport == nil || sharedBucket == "" || bucket == sharedBucket ||
		!bucketNameRE.MatchString(bucket) || bucket == "public" {
		return nil, ErrInvalid
	}
	return &PDFRegistry{transport: transport, bucket: bucket}, nil
}

func (r *PDFRegistry) PublishPDF(ctx context.Context, jobID uuid.UUID, epoch int64, f *os.File) (PDFRef, error) {
	if r == nil || ctx == nil || f == nil {
		return PDFRef{}, ErrInvalid
	}
	info, err := f.Stat()
	if err != nil {
		return PDFRef{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 8 || info.Size() > MaxPDFBytes {
		return PDFRef{}, ErrInvalid
	}
	header := make([]byte, 5)
	if _, err = f.ReadAt(header, 0); err != nil || string(header) != "%PDF-" {
		return PDFRef{}, ErrInvalid
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.NewSectionReader(f, 0, info.Size()))
	if err != nil || n != info.Size() {
		return PDFRef{}, ErrIntegrity
	}
	ref := PDFRef{JobID: jobID, Epoch: epoch, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Size: n}
	key, err := ref.key()
	if err != nil {
		return PDFRef{}, err
	}
	if err = r.transport.PutFileIfAbsent(ctx, r.bucket, key, f, n); err != nil &&
		!errors.Is(err, ErrAlreadyExists) {
		return PDFRef{}, err
	}
	// A replay is not success until an independent authenticated full
	// readback verifies exactly this job/epoch/digest/length.
	if _, err = r.LoadPDF(ctx, ref); err != nil {
		return PDFRef{}, err
	}
	return ref, nil
}

func (r *PDFRegistry) LoadPDF(ctx context.Context, ref PDFRef) ([]byte, error) {
	if r == nil || ctx == nil {
		return nil, ErrInvalid
	}
	key, err := ref.key()
	if err != nil {
		return nil, err
	}
	src, length, err := r.transport.OpenArchive(ctx, r.bucket, key)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, ErrIntegrity
	}
	defer src.Close()
	if length != ref.Size {
		return nil, ErrIntegrity
	}
	raw, err := io.ReadAll(io.LimitReader(src, ref.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != ref.Size || string(raw[:5]) != "%PDF-" || HashBytes(raw) != ref.Digest {
		return nil, fmt.Errorf("%w: PDF length, format or digest mismatch", ErrIntegrity)
	}
	return raw, nil
}
