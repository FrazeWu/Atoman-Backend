package papers

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/papersassets"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxProjectSnapshots = 30

type SnapshotEntry struct {
	FileID   uuid.UUID `json:"file_id"`
	Revision int64     `json:"revision"`
	Path     string    `json:"path"`
	Kind     string    `json:"kind"`
	SHA256   string    `json:"sha256"`
}

type ProjectSnapshotDetail struct {
	model.PaperProjectSnapshot
	Files []SnapshotEntry `json:"files"`
}

type ProjectSnapshotRestore struct {
	ManagedProject
	Backup model.PaperProjectSnapshot `json:"backup"`
}

func snapshotUnavailable() error {
	return apperr.Conflict("papers.snapshot_unavailable", "A referenced immutable file version is missing or invalid")
}

func snapshotFiles(tx *gorm.DB, p model.PaperProject) ([]model.PaperFile, error) {
	var stats struct{ Count, Bytes int64 }
	if err := tx.Model(&model.PaperFile{}).Where("project_id=?", p.ID).Select("COUNT(*) AS count, COALESCE(SUM(octet_length(content)),0) AS bytes").Scan(&stats).Error; err != nil {
		return nil, err
	}
	if stats.Count < 1 || stats.Count > 64 || stats.Bytes > papersassets.MaxSnapshotBytes {
		return nil, apperr.Conflict("papers.compile_source_limit", "Project exceeds checkpoint capacity")
	}
	var files []model.PaperFile
	if err := tx.Where("project_id=?", p.ID).Order("path ASC").Find(&files).Error; err != nil {
		return nil, err
	}
	return files, managedSnapshot(p, files)
}

// The owner project row is locked by every caller. All file mutations acquire
// the same lock, so the checkpoint cannot mix revisions from different edits.
func captureProjectSnapshot(tx *gorm.DB, p model.PaperProject, name string, automatic bool, files []model.PaperFile) (model.PaperProjectSnapshot, error) {
	var out model.PaperProjectSnapshot
	var count int64
	if err := tx.Model(&model.PaperProjectSnapshot{}).Where("project_id=?", p.ID).Count(&count).Error; err != nil {
		return out, err
	}
	if count >= maxProjectSnapshots {
		return out, apperr.Conflict("papers.snapshot_limit", "At most 30 checkpoints per project; remove an unwanted checkpoint before creating or restoring")
	}
	entries := make([]SnapshotEntry, 0, len(files))
	var bytes int64
	for _, f := range files {
		var v model.PaperFileVersion
		err := tx.Where("file_id=? AND revision=?", f.ID, f.Revision).First(&v).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return out, snapshotUnavailable()
		}
		if err != nil {
			return out, err
		}
		if v.Path != f.Path || v.SHA256 != f.SHA256 || v.Content != f.Content || hashText(v.Content) != v.SHA256 {
			return out, snapshotUnavailable()
		}
		entries = append(entries, SnapshotEntry{FileID: f.ID, Revision: f.Revision, Path: f.Path, Kind: f.Kind, SHA256: f.SHA256})
		bytes += int64(len(f.Content))
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return out, err
	}
	out = model.PaperProjectSnapshot{ID: uuid.New(), ProjectID: p.ID, Name: name, SourceRevision: p.HeadRevision, MainFileID: p.MainFileID, Manifest: string(raw), FileCount: len(entries), SourceBytes: bytes, Automatic: automatic}
	return out, tx.Create(&out).Error
}

func snapshotDetail(row model.PaperProjectSnapshot) (ProjectSnapshotDetail, error) {
	detail := ProjectSnapshotDetail{PaperProjectSnapshot: row}
	if len(row.Manifest) > 128<<10 || json.Unmarshal([]byte(row.Manifest), &detail.Files) != nil || len(detail.Files) != row.FileCount || len(detail.Files) < 1 || len(detail.Files) > 64 {
		return detail, snapshotUnavailable()
	}
	seen := map[uuid.UUID]bool{}
	main := false
	for _, entry := range detail.Files {
		if entry.FileID == uuid.Nil || seen[entry.FileID] || entry.Revision < 1 || !papersassets.ValidSourcePath(entry.Path) || papersassets.SourceKind(entry.Path) != entry.Kind || len(entry.SHA256) != 64 {
			return detail, snapshotUnavailable()
		}
		seen[entry.FileID] = true
		if entry.FileID == row.MainFileID && entry.Kind == "text" && safeRootTex.MatchString(entry.Path) {
			main = true
		}
	}
	if !main {
		return detail, snapshotUnavailable()
	}
	return detail, nil
}

func findProjectSnapshot(tx *gorm.DB, pid, id uuid.UUID) (model.PaperProjectSnapshot, error) {
	var row model.PaperProjectSnapshot
	err := tx.Where("project_id=? AND id=?", pid, id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, apperr.NotFound("papers.snapshot_not_found", "Checkpoint not found")
	}
	return row, err
}

