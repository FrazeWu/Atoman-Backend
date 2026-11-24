package papers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/papersqueue"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var compileKeyRE = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,96}$`)
var compileDigestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// SnapshotPublisher must publish immutable content-addressed manifests.
// A production composition MUST use a previously probed private S3
// publication boundary. No local filesystem store is wired in the API.
type SnapshotPublisher interface {
	PublishSnapshot(context.Context, papersassets.Snapshot) (string, error)
}

// TrustedReleaseReader must check the operator-pinned Ed25519 trust roots.
type TrustedReleaseReader interface {
	LoadRelease(context.Context, string) (papersassets.SignedArchiveRelease, error)
}

// CompileJobs has only owner-scoped methods. The actual PostgreSQL queue
// implements it; the API can never claim a lease or invoke runsc itself.
type CompileJobs interface {
	Submit(context.Context, papersqueue.SubmitInput) (model.PaperCompileJob, error)
	FindByIdempotencyKey(context.Context, uuid.UUID, uuid.UUID, string) (model.PaperCompileJob, bool, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (model.PaperCompileJob, error)
	RequestCancel(context.Context, uuid.UUID, uuid.UUID) (model.PaperCompileJob, error)
	ListForProject(context.Context, uuid.UUID, uuid.UUID, int) ([]model.PaperCompileJob, error)
}

// CompileRuntime is explicit dependency injection for integration testing and
// future reviewed startup. The default NewService(db) never wires this,
// including when ENV or a request header tries to enable a feature.
type CompileRuntime struct {
	Publisher         SnapshotPublisher
	Release           TrustedReleaseReader
	Jobs              CompileJobs
	Results           papersassets.PDFReader
	EnvironmentDigest string // Operator pin; NEVER accepted from HTTP JSON.
}

func (r *CompileRuntime) valid() bool {
	return r != nil && r.Publisher != nil && r.Release != nil && r.Jobs != nil &&
		compileDigestRE.MatchString(r.EnvironmentDigest)
}

// NewServiceWithCompile is NOT called by the production router. An eventual
// reviewed composition root must provision private publishing and signed
// environment readers before deliberately injecting these dependencies.
func NewServiceWithCompile(db *gorm.DB, runtime CompileRuntime) (*Service, error) {
	if db == nil || db.Dialector.Name() != "postgres" || !runtime.valid() {
		return nil, errors.New("papers compile: missing trusted PostgreSQL/publisher/release/queue wiring")
	}
	return &Service{db: db, compile: &runtime}, nil
}

type CompileJobView struct {
	ID              uuid.UUID  `json:"id"`
	ProjectID       uuid.UUID  `json:"project_id"`
	SourceRevision  int64      `json:"source_revision"`
	Status          string     `json:"status"`
	Attempts        int        `json:"attempts"`
	MaxAttempts     int        `json:"max_attempts"`
	CancelRequested bool       `json:"cancel_requested"`
	PDFAvailable    bool       `json:"pdf_available"`
	FailureCode     string     `json:"failure_code,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
}

func viewCompile(job model.PaperCompileJob) CompileJobView {
	return CompileJobView{
		ID: job.ID, ProjectID: job.ProjectID, SourceRevision: job.SourceRevision,
		Status: job.Status, Attempts: job.Attempts, MaxAttempts: job.MaxAttempts,
		CancelRequested: job.CancelRequested, FailureCode: job.FailureCode,
		PDFAvailable: job.Status == papersqueue.StatusSucceeded && compileDigestRE.MatchString(job.PDFDigest) && job.PDFSize >= 8 && job.PDFSize <= papersassets.MaxPDFBytes,
		CreatedAt:    job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
	}
}

func compileUnavailable() error {
	return apperr.New(http.StatusServiceUnavailable, "papers.compile_disabled",
		"Compile service is not available until the isolated publisher and worker deployment is approved", nil)
}
func compileBackendError(err error) error {
	switch {
	case errors.Is(err, papersqueue.ErrNotAuthorized):
		return apperr.NotFound("papers.compile_not_found", "Compile job not found")
	case errors.Is(err, papersqueue.ErrIdempotency):
		return apperr.Conflict("papers.idempotency_conflict", "Idempotency key was already used for different inputs")
	case errors.Is(err, papersqueue.ErrInvalid):
		return apperr.Conflict("papers.compile_revision_conflict", "The project changed or the compile request is invalid")
	case errors.Is(err, papersqueue.ErrQuota):
		return apperr.New(http.StatusTooManyRequests, "papers.compile_quota_exceeded", "Compile quota reached. Finish pending jobs or try again later.", nil)
	case errors.Is(err, papersqueue.ErrNotReady), errors.Is(err, papersqueue.ErrBlocked):
		return apperr.New(http.StatusServiceUnavailable, "papers.compile_queue_unavailable", "Compile queue unavailable", nil)
	default:
		return apperr.Internal(err)
	}
}

// CompileCapability exposes only whether trusted compile wiring exists.
// Never reveal S3 endpoints, release keys, worker identities or policies.
func (s *Service) CompileCapability(user authctx.CurrentUser, projectID uuid.UUID) (bool, error) {
	if _, err := s.GetProject(user, projectID); err != nil {
		return false, err
	}
	return s.compile != nil && s.compile.valid(), nil
}

// ownedSnapshot reads all files consistently under a PostgreSQL SHARE lock.
// File writes acquire UPDATE on the same project row, so a compile never
// combines files from different revisions. Publishing happens after COMMIT,
// not while holding any database lock over S3 network I/O.
func (s *Service) ownedSnapshot(ctx context.Context, user authctx.CurrentUser, projectID uuid.UUID, baseRevision int64) (papersassets.Snapshot, error) {
	var snapshot papersassets.Snapshot
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var project model.PaperProject
		err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
			Where("id = ? AND owner_id = ? AND deleted_at IS NULL", projectID, user.ID).Take(&project).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("papers.project_not_found", "Project not found")
		}
		if err != nil {
			return err
		}
		if project.HeadRevision != baseRevision {
			return apperr.Conflict("papers.revision_conflict", "Project changed since compile request")
		}
		// Reject oversized editor projects before loading their full source
		// contents into the web process. A single project may contain up to
		// 100 editor files, but P0 trusted snapshots permit only 64 / 4 MiB.
		var stats struct {
			FileCount  int64
			TotalBytes int64
		}
		if err := tx.Model(&model.PaperFile{}).
			Where("project_id = ? AND deleted_at IS NULL", projectID).
			Select("COUNT(*) AS file_count, COALESCE(SUM(octet_length(content)), 0) AS total_bytes").
			Scan(&stats).Error; err != nil {
			return err
		}
		if stats.FileCount < 1 || stats.FileCount > 64 || stats.TotalBytes > papersassets.MaxSnapshotBytes {
			return apperr.Unprocessable("papers.compile_source_limit", "Trusted compilation supports at most 64 files and 4 MiB of encoded source data")
		}
		var files []model.PaperFile
		if err := tx.Where("project_id = ? AND deleted_at IS NULL", projectID).Order("path ASC").Find(&files).Error; err != nil {
			return err
		}
		snapshot = papersassets.Snapshot{Version: 1, ProjectID: projectID, Revision: project.HeadRevision, Files: make([]papersassets.SourceFile, 0, len(files))}
		mainPath := ""
		for _, f := range files {
			if (f.Kind != "text" && f.Kind != "binary") || hashText(f.Content) != f.SHA256 {
				return apperr.Unprocessable("papers.invalid_compile_input", "Source file integrity verification failed")
			}
			kind := ""
			if f.Kind == "binary" {
				kind = "binary"
			}
			if f.ID == project.MainFileID && f.Kind == "text" {
				mainPath = f.Path
			}
			snapshot.Files = append(snapshot.Files, papersassets.SourceFile{Path: f.Path, Content: f.Content, Kind: kind})
		}
		if err := addCompileEntrypoint(&snapshot, mainPath); err != nil {
			return err
		}
		if snapshot.Validate() != nil {
			return apperr.Unprocessable("papers.unsupported_compile_sources", "Project contains unsupported or oversized source files")
		}
		raw, err := json.Marshal(snapshot)
		if err != nil || len(raw) > papersassets.MaxSnapshotBytes+65536 {
			return apperr.Unprocessable("papers.compile_source_limit", "Serialized project exceeds the compile source limit")
		}

		return nil
	})
	return snapshot, err
}

func (s *Service) SubmitCompile(ctx context.Context, user authctx.CurrentUser, projectID uuid.UUID, baseRevision int64, key string) (CompileJobView, error) {
	if err := s.RequireAccess(user); err != nil {
		return CompileJobView{}, err
	}
	if _, err := s.GetProject(user, projectID); err != nil {
		return CompileJobView{}, err
	}
	if s.compile == nil || !s.compile.valid() {
		return CompileJobView{}, compileUnavailable()
	}
	if ctx == nil || baseRevision <= 0 || !compileKeyRE.MatchString(key) {
		return CompileJobView{}, apperr.BadRequest("papers.invalid_compile_request", "Positive base_revision and valid idempotency_key are required")
	}
	// Repeated accepted HTTP requests are safe even if the project changed
	// after the initial submission. A key cannot be reused with a new revision.
	previous, exists, err := s.compile.Jobs.FindByIdempotencyKey(ctx, user.ID, projectID, key)
	if err != nil {
		return CompileJobView{}, compileBackendError(err)
	}
	if exists {
		if previous.SourceRevision != baseRevision || previous.Priority != papersqueue.PriorityManual ||
			previous.EnvironmentDigest != s.compile.EnvironmentDigest {
			return CompileJobView{}, apperr.Conflict("papers.idempotency_conflict", "Key was already accepted for a different compile")
		}
		return viewCompile(previous), nil
	}
	// Early quota rejection avoids unnecessary immutable source publications.
	// The actual queue enforces the same rule again inside its transaction.
	if admission, ok := s.compile.Jobs.(interface {
		CheckAdmission(context.Context, uuid.UUID) error
	}); ok {
		if err := admission.CheckAdmission(ctx, user.ID); err != nil {
			return CompileJobView{}, compileBackendError(err)
		}
	}
	snapshot, err := s.ownedSnapshot(ctx, user, projectID, baseRevision)
	if err != nil {
		return CompileJobView{}, err
	}
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	canonicalJSON, err := json.Marshal(snapshot)
	if err != nil {
		return CompileJobView{}, apperr.Internal(err)
	}
	expectedDigest := papersassets.HashBytes(canonicalJSON)
	// Only the operator-pinned, actually signed environment may be used.
	release, err := s.compile.Release.LoadRelease(ctx, s.compile.EnvironmentDigest)
	if err != nil {
		return CompileJobView{}, apperr.New(http.StatusServiceUnavailable,
			"papers.trusted_environment_unavailable", "Approved TeX environment is unavailable", nil)
	}
	if release.Claim.EnvironmentDigest != s.compile.EnvironmentDigest ||
		release.Claim.Environment.Validate() != nil {
		return CompileJobView{}, apperr.New(http.StatusServiceUnavailable,
			"papers.trusted_environment_invalid", "Approved TeX environment failed verification", nil)
	}
	pubCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	digest, err := s.compile.Publisher.PublishSnapshot(pubCtx, snapshot)
	if err != nil {
		return CompileJobView{}, apperr.New(http.StatusServiceUnavailable,
			"papers.snapshot_publication_unavailable", "Could not publish immutable source snapshot", nil)
	}
	if !compileDigestRE.MatchString(digest) || digest != expectedDigest {
		return CompileJobView{}, apperr.Internal(fmt.Errorf("publisher returned non-canonical or mismatched snapshot digest"))
	}
	job, err := s.compile.Jobs.Submit(ctx, papersqueue.SubmitInput{
		OwnerID: user.ID, ProjectID: projectID, SourceRevision: snapshot.Revision,
		SnapshotDigest: digest, EnvironmentDigest: s.compile.EnvironmentDigest,
		MainFile: "main.tex", IdempotencyKey: key, Priority: papersqueue.PriorityManual, MaxAttempts: 2,
	})
	if err != nil {
		return CompileJobView{}, compileBackendError(err)
	}
	return viewCompile(job), nil
}

func (s *Service) CompileStatus(ctx context.Context, user authctx.CurrentUser, projectID, jobID uuid.UUID) (CompileJobView, error) {
	if _, err := s.GetProject(user, projectID); err != nil {
		return CompileJobView{}, err
	}
	if s.compile == nil || !s.compile.valid() {
		return CompileJobView{}, compileUnavailable()
	}
	job, err := s.compile.Jobs.Get(ctx, user.ID, jobID)
	if err != nil {
		return CompileJobView{}, compileBackendError(err)
	}
	if job.ProjectID != projectID {
		return CompileJobView{}, apperr.NotFound("papers.compile_not_found", "Compile job not found")
	}
	return viewCompile(job), nil
}

func (s *Service) CompileHistory(ctx context.Context, user authctx.CurrentUser, projectID uuid.UUID) ([]CompileJobView, error) {
	if _, err := s.GetProject(user, projectID); err != nil {
		return nil, err
	}
	if s.compile == nil || !s.compile.valid() {
		return nil, compileUnavailable()
	}
	jobs, err := s.compile.Jobs.ListForProject(ctx, user.ID, projectID, 10)
	if err != nil {
		return nil, compileBackendError(err)
	}
	result := make([]CompileJobView, 0, len(jobs))
	for _, job := range jobs {
		result = append(result, viewCompile(job))
	}
	return result, nil
}

func (s *Service) CancelCompile(ctx context.Context, user authctx.CurrentUser, projectID, jobID uuid.UUID) (CompileJobView, error) {
	if _, err := s.CompileStatus(ctx, user, projectID, jobID); err != nil {
		return CompileJobView{}, err
	}
	job, err := s.compile.Jobs.RequestCancel(ctx, user.ID, jobID)
	if err != nil {
		return CompileJobView{}, compileBackendError(err)
	}
	return viewCompile(job), nil
}

// CompilePDF retrieves only an immutable, fenced result belonging to the
// authenticated, currently invited project owner. No caller-chosen object key.
func (s *Service) CompilePDF(ctx context.Context, user authctx.CurrentUser, projectID, jobID uuid.UUID) ([]byte, error) {
	if _, err := s.GetProject(user, projectID); err != nil {
		return nil, err
	}
	if s.compile == nil || !s.compile.valid() {
		return nil, compileUnavailable()
	}
	job, err := s.compile.Jobs.Get(ctx, user.ID, jobID)
	if err != nil {
		return nil, compileBackendError(err)
	}
	if job.ProjectID != projectID {
		return nil, apperr.NotFound("papers.compile_not_found", "Compile job not found")
	}
	if s.compile.Results == nil {
		return nil, apperr.New(http.StatusServiceUnavailable, "papers.pdf_persistence_pending", "Verified PDF persistence is not configured", nil)
	}
	ref := papersassets.PDFRef{JobID: job.ID, Epoch: job.LeaseEpoch, Digest: job.PDFDigest, Size: job.PDFSize}
	if job.Status != papersqueue.StatusSucceeded || ref.Validate() != nil {
		return nil, apperr.NotFound("papers.pdf_not_available", "No verified PDF exists for this compile job")
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := s.compile.Results.LoadPDF(bounded, ref)
	if err != nil || int64(len(raw)) != ref.Size || papersassets.HashBytes(raw) != ref.Digest || len(raw) < 8 || string(raw[:5]) != "%PDF-" {
		return nil, apperr.New(http.StatusServiceUnavailable, "papers.pdf_integrity_failed", "Verified PDF could not be retrieved", nil)
	}
	// Recheck access after potentially slow private storage IO.
	if _, err := s.GetProject(user, projectID); err != nil {
		return nil, err
	}
	return raw, nil
}

// CompileSourceMap returns only project-owned metadata bound to a successful
// immutable PDF and its fencing epoch. Old results can lack a map.
func (s *Service) CompileSourceMap(ctx context.Context, user authctx.CurrentUser, projectID, jobID uuid.UUID) ([]byte, error) {
	if _, err := s.GetProject(user, projectID); err != nil {
		return nil, err
	}
	if s.compile == nil || !s.compile.valid() {
		return nil, compileUnavailable()
	}
	job, err := s.compile.Jobs.Get(ctx, user.ID, jobID)
	if err != nil {
		return nil, compileBackendError(err)
	}
	ref := papersassets.PDFRef{JobID: job.ID, Epoch: job.LeaseEpoch, Digest: job.PDFDigest, Size: job.PDFSize}
	missing := apperr.NotFound("papers.source_map_unavailable", "Recompile to generate a source map")
	if job.ProjectID != projectID || job.Status != papersqueue.StatusSucceeded || ref.Validate() != nil {
		return nil, missing
	}
	var m model.PaperCompileSourceMap
	err = s.db.WithContext(ctx).Where("job_id=? AND project_id=? AND epoch=? AND pdf_digest=?", jobID, projectID, job.LeaseEpoch, job.PDFDigest).Take(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, missing
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if papersassets.ValidateSourceMap(m.Data) != nil {
		return nil, missing
	}
	if _, err = s.GetProject(user, projectID); err != nil {
		return nil, err
	}
	return m.Data, nil
}
