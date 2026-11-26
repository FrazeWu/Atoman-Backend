package papers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/papersassets"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxProjects  = 20
	maxFiles     = 100
	maxTextBytes = 1 << 20
)

const initialTex = "\\documentclass{article}\n\\begin{document}\nHello, Atoman Papers!\n\\end{document}\n"

type Service struct {
	aiModels      paperAIModelCaller // injectable only in package tests.
	aiCall        paperAICaller      // injectable only in package tests; defaults to bounded HTTPS transport.
	db            *gorm.DB
	compile       *CompileRuntime       // nil by default: production stays fail-closed.
	collaboration *collaborationGateway // explicitly injected; normal service remains disabled.
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

func (s *Service) AccessAllowed(user authctx.CurrentUser) (bool, error) {
	if user.ID == uuid.Nil {
		return false, nil
	}
	var grant model.PaperAccessGrant
	err := s.db.Where("user_id = ? AND status = ?", user.ID, "active").First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return grant.ExpiresAt == nil || grant.ExpiresAt.After(time.Now().UTC()), nil
}

func (s *Service) RequireAccess(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	allowed, err := s.AccessAllowed(user)
	if err != nil {
		return err
	}
	if !allowed {
		return apperr.Forbidden("papers.invite_required", "Papers requires an active invitation")
	}
	return nil
}

func (s *Service) GrantAccess(actor authctx.CurrentUser, target uuid.UUID, expires *time.Time) error {
	if actor.ID == uuid.Nil || !authctx.RoleAtLeast(actor.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("papers.admin_required", "Administrator permission required")
	}
	if target == uuid.Nil {
		return apperr.BadRequest("papers.invalid_user", "user_id is required")
	}
	if expires != nil && !expires.After(time.Now().UTC()) {
		return apperr.BadRequest("papers.invalid_expiration", "Expiry must be in the future")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var existing model.User
		if err := tx.Where("uuid = ? AND is_active = ?", target, true).First(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("papers.user_not_found", "Active user not found")
			}
			return err
		}
		grant := model.PaperAccessGrant{UserID: target, Status: "active", GrantedBy: &actor.ID, ExpiresAt: expires}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"status", "granted_by", "expires_at", "updated_at"}),
		}).Create(&grant).Error; err != nil {
			return err
		}
		return audit.Record(tx, audit.Entry{ActorID: &actor.ID, Action: "papers.access.grant", EntityType: "user", EntityID: &target})
	})
}

func (s *Service) RevokeAccess(actor authctx.CurrentUser, target uuid.UUID) error {
	if actor.ID == uuid.Nil || !authctx.RoleAtLeast(actor.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("papers.admin_required", "Administrator permission required")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.PaperAccessGrant{}).Where("user_id = ? AND status = ?", target, "active").
			Updates(map[string]any{"status": "revoked", "updated_at": time.Now().UTC()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return apperr.NotFound("papers.grant_not_found", "Active invitation not found")
		}
		return audit.Record(tx, audit.Entry{ActorID: &actor.ID, Action: "papers.access.revoke", EntityType: "user", EntityID: &target})
	})
}

func validatedName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 100 ||
		strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return "", apperr.BadRequest("papers.invalid_name", "Project name must be 1-100 characters")
	}
	return name, nil
}

