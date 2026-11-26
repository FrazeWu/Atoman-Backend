package papers

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	aiStarted    = "papers.ai.started"
	aiProposed   = "papers.ai.proposed"
	aiFailed     = "papers.ai.failed"
	aiApplied    = "papers.ai.applied"
	aiRolledBack = "papers.ai.rolled_back"
)

var paperAIEventActions = []string{aiStarted, aiProposed, aiFailed, aiApplied, aiRolledBack}

type paperAIAuditFile struct {
	FileID         uuid.UUID `json:"file_id"`
	BeforeRevision int64     `json:"before_revision"`
	AfterRevision  int64     `json:"after_revision"`
	BeforeSHA      string    `json:"before_sha256"`
	AfterSHA       string    `json:"after_sha256"`
}
type paperAIAuditMeta struct {
	Version         int                `json:"version"`
	AttemptID       uuid.UUID          `json:"attempt_id"`
	RelatedID       uuid.UUID          `json:"related_id"`
	Task            string             `json:"task"`
	ProjectRevision int64              `json:"project_revision"`
	Files           []paperAIAuditFile `json:"files,omitempty"`
	InputTokens     int64              `json:"input_tokens,omitempty"`
	OutputTokens    int64              `json:"output_tokens,omitempty"`
}
type paperAIActivity struct {
	Contributor     paperAIContributor    `json:"contributor"`
	Decision        string                `json:"decision"`
	ID              uuid.UUID             `json:"id"`
	Action          string                `json:"action"`
	CreatedAt       time.Time             `json:"created_at"`
	AttemptID       uuid.UUID             `json:"attempt_id"`
	RelatedID       uuid.UUID             `json:"related_id"`
	Task            string                `json:"task"`
	ProjectRevision int64                 `json:"project_revision"`
	Files           []paperAIActivityFile `json:"files"`
	InputTokens     int64                 `json:"input_tokens,omitempty"`
	OutputTokens    int64                 `json:"output_tokens,omitempty"`
}
type paperAIActivityFile struct {
	FileID         uuid.UUID `json:"file_id"`
	BeforeRevision int64     `json:"before_revision"`
	AfterRevision  int64     `json:"after_revision"`
}