func (s *Service) ListProjectSnapshots(user authctx.CurrentUser, pid uuid.UUID) ([]model.PaperProjectSnapshot, error) {
	if _, err := s.GetProject(user, pid); err != nil {
		return nil, err
	}
	rows := make([]model.PaperProjectSnapshot, 0)
	err := s.db.Omit("manifest").Where("project_id=?", pid).Order("created_at DESC, id DESC").Limit(maxProjectSnapshots).Find(&rows).Error
	return rows, err
}
func (s *Service) GetProjectSnapshot(user authctx.CurrentUser, pid, id uuid.UUID) (ProjectSnapshotDetail, error) {
	if _, err := s.GetProject(user, pid); err != nil {
		return ProjectSnapshotDetail{}, err
	}
	row, err := findProjectSnapshot(s.db, pid, id)
	if err != nil {
		return ProjectSnapshotDetail{}, err
	}
	return snapshotDetail(row)
}
func (s *Service) CreateProjectSnapshot(user authctx.CurrentUser, pid uuid.UUID, name string, base int64) (model.PaperProjectSnapshot, error) {
	var out model.PaperProjectSnapshot
	if err := s.RequireAccess(user); err != nil {
		return out, err
	}
	name, err := validatedName(name)
	if err != nil {
		return out, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		p, err := s.lockOwned(tx, user, pid)
		if err != nil {
			return err
		}
		if base < 1 || p.HeadRevision != base {
			return apperr.Conflict("papers.revision_conflict", "Project has changed")
		}
		files, err := snapshotFiles(tx, p)
		if err != nil {
			return err
		}
		out, err = captureProjectSnapshot(tx, p, name, false, files)
		if err != nil {
			return err
		}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.snapshot.create", EntityType: "paper_project", EntityID: &pid})
	})
	return out, err
}

func (s *Service) DeleteProjectSnapshot(user authctx.CurrentUser, pid, id uuid.UUID) error {
	if err := s.RequireAccess(user); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockOwned(tx, user, pid); err != nil {
			return err
		}
		row, err := findProjectSnapshot(tx, pid, id)
		if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.snapshot.delete", EntityType: "paper_project", EntityID: &pid})
	})
}

func (s *Service) RestoreProjectSnapshot(user authctx.CurrentUser, pid, id uuid.UUID, base int64) (ProjectSnapshotRestore, error) {
	var out ProjectSnapshotRestore
	if err := s.RequireAccess(user); err != nil {
		return out, err
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		p, err := s.lockOwned(tx, user, pid)
		if err != nil {
			return err
		}
		if base < 1 || p.HeadRevision != base {
			return apperr.Conflict("papers.revision_conflict", "Project has changed")
		}
		row, err := findProjectSnapshot(tx, pid, id)
		if err != nil {
			return err
		}
		detail, err := snapshotDetail(row)
		if err != nil {
			return err
		}
		current, err := snapshotFiles(tx, p)
		if err != nil {
			return err
		}
		target := make([]model.PaperFile, 0, len(detail.Files))
		var total int64
		for _, entry := range detail.Files {
			var f model.PaperFile
			// Recover original identities, even when soft-deleted. Never accept
			// a manifest pointing at another project or re-use an older revision.
			err := tx.Unscoped().Where("project_id=? AND id=?", pid, entry.FileID).First(&f).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return snapshotUnavailable()
			}
			if err != nil {
				return err
			}
			var v model.PaperFileVersion
			err = tx.Where("file_id=? AND revision=?", entry.FileID, entry.Revision).First(&v).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return snapshotUnavailable()
			}
			if err != nil {
				return err
			}
			if f.Kind != entry.Kind || f.Revision < entry.Revision || v.Path != entry.Path || v.SHA256 != entry.SHA256 || hashText(v.Content) != v.SHA256 {
				return snapshotUnavailable()
			}
			f.Path = v.Path
			f.Content = v.Content
			f.SHA256 = v.SHA256
			f.Revision++
			f.DeletedAt = gorm.DeletedAt{}
			target = append(target, f)
			total += int64(len(f.Content))
		}
		if total != row.SourceBytes {
			return snapshotUnavailable()
		}
		next := p
		next.MainFileID = row.MainFileID
		next.HeadRevision++
		if err := managedSnapshot(next, target); err != nil {
			return err
		}
		// The safety checkpoint and all restore mutations commit together.
		backup, err := captureProjectSnapshot(tx, p, fmt.Sprintf("Before restore · revision %d", p.HeadRevision), true, current)
		if err != nil {
			return err
		}
		// Temporarily vacate the active namespace inside this transaction. This
		// makes even swapped filenames safe with the partial unique path index.
		now := time.Now().UTC()
		if err := tx.Model(&model.PaperFile{}).Where("project_id=?", pid).Update("deleted_at", now).Error; err != nil {
			return err
		}
		for _, f := range target {
			res := tx.Unscoped().Model(&model.PaperFile{}).Where("id=? AND project_id=?", f.ID, pid).Updates(map[string]any{"path": f.Path, "content": f.Content, "sha256": f.SHA256, "revision": f.Revision, "deleted_at": nil, "updated_at": now})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return snapshotUnavailable()
			}
			if err := tx.Create(&[]model.PaperFileVersion{fileVersion(f, user.ID)}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&p).Updates(map[string]any{"main_file_id": next.MainFileID, "head_revision": next.HeadRevision, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.snapshot.restore", EntityType: "paper_project", EntityID: &pid}); err != nil {
			return err
		}
		out.Project = next
		out.Project.UpdatedAt = now
		out.Backup = backup
		out.Files = make([]model.PaperFile, 0, len(target))
		return tx.Select("id", "project_id", "path", "kind", "sha256", "revision", "created_at", "updated_at").Where("project_id=?", pid).Order("path ASC").Find(&out.Files).Error
	})
	return out, err
}
