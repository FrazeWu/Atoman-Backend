package papers

import (
	"bytes"
	"context"
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/paperscrdt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// This store is not exposed through a router yet. The future gateway must
// authorize every message, rate limit it and serialize/batch room updates.
const maxCollabProjectBytes = 16 << 20
const maxCollabProjectFiles = 64

type collabEngine interface {
	Run(context.Context, paperscrdt.Request) (paperscrdt.Checkpoint, error)
}
type collaborationStore struct {
	service *Service
	engine  collabEngine
}
type collaborationState struct {
	Epoch           uuid.UUID
	Sequence        int64
	FileRevision    int64
	ProjectRevision int64
	Checkpoint      paperscrdt.Checkpoint
}
type collabBase struct {
	project model.PaperProject
	file    model.PaperFile
	row     model.PaperCollaborativeDocument
	exists  bool
}

func staleEpoch() error {
	return apperr.Conflict("papers.collab_epoch_stale", "Collaborative document epoch is stale; recover local edits before reopening")
}
func corruptCheckpoint() error {
	return apperr.Conflict("papers.collab_checkpoint_invalid", "Collaborative checkpoint is invalid; no source was modified")
}
func collabRetry() error {
	return apperr.Conflict("papers.collab_retry", "Collaborative checkpoint changed; retry the same update")
}
func collabRuntimeError(err error) error {
	if errors.Is(err, paperscrdt.ErrInvalid) {
		return apperr.BadRequest("papers.collab_invalid_update", "Invalid collaborative document or update")
	}
	// No user-controlled engine error or source text reaches an error response.
	return apperr.Conflict("papers.collab_unavailable", "Collaborative runtime is unavailable")
}

// SQL values (including private manuscript source and binary state) must not
// enter diagnostic logs or escape through a raw database error. Scope the
// logger change to this internal store rather than mutating the shared DB.
func (c *collaborationStore) database(ctx context.Context) *gorm.DB {
	return c.service.db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).WithContext(ctx)
}
func safeCollabStorageError(err error) error {
	if err == nil {
		return nil
	}
	var app *apperr.AppError
	if errors.As(err, &app) {
		return app
	}
	return apperr.New(500, "papers.collab_storage_failed", "Collaborative storage operation failed; no update was acknowledged", nil)
}
func (c *collaborationStore) ready() bool {
	return c != nil && c.service != nil && c.service.db != nil && c.engine != nil
}

func lockCollabAccess(tx *gorm.DB, s *Service, user authctx.CurrentUser, pid uuid.UUID, write bool) (model.PaperProject, error) {
	var empty model.PaperProject
	if user.ID == uuid.Nil {
		return empty, apperr.Unauthorized("Login required")
	}
	p, err := s.lockAccessible(tx, user, pid, write)
	if err != nil {
		return empty, err
	}
	// Serialize module revocation against the accepted update. Expiration
	// and the current membership are checked again at commit.
	var grant model.PaperAccessGrant
	err = tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("user_id=? AND status='active'", user.ID).First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && grant.ExpiresAt != nil && !grant.ExpiresAt.After(time.Now().UTC())) {
		return empty, apperr.Forbidden("papers.invite_required", "Papers requires an active invitation")
	}
	if err != nil {
		return empty, err
	}
	var account model.User
	err = tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("uuid", "auth_version").Where("uuid=? AND is_active=?", user.ID, true).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return empty, apperr.Forbidden("papers.invite_required", "Active account required")
	}
	if err != nil {
		return empty, err
	}

	// Session-bound callers recheck expiration, revocation and auth version
	// inside the same locked transaction as each accepted document update.
	if sessionID, ok := tx.Statement.Context.Value(collabSessionKey{}).(uuid.UUID); ok {
		if sessionID == uuid.Nil {
			return empty, apperr.Unauthorized("Session expired")
		}
		var session model.AuthSession
		err = tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id").Where("id=? AND user_id=? AND auth_version=? AND revoked_at IS NULL AND expires_at>?", sessionID, user.ID, account.AuthVersion, time.Now().UTC()).First(&session).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return empty, apperr.Unauthorized("Session expired")
		}
		if err != nil {
			return empty, err
		}
	}
	return p, nil
}