func writePaperAIEvent(tx *gorm.DB, actor, pid uuid.UUID, action string, meta paperAIAuditMeta) (uuid.UUID, error) {
	meta.Version = 1
	raw, err := json.Marshal(meta)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	row := model.AuditLog{Base: model.Base{ID: id}, ActorID: &actor, Action: action, EntityType: "paper_project", EntityID: &pid, Metadata: string(raw)}
	if err := tx.Create(&row).Error; err != nil {
		return uuid.Nil, err
	}
	return id, nil
}
func readPaperAIEvent(row model.AuditLog) (paperAIAuditMeta, error) {
	var meta paperAIAuditMeta
	if err := json.Unmarshal([]byte(row.Metadata), &meta); err != nil {
		return meta, errors.New("invalid AI audit metadata")
	}
	if meta.Version != 1 || meta.AttemptID == uuid.Nil || meta.ProjectRevision < 1 || (meta.Task != "project" && meta.Task != "revise" && meta.Task != "explain") || len(meta.Files) > 5 {
		return meta, errors.New("invalid AI audit metadata")
	}
	seen := map[uuid.UUID]bool{}
	for _, file := range meta.Files {
		if file.FileID == uuid.Nil || seen[file.FileID] || file.BeforeRevision < 1 || file.AfterRevision != file.BeforeRevision+1 || len(file.BeforeSHA) != 64 || len(file.AfterSHA) != 64 || file.BeforeSHA == file.AfterSHA {
			return meta, errors.New("invalid AI audit file metadata")
		}
		seen[file.FileID] = true
	}
	return meta, nil
}
func paperAIEventQuery(tx *gorm.DB, user, pid uuid.UUID) *gorm.DB {
	return tx.Model(&model.AuditLog{}).Where("entity_type=? AND entity_id=? AND actor_id=? AND action IN ?", "paper_project", pid, user, paperAIEventActions)
}
func (s *Service) listPaperAIActivity(user authctx.CurrentUser, pid uuid.UUID) ([]paperAIActivity, error) {
	out := make([]paperAIActivity, 0)
	if err := s.RequireAccess(user); err != nil {
		return nil, err
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockOwned(tx, user, pid); err != nil {
			return err
		}
		if err := lockInvitationEligibility(tx, user.ID); err != nil {
			return err
		}
		var rows []model.AuditLog
		if err := paperAIEventQuery(tx, user.ID, pid).Order("created_at DESC, id DESC").Limit(50).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			m, err := readPaperAIEvent(row)
			if err != nil {
				return err
			}
			item := paperAIActivity{Contributor: paperAIIdentity(), Decision: paperAIActor(row.Action), ID: row.ID, Action: row.Action, CreatedAt: row.CreatedAt, AttemptID: m.AttemptID, RelatedID: m.RelatedID, Task: m.Task, ProjectRevision: m.ProjectRevision, Files: []paperAIActivityFile{}, InputTokens: m.InputTokens, OutputTokens: m.OutputTokens}
			for _, f := range m.Files {
				item.Files = append(item.Files, paperAIActivityFile{FileID: f.FileID, BeforeRevision: f.BeforeRevision, AfterRevision: f.AfterRevision})
			}
			out = append(out, item)
		}
		return nil
	})
	return out, err
}
func paperAIProposalFiles(edits []paperAIProjectEdit, approved map[string]model.PaperFile) []paperAIAuditFile {
	out := make([]paperAIAuditFile, 0, len(edits))
	for _, edit := range edits {
		f := approved[edit.Path]
		i := strings.Index(f.Content, edit.Before)
		next := f.Content[:i] + edit.After + f.Content[i+len(edit.Before):]
		out = append(out, paperAIAuditFile{FileID: f.ID, BeforeRevision: f.Revision, AfterRevision: f.Revision + 1, BeforeSHA: hashText(f.Content), AfterSHA: hashText(next)})
	}
	return out
}
func aiAuditConflict() error {
	return apperr.Conflict("papers.ai_audit_conflict", "AI operation no longer matches the saved file versions")
}
func preparePaperAIMutation(tx *gorm.DB, user authctx.CurrentUser, p model.PaperProject, files []model.PaperFile, in *FileChange) (*paperAIAuditMeta, error) {
	if in.Action != "ai_apply" && in.Action != "ai_rollback" {
		if in.AIEventID != nil {
			return nil, paperAIInvalid()
		}
		return nil, nil
	}
	if in.AIEventID == nil || *in.AIEventID == uuid.Nil {
		return nil, paperAIInvalid()
	}
	if err := lockInvitationEligibility(tx, user.ID); err != nil {
		return nil, err
	}
	action := aiProposed
	if in.Action == "ai_rollback" {
		action = aiApplied
	}
	var row model.AuditLog
	err := paperAIEventQuery(tx, user.ID, p.ID).Where("id=? AND action=?", *in.AIEventID, action).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperr.NotFound("papers.ai_event_not_found", "AI operation not found")
	}
	if err != nil {
		return nil, err
	}
	prior, err := readPaperAIEvent(row)
	if err != nil {
		return nil, err
	}
	if prior.Task != "project" || len(prior.Files) == 0 {
		return nil, aiAuditConflict()
	}
	next := &paperAIAuditMeta{AttemptID: prior.AttemptID, RelatedID: row.ID, Task: "project", ProjectRevision: p.HeadRevision + 1}
	current := map[uuid.UUID]model.PaperFile{}
	approved := map[uuid.UUID]paperAIAuditFile{}
	for _, file := range files {
		current[file.ID] = file
	}
	for _, file := range prior.Files {
		approved[file.FileID] = file
	}
	if in.Action == "ai_apply" {
		if prior.ProjectRevision != p.HeadRevision || len(in.Replacements) < 1 || len(in.Replacements) > len(prior.Files) {
			return nil, aiAuditConflict()
		}
		seen := map[uuid.UUID]bool{}
		for _, r := range in.Replacements {
			f, ok := current[r.FileID]
			allowed, ok2 := approved[r.FileID]
			if !ok || !ok2 || seen[r.FileID] || f.Kind != "text" || f.Revision != r.BaseRevision || f.Revision != allowed.BeforeRevision || hashText(f.Content) != allowed.BeforeSHA || f.SHA256 != allowed.BeforeSHA || hashText(r.Content) != allowed.AfterSHA {
				return nil, aiAuditConflict()
			}
			seen[r.FileID] = true
			next.Files = append(next.Files, allowed)
		}
	} else {
		if len(in.Replacements) != 0 {
			return nil, paperAIInvalid()
		}
		for _, allowed := range prior.Files {
			f, ok := current[allowed.FileID]
			if !ok || f.Kind != "text" || f.Revision != allowed.AfterRevision || f.SHA256 != allowed.AfterSHA || hashText(f.Content) != allowed.AfterSHA {
				return nil, aiAuditConflict()
			}
			var before, after model.PaperFileVersion
			for _, v := range []struct {
				revision int64
				dest     *model.PaperFileVersion
			}{{allowed.BeforeRevision, &before}, {allowed.AfterRevision, &after}} {
				if err := tx.Where("file_id=? AND revision=?", f.ID, v.revision).First(v.dest).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return nil, aiAuditConflict()
					}
					return nil, err
				}
			}
			if before.Path != f.Path || after.Path != f.Path || before.SHA256 != allowed.BeforeSHA || after.SHA256 != allowed.AfterSHA || hashText(before.Content) != allowed.BeforeSHA || hashText(after.Content) != allowed.AfterSHA {
				return nil, aiAuditConflict()
			}
			in.Replacements = append(in.Replacements, TextReplacement{FileID: f.ID, BaseRevision: f.Revision, Content: before.Content})
			next.Files = append(next.Files, paperAIAuditFile{FileID: f.ID, BeforeRevision: f.Revision, AfterRevision: f.Revision + 1, BeforeSHA: allowed.AfterSHA, AfterSHA: allowed.BeforeSHA})
		}
	}
	return next, nil
}
