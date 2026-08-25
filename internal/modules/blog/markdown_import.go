package blog

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxBlogMarkdownImportBytes = 2 << 20

type MarkdownImportPreview struct {
	ImportID    uuid.UUID                            `json:"import_id"`
	DraftID     uuid.UUID                            `json:"draft_id"`
	Title       string                               `json:"title"`
	Summary     string                               `json:"summary"`
	Content     string                               `json:"content"`
	Diagnostics []model.BlogMarkdownImportDiagnostic `json:"diagnostics"`
}

type MarkdownImportDetails struct {
	ID          uuid.UUID                            `json:"import_id"`
	DraftID     *uuid.UUID                           `json:"draft_id,omitempty"`
	ContentID   *uuid.UUID                           `json:"content_id,omitempty"`
	Status      string                               `json:"status"`
	FileName    string                               `json:"file_name"`
	Title       string                               `json:"title"`
	Summary     string                               `json:"summary"`
	Content     string                               `json:"content"`
	Diagnostics []model.BlogMarkdownImportDiagnostic `json:"diagnostics"`
}

func (s *Service) GetMarkdownImport(user authctx.CurrentUser, importID uuid.UUID) (MarkdownImportDetails, error) {
	if user.ID == uuid.Nil {
		return MarkdownImportDetails{}, apperr.Unauthorized("Login required")
	}
	var entry model.BlogMarkdownImport
	if err := s.db.Where("id = ? AND user_id = ?", importID, user.ID).First(&entry).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return MarkdownImportDetails{}, apperr.NotFound("blog.import_not_found", "Markdown import not found")
		}
		return MarkdownImportDetails{}, err
	}
	var diagnostics []model.BlogMarkdownImportDiagnostic
	if err := s.db.Where("import_id = ?", entry.ID).Order("line ASC, created_at ASC").Find(&diagnostics).Error; err != nil {
		return MarkdownImportDetails{}, err
	}
	return MarkdownImportDetails{ID: entry.ID, DraftID: entry.DraftID, ContentID: entry.ContentID, Status: entry.Status, FileName: entry.FileName, Title: entry.Title, Summary: entry.Summary, Content: entry.Content, Diagnostics: diagnostics}, nil
}

func (s *Service) ConfirmMarkdownImport(user authctx.CurrentUser, importID, contentID uuid.UUID) (MarkdownImportDetails, error) {
	if user.ID == uuid.Nil {
		return MarkdownImportDetails{}, apperr.Unauthorized("Login required")
	}
	if contentID == uuid.Nil {
		return MarkdownImportDetails{}, apperr.BadRequest("blog.import_invalid_content", "content_id is required")
	}
	before, err := s.GetMarkdownImport(user, importID)
	if err != nil {
		return MarkdownImportDetails{}, err
	}
	content, err := loadCanonicalBlogContent(s.db, contentID)
	if err != nil {
		return MarkdownImportDetails{}, err
	}
	if content.UserID != user.ID {
		return MarkdownImportDetails{}, apperr.Forbidden("blog.import_content_forbidden", "You can only confirm your own content")
	}
	var alreadyConfirmed bool
	err = s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.BlogMarkdownImport{}).
			Where("id = ? AND user_id = ? AND status = ?", importID, user.ID, "preview").
			Updates(map[string]any{"status": "confirmed", "content_id": contentID})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			alreadyConfirmed = true
			return nil
		}
		if before.DraftID == nil {
			return apperr.NotFound("blog.import_draft_not_found", "Markdown import draft not found")
		}
		draftResult := tx.Model(&model.ContentBlogDraft{}).
			Where("id = ? AND user_id = ?", *before.DraftID, user.ID).
			Updates(map[string]any{"content_id": contentID})
		if draftResult.Error != nil {
			return draftResult.Error
		}
		if draftResult.RowsAffected != 1 {
			return apperr.NotFound("blog.import_draft_not_found", "Markdown import draft not found")
		}
		return nil
	})
	if err != nil {
		return MarkdownImportDetails{}, err
	}
	if alreadyConfirmed {
		details, err := s.GetMarkdownImport(user, importID)
		if err != nil {
			return MarkdownImportDetails{}, err
		}
		if details.Status == "confirmed" && details.ContentID != nil && *details.ContentID == contentID {
			return details, nil
		}
		return MarkdownImportDetails{}, apperr.Conflict("blog.import_already_confirmed", "Markdown import has already been confirmed")
	}
	return s.GetMarkdownImport(user, importID)
}

