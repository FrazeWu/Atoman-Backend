package papersqueue

import (
	"context"
	"errors"

	"atoman/internal/model"
	"atoman/internal/platform/papersassets"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// FindByIdempotencyKey is owner+project scoped. The HTTP adapter may check
// existing accepted jobs before rereading a potentially newer project revision.
// No lookup by key alone is permitted across users or projects.
func (q *Queue) FindByIdempotencyKey(ctx context.Context, ownerID, projectID uuid.UUID, key string) (model.PaperCompileJob, bool, error) {
	var job model.PaperCompileJob
	if q == nil || q.db == nil {
		return job, false, ErrNotReady
	}
	if ctx == nil || ownerID == uuid.Nil || projectID == uuid.Nil || !idempotencyPattern.MatchString(key) {
		return job, false, ErrInvalid
	}
	err := q.db.WithContext(ctx).Where(
		"owner_id = ? AND project_id = ? AND idempotency_key = ?",
		ownerID, projectID, key,
	).Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.PaperCompileJob{}, false, nil
	}
	return job, err == nil, err
}

// ListForProject is always bound to BOTH owner and project. It is only a
// read-only status projection; lease tokens and sandbox IDs must not be exposed
// from HTTP (the caller uses the restricted CompileJobView).
func (q *Queue) ListForProject(ctx context.Context, ownerID, projectID uuid.UUID, limit int) ([]model.PaperCompileJob, error) {
	if q == nil || q.db == nil {
		return nil, ErrNotReady
	}
	if ctx == nil || ownerID == uuid.Nil || projectID == uuid.Nil || limit < 1 || limit > 20 {
		return nil, ErrInvalid
	}
	var jobs []model.PaperCompileJob
	err := q.db.WithContext(ctx).
		Where("owner_id = ? AND project_id = ?", ownerID, projectID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&jobs).Error
	if err != nil || len(jobs) < limit {
		return jobs, err
	}
	// A long sequence of failures must not hide the last usable PDF.
	for _, j := range jobs {
		if j.Status == StatusSucceeded && (papersassets.PDFRef{JobID: j.ID, Epoch: j.LeaseEpoch, Digest: j.PDFDigest, Size: j.PDFSize}).Validate() == nil {
			return jobs, nil
		}
	}
	var last model.PaperCompileJob
	err = q.db.WithContext(ctx).Where("owner_id=? AND project_id=? AND status=? AND lease_epoch>0 AND pdf_size>=8 AND pdf_size<=? AND pdf_digest ~ ?", ownerID, projectID, StatusSucceeded, papersassets.MaxPDFBytes, "^sha256:[a-f0-9]{64}$").Order("created_at DESC, id DESC").Take(&last).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return jobs, nil
	}
	if err != nil {
		return nil, err
	}
	return append(jobs, last), nil
}

// FindReusableCompile selects only the same owner's exact saved revision,
// normalized engine and verified environment. A successful job must have
// a valid durable PDF reference; the result gateway still verifies bytes.
func (q *Queue) FindReusableCompile(ctx context.Context, ownerID, projectID uuid.UUID, revision int64, compiler, environment string) (model.PaperCompileJob, bool, error) {
	var job model.PaperCompileJob
	if q == nil || q.db == nil {
		return job, false, ErrNotReady
	}
	engine, ok := papersassets.NormalizeCompiler(compiler)
	if ctx == nil || ownerID == uuid.Nil || projectID == uuid.Nil || revision <= 0 || !ok || !digestPattern.MatchString(environment) {
		return job, false, ErrInvalid
	}
	err := q.db.WithContext(ctx).Where("owner_id=? AND project_id=? AND source_revision=? AND compiler=? AND environment_digest=? AND main_file='main.tex'", ownerID, projectID, revision, engine, environment).
		Where("((status IN ('queued','running','recovering') AND cancel_requested=false) OR (status='succeeded' AND lease_epoch>0 AND pdf_size>=8 AND pdf_size<=? AND pdf_digest ~ ?))", papersassets.MaxPDFBytes, "^sha256:[a-f0-9]{64}$").
		Order("created_at DESC, id DESC").Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.PaperCompileJob{}, false, nil
	}
	return job, err == nil, err
}
