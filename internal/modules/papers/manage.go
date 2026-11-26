package papers

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/papersassets"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// All mutations are compared with the saved project revision and validated
// against the exact snapshot constraints before any accepted write.
type FileUpload struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
}
type TextReplacement struct {
	FileID       uuid.UUID `json:"file_id"`
	BaseRevision int64     `json:"base_revision"`
	Content      string    `json:"content"`
}
type FileChange struct {
	Action       string            `json:"action"`
	BaseRevision int64             `json:"base_revision"`
	FileID       uuid.UUID         `json:"file_id"`
	Path         string            `json:"path"`
	Uploads      []FileUpload      `json:"uploads"`
	Replacements []TextReplacement `json:"replacements"`
	AIEventID    *uuid.UUID        `json:"ai_event_id,omitempty"`
}
type ManagedProject struct {
	Project   model.PaperProject `json:"project"`
	Files     []model.PaperFile  `json:"files"`
	AIEventID *uuid.UUID         `json:"ai_event_id,omitempty"`
}

func validManagedSource(path, kind, content string) error {
	if !papersassets.ValidSourcePath(path) || papersassets.SourceKind(path) != kind {
		return apperr.BadRequest("papers.invalid_file_type", "Unsupported project file path or type")
	}
	if kind == "text" {
		return validateText(content)
	}
	if kind != "binary" {
		return apperr.BadRequest("papers.invalid_file_type", "Unsupported resource type")
	}
	raw, err := (papersassets.SourceFile{Path: path, Kind: kind, Content: content}).Bytes()
	if err != nil || len(raw) == 0 {
		return apperr.BadRequest("papers.invalid_content", "Resource must use canonical base64 encoding")
	}
	return nil
}
func managedSnapshot(project model.PaperProject, files []model.PaperFile) error {
	if len(files) < 1 || len(files) > 64 {
		return apperr.Conflict("papers.file_limit", "At most 64 source files including the compile entrypoint")
	}
	snapshot := papersassets.Snapshot{Version: 1, ProjectID: project.ID, Revision: project.HeadRevision, Files: make([]papersassets.SourceFile, 0, len(files)+1)}
	main := ""
	total := 0
	paths := map[string]bool{}
	dirs := map[string]bool{}
	for _, f := range files {
		if err := validManagedSource(f.Path, f.Kind, f.Content); err != nil {
			return err
		}
		key := strings.ToLower(f.Path)
		if paths[key] || dirs[key] {
			return apperr.Conflict("papers.file_exists", "File or directory path already exists")
		}
		paths[key] = true
		parts := strings.Split(key, "/")
		for i := 1; i < len(parts); i++ {
			d := strings.Join(parts[:i], "/")
			if paths[d] {
				return apperr.Conflict("papers.file_exists", "File or directory path already exists")
			}
			dirs[d] = true
		}
		total += len(f.Content)
		if total > papersassets.MaxSnapshotBytes {
			return apperr.Conflict("papers.compile_source_limit", "Project exceeds source capacity")
		}
		if f.ID == project.MainFileID && f.Kind == "text" {
			main = f.Path
		}
		snapshot.Files = append(snapshot.Files, papersassets.SourceFile{Path: f.Path, Kind: f.Kind, Content: f.Content})
	}
	if err := addCompileEntrypoint(&snapshot, main); err != nil {
		return err
	}
	if len(snapshot.Files) > 64 {
		return apperr.Conflict("papers.file_limit", "At most 64 source files including the compile entrypoint")
	}
	if err := snapshot.Validate(); err != nil {
		return apperr.Conflict("papers.compile_source_limit", "Project paths or size are incompatible with the compiler")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil || len(raw) > papersassets.MaxSnapshotBytes+65536 {
		return apperr.Conflict("papers.compile_source_limit", "Serialized project exceeds source capacity")
	}
	return nil
}
func (s *Service) ManageFiles(user authctx.CurrentUser, id uuid.UUID, in FileChange) (ManagedProject, error) {
	var out ManagedProject
	if err := s.RequireAccess(user); err != nil {
		return out, err
	}
	if in.BaseRevision < 1 {
		return out, apperr.BadRequest("papers.invalid_revision", "A saved project revision is required")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		p, err := s.lockOwned(tx, user, id)
		if err != nil {
			return err
		}
		if p.HeadRevision != in.BaseRevision {
			return apperr.Conflict("papers.revision_conflict", "Project has changed")
		}
		var stats struct {
			Count int64
			Bytes int64
		}
		if err := tx.Model(&model.PaperFile{}).Where("project_id=?", id).Select("COUNT(*) AS count, COALESCE(SUM(octet_length(content)),0) AS bytes").Scan(&stats).Error; err != nil {
			return err
		}
		if stats.Count > 100 || stats.Bytes > 100*maxTextBytes+papersassets.MaxSnapshotBytes {
			return apperr.Conflict("papers.compile_source_limit", "Legacy project exceeds management limits")
		}
		var files []model.PaperFile
		if err := tx.Where("project_id=?", id).Find(&files).Error; err != nil {
			return err
		}
		aiMeta, err := preparePaperAIMutation(tx, user, p, files, &in)
		if err != nil {
			return err
		}
		idx := -1
		for i, f := range files {
			if f.ID == in.FileID {
				idx = i
				break
			}
		}
		var changed []model.PaperFile
		var created []model.PaperFile
		var removed *model.PaperFile
		switch in.Action {
		case "upload":
			if len(in.Uploads) < 1 || len(in.Uploads) > 64 || len(files)+len(in.Uploads) > 64 {
				return apperr.Conflict("papers.file_limit", "Too many files")
			}
			for _, u := range in.Uploads {
				if err := validManagedSource(u.Path, u.Kind, u.Content); err != nil {
					return err
				}
				fid, err := uuid.NewV7()
				if err != nil {
					return err
				}
				f := model.PaperFile{Base: model.Base{ID: fid}, ProjectID: id, Path: u.Path, Kind: u.Kind, Content: u.Content, SHA256: hashText(u.Content), Revision: 1}
				files = append(files, f)
				created = append(created, f)
			}
		case "replace_text", "ai_apply", "ai_rollback":
			if len(in.Replacements) < 1 || len(in.Replacements) > 64 {
				return apperr.BadRequest("papers.invalid_action", "Provide 1-64 unique text replacements")
			}
			positions := make(map[uuid.UUID]int, len(files))
			for i, f := range files {
				positions[f.ID] = i
			}
			seen := map[uuid.UUID]bool{}
			for _, replacement := range in.Replacements {
				i, ok := positions[replacement.FileID]
				if !ok {
					return apperr.NotFound("papers.file_not_found", "File not found")
				}
				if seen[replacement.FileID] {
					return apperr.BadRequest("papers.invalid_action", "Duplicate replacement")
				}
				seen[replacement.FileID] = true
				f := files[i]
				if f.Kind != "text" {
					return apperr.BadRequest("papers.binary_read_only", "Binary file cannot be edited")
				}
				if replacement.BaseRevision < 1 || f.Revision != replacement.BaseRevision {
					return apperr.Conflict("papers.revision_conflict", "File has changed")
				}
				if err := validateText(replacement.Content); err != nil {
					return err
				}
				if f.Content == replacement.Content {
					continue
				}
				f.Content = replacement.Content
				f.SHA256 = hashText(f.Content)
				f.Revision++
				files[i] = f
				changed = append(changed, f)
			}
			if len(changed) == 0 {
				return apperr.BadRequest("papers.invalid_action", "No source changes")
			}
		case "rename", "delete", "set_main":
			if idx < 0 {
				return apperr.NotFound("papers.file_not_found", "File not found")
			}
			f := files[idx]
			switch in.Action {
			case "rename":
				if in.Path != f.Path {
					if err := validManagedSource(in.Path, f.Kind, f.Content); err != nil {
						return err
					}
					f.Path = in.Path
					f.Revision++
					files[idx] = f
					changed = append(changed, f)
				}
			case "delete":
				if f.ID == p.MainFileID {
					return apperr.Conflict("papers.main_file_required", "Choose another main TeX file before deleting this one")
				}
				removed = &f
				files = append(files[:idx], files[idx+1:]...)
			case "set_main":
				if f.Kind != "text" || !safeRootTex.MatchString(f.Path) {
					return apperr.BadRequest("papers.invalid_main_file", "Main file must be a portable root-level .tex file")
				}
				p.MainFileID = f.ID
			}
		default:
			return apperr.BadRequest("papers.invalid_action", "Unsupported file operation")
		}
		p.HeadRevision++
		if err := managedSnapshot(p, files); err != nil {
			return err
		}
		for _, f := range created {
			if err := tx.Create(&f).Error; err != nil {
				return err
			}
			if err := tx.Create(&[]model.PaperFileVersion{fileVersion(f, user.ID)}).Error; err != nil {
				return err
			}
		}
		for _, f := range changed {
			if err := tx.Model(&model.PaperFile{}).Where("id=? AND project_id=?", f.ID, id).Updates(map[string]any{"path": f.Path, "content": f.Content, "sha256": f.SHA256, "revision": f.Revision, "updated_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
			if err := tx.Create(&[]model.PaperFileVersion{fileVersion(f, user.ID)}).Error; err != nil {
				return err
			}
		}
		if removed != nil {
			if err := tx.Delete(removed).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&p).Updates(map[string]any{"main_file_id": p.MainFileID, "head_revision": p.HeadRevision, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		if aiMeta != nil {
			action := aiApplied
			if in.Action == "ai_rollback" {
				action = aiRolledBack
			}
			event, err := writePaperAIEvent(tx, user.ID, id, action, *aiMeta)
			if err != nil {
				return err
			}
			out.AIEventID = &event
		} else if err := audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.files." + in.Action, EntityType: "paper_project", EntityID: &id}); err != nil {
			return err
		}
		// List only metadata; do not echo a project's entire private source.
		if err := tx.Select("id", "project_id", "path", "kind", "sha256", "revision", "created_at", "updated_at").Where("project_id=?", id).Find(&out.Files).Error; err != nil {
			return err
		}
		sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
		out.Project = p
		return nil
	})
	return out, err
}

type TrashedProject struct {
	model.PaperProject
	TrashedAt time.Time `json:"trashed_at"`
}

func (s *Service) ListTrashedProjects(user authctx.CurrentUser) ([]TrashedProject, error) {
	if err := s.RequireAccess(user); err != nil {
		return nil, err
	}
	var rows []model.PaperProject
	if err := s.db.Unscoped().Where("owner_id=? AND deleted_at IS NOT NULL", user.ID).Order("deleted_at DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]TrashedProject, 0, len(rows))
	for _, p := range rows {
		result = append(result, TrashedProject{p, p.DeletedAt.Time})
	}
	return result, nil
}
func (s *Service) RestoreProject(user authctx.CurrentUser, id uuid.UUID) (model.PaperProject, error) {
	var p model.PaperProject
	if err := s.RequireAccess(user); err != nil {
		return p, err
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var owner model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("uuid").Where("uuid=?", user.ID).First(&owner).Error; err != nil {
			return err
		}
		err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND owner_id=? AND deleted_at IS NOT NULL", id, user.ID).First(&p).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("papers.project_not_found", "Project not found")
		}
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.PaperProject{}).Where("owner_id=?", user.ID).Count(&count).Error; err != nil {
			return err
		}
		if count >= maxProjects {
			return apperr.Conflict("papers.project_limit", "Project limit reached")
		}
		if s.compile != nil {
			if err := tx.Model(&model.PaperCompileJob{}).Where("project_id=? AND status IN ('queued','running','recovering')", id).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return apperr.Conflict("papers.compile_active", "Wait for the prior compile to finish")
			}
		}
		if err := tx.Unscoped().Model(&p).Updates(map[string]any{"deleted_at": nil, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		p.DeletedAt = gorm.DeletedAt{}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.project.restore", EntityType: "paper_project", EntityID: &id})
	})
	return p, err
}