func (s *Service) PreviewMarkdownImport(user authctx.CurrentUser, fileName string, raw []byte) (MarkdownImportPreview, error) {
	if user.ID == uuid.Nil {
		return MarkdownImportPreview{}, apperr.Unauthorized("Login required")
	}
	if len(raw) == 0 || len(raw) > maxBlogMarkdownImportBytes {
		return MarkdownImportPreview{}, apperr.BadRequest("blog.import_invalid_size", "Markdown file must be between 1 byte and 2 MiB")
	}
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return MarkdownImportPreview{}, apperr.BadRequest("blog.import_invalid_file", "Markdown file name is required")
	}
	title, summary, content, diagnostics := parseBlogMarkdownImport(string(raw))
	if title == "" {
		title = strings.TrimSuffix(fileName, ".markdown")
		title = strings.TrimSuffix(title, ".md")
	}
	if strings.TrimSpace(content) == "" {
		return MarkdownImportPreview{}, apperr.BadRequest("blog.import_empty_content", "Markdown content is required")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	contextKey := "markdown-import:" + hash
	preview := MarkdownImportPreview{Title: title, Summary: summary, Content: content}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		draft := model.ContentBlogDraft{UserID: user.ID, ContextKey: contextKey, Title: title, Summary: summary, Content: content, Visibility: "public"}
		if err := tx.Clauses(clauseOnConflictDraft()).Create(&draft).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ? AND context_key = ?", user.ID, contextKey).First(&draft).Error; err != nil {
			return err
		}
		entry := model.BlogMarkdownImport{UserID: user.ID, DraftID: &draft.ID, FileName: fileName, ContentHash: hash, Status: "preview", Title: title, Summary: summary, Content: content}
		if err := tx.Create(&entry).Error; err != nil {
			return err
		}
		for index := range diagnostics {
			diagnostics[index].ImportID = entry.ID
		}
		if len(diagnostics) > 0 {
			if err := tx.Create(&diagnostics).Error; err != nil {
				return err
			}
		}
		preview.ImportID, preview.DraftID = entry.ID, draft.ID
		return nil
	})
	return preview, err
}

func clauseOnConflictDraft() clause.OnConflict {
	return clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "context_key"}}, DoUpdates: clause.Assignments(map[string]any{"title": gorm.Expr("EXCLUDED.title"), "summary": gorm.Expr("EXCLUDED.summary"), "content": gorm.Expr("EXCLUDED.content"), "visibility": "public"})}
}

func parseBlogMarkdownImport(raw string) (title, summary, content string, diagnostics []model.BlogMarkdownImportDiagnostic) {
	content = strings.TrimSpace(raw)
	lines := strings.Split(content, "\n")
	if len(lines) > 2 && strings.TrimSpace(lines[0]) == "---" {
		for index := 1; index < len(lines); index++ {
			line := strings.TrimSpace(lines[index])
			if line == "---" {
				content = strings.TrimSpace(strings.Join(lines[index+1:], "\n"))
				break
			}
			key, value, found := strings.Cut(line, ":")
			if !found {
				diagnostics = append(diagnostics, model.BlogMarkdownImportDiagnostic{Level: "warning", Code: "front_matter_invalid", Message: "Unrecognized front matter field", Line: index + 1})
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), "\"'")
			switch strings.TrimSpace(strings.ToLower(key)) {
			case "title":
				title = value
			case "summary", "description":
				summary = value
			}
		}
	}
	if title == "" {
		for index, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "# ") {
				title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "# "))
				content = strings.TrimSpace(strings.Join(strings.Split(content, "\n")[index+1:], "\n"))
				break
			}
		}
	}
	for index, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "](") && (strings.Contains(line, "http://") || strings.Contains(line, "https://")) {
			diagnostics = append(diagnostics, model.BlogMarkdownImportDiagnostic{Level: "warning", Code: "external_resource_preserved", Message: "External resource URL was preserved without downloading", Line: index + 1})
		}
	}
	return title, summary, content, diagnostics
}