func lockCollabBase(tx *gorm.DB, s *Service, user authctx.CurrentUser, pid, fid uuid.UUID, write bool) (collabBase, error) {
	var base collabBase
	p, err := lockCollabAccess(tx, s, user, pid, write)
	if err != nil {
		return base, err
	}
	base.project = p
	err = tx.Where("id=? AND project_id=?", fid, pid).First(&base.file).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return base, apperr.NotFound("papers.file_not_found", "File not found")
	}
	if err != nil {
		return base, err
	}
	if base.file.Kind != "text" {
		return base, apperr.BadRequest("papers.binary_read_only", "Only text files support collaboration")
	}
	if validateText(base.file.Content) != nil || hashText(base.file.Content) != base.file.SHA256 || base.file.Revision < 1 {
		return base, corruptCheckpoint()
	}
	err = tx.Where("file_id=? AND project_id=?", fid, pid).First(&base.row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return base, nil
	}
	if err != nil {
		return base, err
	}
	base.exists = true
	return base, nil
}

func (b collabBase) checkpoint() (collaborationState, error) {
	r := b.row
	if !b.exists || r.FileRevision < b.file.Revision {
		return collaborationState{}, staleEpoch()
	}
	if r.FileRevision != b.file.Revision || r.FileSHA256 != b.file.SHA256 || r.Path != b.file.Path ||
		r.Epoch == uuid.Nil || r.Sequence < 1 || r.ProjectID != b.project.ID || r.FileID != b.file.ID ||
		hashText(string(r.State)) != r.StateSHA256 || hashText(string(r.StateVector)) != r.VectorSHA256 {
		return collaborationState{}, corruptCheckpoint()
	}
	cp := paperscrdt.Checkpoint{Text: b.file.Content, State: r.State, Vector: r.StateVector}
	if !paperscrdt.ValidCheckpoint(cp) {
		return collaborationState{}, corruptCheckpoint()
	}
	return collaborationState{Epoch: r.Epoch, Sequence: r.Sequence, FileRevision: b.file.Revision, ProjectRevision: b.project.HeadRevision, Checkpoint: cp}, nil
}
func sameCollabBase(a, b collabBase) bool {
	if a.file.ID != b.file.ID || a.file.Revision != b.file.Revision || a.file.Path != b.file.Path || a.file.SHA256 != b.file.SHA256 || a.exists != b.exists {
		return false
	}
	return !a.exists || (a.row.Epoch == b.row.Epoch && a.row.Sequence == b.row.Sequence && a.row.FileRevision == b.row.FileRevision && a.row.StateSHA256 == b.row.StateSHA256 && a.row.VectorSHA256 == b.row.VectorSHA256)
}
func (c *collaborationStore) load(ctx context.Context, user authctx.CurrentUser, pid, fid uuid.UUID, write bool) (collabBase, error) {
	var base collabBase
	if !c.ready() || ctx == nil {
		return base, collabRuntimeError(paperscrdt.ErrUnavailable)
	}
	if err := c.service.RequireAccess(user); err != nil {
		return base, safeCollabStorageError(err)
	}
	err := c.database(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		base, err = lockCollabBase(tx, c.service, user, pid, fid, write)
		return err
	})
	return base, safeCollabStorageError(err)
}