func validatedPath(filename string) (string, error) {
	if filename == "" || len(filename) > 512 || !utf8.ValidString(filename) ||
		strings.ContainsAny(filename, "\\\x00:") ||
		strings.IndexFunc(filename, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 ||
		strings.HasPrefix(filename, "/") || filename == "." || filename == ".." ||
		path.Clean(filename) != filename || strings.HasPrefix(filename, "../") {
		return "", apperr.BadRequest("papers.invalid_path", "Invalid project-relative path")
	}
	if papersassets.SourceKind(filename) != "text" && path.Base(filename) != ".latexmkrc" {
		return "", apperr.BadRequest("papers.invalid_file_type", "Unsupported text file type")
	}

	return filename, nil
}

func validateText(content string) error {
	if len(content) > maxTextBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return apperr.BadRequest("papers.invalid_content", "Text must be UTF-8, under 1 MiB, without NUL")
	}
	return nil
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func fileVersion(file model.PaperFile, author uuid.UUID) model.PaperFileVersion {
	return model.PaperFileVersion{ID: uuid.New(), FileID: file.ID, Revision: file.Revision, Path: file.Path, Content: file.Content, SHA256: file.SHA256, AuthorID: author}
}

func (s *Service) ListProjects(user authctx.CurrentUser) ([]model.PaperProject, error) {
	if err := s.RequireAccess(user); err != nil {
		return nil, err
	}
	// Use one statement so each listed role and visible project come from
	// the same database snapshot. A later request rechecks authorization.
	var rows []struct {
		model.PaperProject
		MemberRole string
	}
	err := s.db.Model(&model.PaperProject{}).
		Select("paper_projects.*, CASE WHEN paper_projects.owner_id = ? THEN 'owner' ELSE m.role END AS member_role", user.ID).
		Joins("LEFT JOIN paper_project_members m ON m.project_id=paper_projects.id AND m.user_id=? AND m.role IN ('editor','viewer')", user.ID).
		Where("paper_projects.owner_id=? OR m.user_id IS NOT NULL", user.ID).
		Order("paper_projects.updated_at DESC,paper_projects.id DESC").Find(&rows).Error
	projects := make([]model.PaperProject, 0, len(rows))
	for _, row := range rows {
		p := row.PaperProject
		p.AccessRole = row.MemberRole
		projects = append(projects, p)
	}
	return projects, err
}

func (s *Service) GetProject(user authctx.CurrentUser, id uuid.UUID) (model.PaperProject, error) {
	if err := s.RequireAccess(user); err != nil {
		return model.PaperProject{}, err
	}
	var p model.PaperProject
	err := s.db.Where("id = ? AND owner_id = ?", id, user.ID).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return p, apperr.NotFound("papers.project_not_found", "Project not found")
	}
	return p, err
}

func (s *Service) CreateProject(user authctx.CurrentUser, name string) (model.PaperProject, error) {
	if err := s.RequireAccess(user); err != nil {
		return model.PaperProject{}, err
	}
	clean, err := validatedName(name)
	if err != nil {
		return model.PaperProject{}, err
	}
	var project model.PaperProject
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// Serialize concurrent creations for this owner so the project limit
		// cannot be bypassed by two overlapping requests.
		var owner model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("uuid").Where("uuid = ?", user.ID).First(&owner).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.PaperProject{}).Where("owner_id = ?", user.ID).Count(&count).Error; err != nil {
			return err
		}
		if count >= maxProjects {
			return apperr.Conflict("papers.project_limit", "Project limit reached")
		}
		project = model.PaperProject{OwnerID: user.ID, Name: clean, HeadRevision: 1}
		if err := tx.Create(&project).Error; err != nil {
			return err
		}
		file := model.PaperFile{ProjectID: project.ID, Path: "main.tex", Kind: "text", Content: initialTex, SHA256: hashText(initialTex), Revision: 1}
		if err := tx.Create(&file).Error; err != nil {
			return err
		}
		if err := tx.Create(&[]model.PaperFileVersion{fileVersion(file, user.ID)}).Error; err != nil {
			return err
		}
		project.MainFileID = file.ID
		if err := tx.Model(&project).Update("main_file_id", file.ID).Error; err != nil {
			return err
		}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.project.create", EntityType: "paper_project", EntityID: &project.ID})
	})
	return project, err
}

func (s *Service) lockOwned(tx *gorm.DB, user authctx.CurrentUser, id uuid.UUID) (model.PaperProject, error) {
	var p model.PaperProject
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND owner_id = ?", id, user.ID).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return p, apperr.NotFound("papers.project_not_found", "Project not found")
	}
	return p, err
}