// Open initializes from a canonical revision once. A normal source edit
// invalidates the prior epoch; an old update is never replayed into a new one.
// Engine work happens outside the project lock, then commits by CAS.
func (c *collaborationStore) Open(ctx context.Context, user authctx.CurrentUser, pid, fid uuid.UUID) (collaborationState, error) {
	base, err := c.load(ctx, user, pid, fid, true)
	if err != nil {
		return collaborationState{}, err
	}
	if base.exists && base.row.FileRevision >= base.file.Revision {
		return base.checkpoint()
	}
	cp, err := c.engine.Run(ctx, paperscrdt.Request{Op: "seed", Text: base.file.Content})
	if err != nil {
		return collaborationState{}, collabRuntimeError(err)
	}
	if !paperscrdt.ValidCheckpoint(cp) || cp.Text != base.file.Content {
		return collaborationState{}, corruptCheckpoint()
	}
	var out collaborationState
	err = c.database(ctx).Transaction(func(tx *gorm.DB) error {
		fresh, err := lockCollabBase(tx, c.service, user, pid, fid, true)
		if err != nil {
			return err
		}
		if !sameCollabBase(base, fresh) {
			return collabRetry()
		}
		// Deleted file states are no longer usable. Accepted canonical versions
		// remain in history; reclaim their unused CRDT storage under the same lock.
		if err := tx.Where("project_id=? AND file_id NOT IN (?)", pid, tx.Model(&model.PaperFile{}).Select("id").Where("project_id=?", pid)).Delete(&model.PaperCollaborativeDocument{}).Error; err != nil {
			return err
		}
		if err := checkCollabQuota(tx, pid, fid, cp); err != nil {
			return err
		}
		row := model.PaperCollaborativeDocument{FileID: fid, ProjectID: pid, Epoch: uuid.New(), Sequence: 1, FileRevision: fresh.file.Revision, FileSHA256: fresh.file.SHA256, Path: fresh.file.Path, State: cp.State, StateVector: cp.Vector, StateSHA256: hashText(string(cp.State)), VectorSHA256: hashText(string(cp.Vector))}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "file_id"}}, DoUpdates: clause.AssignmentColumns([]string{"epoch", "sequence", "file_revision", "file_sha256", "path", "state", "state_vector", "state_sha256", "vector_sha256", "updated_at"})}).Create(&row).Error; err != nil {
			return err
		}
		out = collaborationState{Epoch: row.Epoch, Sequence: 1, FileRevision: fresh.file.Revision, ProjectRevision: fresh.project.HeadRevision, Checkpoint: cp}
		return nil
	})
	if err != nil {
		return collaborationState{}, safeCollabStorageError(err)
	}
	return out, nil
}

// Read permits viewers but does not create a room or mutate its state.
func (c *collaborationStore) Read(ctx context.Context, user authctx.CurrentUser, pid, fid, epoch uuid.UUID) (collaborationState, error) {
	base, err := c.load(ctx, user, pid, fid, false)
	if err != nil {
		return collaborationState{}, err
	}
	state, err := base.checkpoint()
	if err != nil {
		return collaborationState{}, err
	}
	if state.Epoch != epoch {
		return collaborationState{}, staleEpoch()
	}
	return state, nil
}

func checkCollabQuota(tx *gorm.DB, pid, fid uuid.UUID, cp paperscrdt.Checkpoint) error {
	var stats struct{ Count, Bytes int64 }
	err := tx.Model(&model.PaperCollaborativeDocument{}).Where("project_id=? AND file_id<>?", pid, fid).Select("COUNT(*) AS count,COALESCE(SUM(octet_length(state)+octet_length(state_vector)),0) AS bytes").Scan(&stats).Error
	if err != nil {
		return err
	}
	if stats.Count >= maxCollabProjectFiles || stats.Bytes+int64(len(cp.State)+len(cp.Vector)) > maxCollabProjectBytes {
		return apperr.Conflict("papers.collab_quota", "Collaborative storage capacity reached")
	}
	return nil
}