func (s *Service) RenameProject(user authctx.CurrentUser, id uuid.UUID, name string, base int64) (model.PaperProject, error) {
	if err := s.RequireAccess(user); err != nil {
		return model.PaperProject{}, err
	}
	clean, err := validatedName(name)
	if err != nil {
		return model.PaperProject{}, err
	}
	var p model.PaperProject
	err = s.db.Transaction(func(tx *gorm.DB) error {
		p, err = s.lockOwned(tx, user, id)
		if err != nil {
			return err
		}
		if p.HeadRevision != base {
			return apperr.Conflict("papers.revision_conflict", "Project has changed")
		}
		if p.Name == clean {
			return nil
		}
		p.Name = clean
		p.HeadRevision++
		return tx.Model(&p).Updates(map[string]any{"name": clean, "head_revision": p.HeadRevision, "updated_at": time.Now().UTC()}).Error
	})
	return p, err
}

func (s *Service) DeleteProject(user authctx.CurrentUser, id uuid.UUID) error {
	if err := s.RequireAccess(user); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		p, err := s.lockOwned(tx, user, id)
		if err != nil {
			return err
		}
		if err := tx.Delete(&p).Error; err != nil {
			return err
		}
		// Restoring a trashed project must not silently re-share it.
		if err := tx.Where("project_id=?", id).Delete(&model.PaperProjectMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id=? AND revoked_at IS NULL", id).Model(&model.PaperProjectInvitation{}).Update("revoked_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		// Invalidate collaboration epochs on project trash/restore. Accepted
		// canonical text and immutable file versions remain available.
		if err := tx.Where("project_id=?", id).Delete(&model.PaperCollaborativeDocument{}).Error; err != nil {
			return err
		}

		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.project.delete", EntityType: "paper_project", EntityID: &id})
	})
}

func (s *Service) ListFiles(user authctx.CurrentUser, projectID uuid.UUID) ([]model.PaperFile, error) {
	if err := s.RequireAccess(user); err != nil {
		return nil, err
	}
	files := make([]model.PaperFile, 0)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockAccessible(tx, user, projectID, false); err != nil {
			return err
		}
		return tx.Select("id", "project_id", "path", "kind", "sha256", "revision", "created_at", "updated_at").Where("project_id=?", projectID).Order("path ASC").Find(&files).Error
	})
	return files, err
}
func (s *Service) GetFile(user authctx.CurrentUser, projectID, fileID uuid.UUID) (model.PaperFile, error) {
	var file model.PaperFile
	if err := s.RequireAccess(user); err != nil {
		return file, err
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockAccessible(tx, user, projectID, false); err != nil {
			return err
		}
		err := tx.Where("id=? AND project_id=?", fileID, projectID).First(&file).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("papers.file_not_found", "File not found")
		}
		return err
	})
	return file, err
}
func (s *Service) CreateFile(user authctx.CurrentUser, projectID uuid.UUID, filename, content string) (model.PaperFile, error) {
	if err := s.RequireAccess(user); err != nil {
		return model.PaperFile{}, err
	}
	filename, err := validatedPath(filename)
	if err != nil {
		return model.PaperFile{}, err
	}
	if !papersassets.ValidSourcePath(filename) || papersassets.SourceKind(filename) != "text" {
		return model.PaperFile{}, apperr.BadRequest("papers.invalid_file_type", "Use a portable, supported LaTeX source filename")
	}
	if err := validateText(content); err != nil {
		return model.PaperFile{}, err
	}
	var file model.PaperFile
	err = s.db.Transaction(func(tx *gorm.DB) error {
		p, err := s.lockOwned(tx, user, projectID)
		if err != nil {
			return err
		}
		// The same namespace and count constraints as the immutable compile
		// snapshot: case-colliding paths and files masquerading as directories
		// must not turn an otherwise valid project into an unbuildable one.
		var existing []model.PaperFile
		if err := tx.Select("id", "path").Where("project_id = ?", projectID).Find(&existing).Error; err != nil {
			return err
		}
		extra := 0
		key := strings.ToLower(filename)
		for _, prior := range existing {
			if prior.ID == p.MainFileID && prior.Path != "main.tex" {
				extra = 1
			}
			old := strings.ToLower(prior.Path)
			if key == old || strings.HasPrefix(key, old+"/") || strings.HasPrefix(old, key+"/") {
				return apperr.Conflict("papers.file_exists", "File or directory path conflicts with an existing file")
			}
		}
		if extra > 0 && (key == "main.tex" || strings.HasPrefix(key, "main.tex/")) {
			return apperr.Conflict("papers.file_exists", "main.tex is reserved for the generated compile entrypoint")
		}
		if len(existing)+extra >= 64 {
			return apperr.Conflict("papers.file_limit", "Compile-compatible project limit is 64 source files, including the entrypoint")
		}
		var used int64
		if err := tx.Model(&model.PaperFile{}).Where("project_id = ?", projectID).Select("COALESCE(SUM(octet_length(content)),0)").Scan(&used).Error; err != nil {
			return err
		}
		if used+int64(len(content)) > papersassets.MaxSnapshotBytes {
			return apperr.Conflict("papers.compile_source_limit", "Project exceeds the 4 MiB encoded compile-source limit")
		}
		file = model.PaperFile{ProjectID: projectID, Path: filename, Kind: "text", Content: content, SHA256: hashText(content), Revision: 1}
		if err := tx.Create(&file).Error; err != nil {
			return err
		}
		if err := tx.Create(&[]model.PaperFileVersion{fileVersion(file, user.ID)}).Error; err != nil {
			return err
		}
		file.ProjectHeadRevision = p.HeadRevision + 1
		return tx.Model(&p).Updates(map[string]any{"head_revision": p.HeadRevision + 1, "updated_at": time.Now().UTC()}).Error
	})
	return file, err
}

func (s *Service) SaveFile(user authctx.CurrentUser, projectID, fileID uuid.UUID, content string, base int64) (model.PaperFile, error) {
	if err := s.RequireAccess(user); err != nil {
		return model.PaperFile{}, err
	}
	if err := validateText(content); err != nil {
		return model.PaperFile{}, err
	}
	if base < 1 {
		return model.PaperFile{}, apperr.BadRequest("papers.invalid_revision", "base_revision must be positive")
	}
	var file model.PaperFile
	err := s.db.Transaction(func(tx *gorm.DB) error {
		p, err := s.lockAccessible(tx, user, projectID, true)
		if err != nil {
			return err
		}
		err = tx.Where("id = ? AND project_id = ?", fileID, projectID).First(&file).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("papers.file_not_found", "File not found")
		}
		if err != nil {
			return err
		}
		if file.Kind != "text" {
			return apperr.BadRequest("papers.binary_read_only", "Imported binary resources cannot be edited as text")
		}
		// Retrying an accepted save after a lost HTTP response is safe when
		// the desired content is already stored; never overwrite other edits.
		file.ProjectHeadRevision = p.HeadRevision
		if file.Content == content && base <= file.Revision {
			return nil
		}
		if file.Revision != base {
			return apperr.Conflict("papers.revision_conflict", "File has changed")
		}
		if len(content) > len(file.Content) {
			var used int64
			if err := tx.Model(&model.PaperFile{}).Where("project_id = ?", projectID).Select("COALESCE(SUM(octet_length(content)),0)").Scan(&used).Error; err != nil {
				return err
			}
			if used-int64(len(file.Content))+int64(len(content)) > papersassets.MaxSnapshotBytes {
				return apperr.Conflict("papers.compile_source_limit", "Project exceeds the 4 MiB encoded compile-source limit")
			}
		}
		file.Content = content
		file.SHA256 = hashText(content)
		file.Revision++
		result := tx.Model(&model.PaperFile{}).Where("id = ? AND project_id = ? AND revision = ?", fileID, projectID, base).
			Updates(map[string]any{"content": content, "sha256": file.SHA256, "revision": file.Revision, "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return apperr.Conflict("papers.revision_conflict", "File has changed")
		}
		if err := tx.Create(&[]model.PaperFileVersion{fileVersion(file, user.ID)}).Error; err != nil {
			return err
		}
		file.ProjectHeadRevision = p.HeadRevision + 1
		return tx.Model(&p).Updates(map[string]any{"head_revision": p.HeadRevision + 1, "updated_at": time.Now().UTC()}).Error
	})
	return file, err
}