func (c *collaborationStore) Apply(ctx context.Context, user authctx.CurrentUser, pid, fid, epoch uuid.UUID, update []byte) (collaborationState, error) {
	if epoch == uuid.Nil || len(update) == 0 || len(update) > paperscrdt.MaxUpdate {
		return collaborationState{}, apperr.BadRequest("papers.collab_invalid_update", "Invalid collaborative update")
	}
	base, err := c.load(ctx, user, pid, fid, true)
	if err != nil {
		return collaborationState{}, err
	}
	state, err := base.checkpoint()
	if err != nil {
		return collaborationState{}, err
	}
	if state.Epoch != epoch {
		return collaborationState{}, staleEpoch()
	}
	cp, err := c.engine.Run(ctx, paperscrdt.Request{Op: "merge", Text: base.file.Content, State: base.row.State, Update: update})
	if err != nil {
		return collaborationState{}, collabRuntimeError(err)
	}
	if !paperscrdt.ValidCheckpoint(cp) {
		return collaborationState{}, corruptCheckpoint()
	}
	var out collaborationState
	err = c.database(ctx).Transaction(func(tx *gorm.DB) error {
		fresh, err := lockCollabBase(tx, c.service, user, pid, fid, true)
		if err != nil {
			return err
		}
		current, err := fresh.checkpoint()
		if err != nil {
			return err
		}
		if current.Epoch != epoch {
			return staleEpoch()
		}
		if !sameCollabBase(base, fresh) {
			return collabRetry()
		}
		if cp.Text == fresh.file.Content && bytes.Equal(cp.State, current.Checkpoint.State) && bytes.Equal(cp.Vector, current.Checkpoint.Vector) {
			out = current // idempotent retry: no new revision or sequence.
			return nil
		}
		if err := checkCollabQuota(tx, pid, fid, cp); err != nil {
			return err
		}
		file := fresh.file
		projectRevision := fresh.project.HeadRevision
		now := time.Now().UTC()
		if cp.Text != file.Content {
			if err := validateText(cp.Text); err != nil {
				return err
			}
			if len(cp.Text) > len(file.Content) {
				var used int64
				if err := tx.Model(&model.PaperFile{}).Where("project_id=?", pid).Select("COALESCE(SUM(octet_length(content)),0)").Scan(&used).Error; err != nil {
					return err
				}
				if used-int64(len(file.Content))+int64(len(cp.Text)) > papersassets.MaxSnapshotBytes {
					return apperr.Conflict("papers.compile_source_limit", "Project exceeds source capacity")
				}
			}
			file.Content = cp.Text
			file.SHA256 = hashText(cp.Text)
			file.Revision++
			res := tx.Model(&model.PaperFile{}).Where("id=? AND project_id=? AND revision=?", fid, pid, fresh.file.Revision).Updates(map[string]any{"content": file.Content, "sha256": file.SHA256, "revision": file.Revision, "updated_at": now})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return collabRetry()
			}
			if err := tx.Create(&[]model.PaperFileVersion{fileVersion(file, user.ID)}).Error; err != nil {
				return err
			}
			projectRevision++
			if err := tx.Model(&fresh.project).Updates(map[string]any{"head_revision": projectRevision, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		// Compare full state, not just the state vector (deletions may leave the
		// vector unchanged). The accepted checkpoint and canonical revision commit
		// together, including deletion-only and text-neutral CRDT updates.
		res := tx.Model(&model.PaperCollaborativeDocument{}).Where("file_id=? AND project_id=? AND epoch=? AND sequence=?", fid, pid, epoch, current.Sequence).Updates(map[string]any{"sequence": current.Sequence + 1, "file_revision": file.Revision, "file_sha256": file.SHA256, "state": cp.State, "state_vector": cp.Vector, "state_sha256": hashText(string(cp.State)), "vector_sha256": hashText(string(cp.Vector)), "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return collabRetry()
		}
		out = collaborationState{Epoch: epoch, Sequence: current.Sequence + 1, FileRevision: file.Revision, ProjectRevision: projectRevision, Checkpoint: cp}
		return nil
	})
	if err != nil {
		return collaborationState{}, safeCollabStorageError(err)
	}
	return out, nil
}
