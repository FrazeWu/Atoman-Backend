package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"time"
)

// Work-level timeline preserves the exact immutable file behind every mark.
type WorkNoteSource struct {
	WorkID       uuid.UUID  `json:"work_id"`
	EditionID    *uuid.UUID `json:"edition_id,omitempty"`
	AssetID      uuid.UUID  `json:"asset_id"`
	RevisionID   uuid.UUID  `json:"revision_id"`
	FileName     string     `json:"file_name"`
	Format       string     `json:"format"`
	EditionLabel string     `json:"edition_label,omitempty"`
	CanOpen      bool       `json:"can_open"`
}
type WorkNoteItem struct {
	ID        uuid.UUID              `json:"id"`
	Type      string                 `json:"type"`
	Title     string                 `json:"title"`
	Quote     string                 `json:"quote"`
	Note      string                 `json:"note"`
	Color     string                 `json:"color,omitempty"`
	Style     string                 `json:"style,omitempty"`
	Anchor    *BookReadingMarkAnchor `json:"anchor,omitempty"`
	Source    *WorkNoteSource        `json:"source,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}
type WorkNotesResult struct {
	WorkID uuid.UUID      `json:"work_id"`
	Total  int64          `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
	Items  []WorkNoteItem `json:"items"`
}
type WorkNotesFilter struct {
	Kind          string
	RevisionID    uuid.UUID
	Limit, Offset int
}
type workNoteRow struct {
	ID         uuid.UUID  `gorm:"column:id"`
	Type       string     `gorm:"column:type"`
	RevisionID *uuid.UUID `gorm:"column:revision_id"`
	AnchorJSON string     `gorm:"column:anchor_json"`
	Title      string     `gorm:"column:title"`
	Quote      string     `gorm:"column:quote"`
	Note       string     `gorm:"column:note"`
	Color      string     `gorm:"column:color"`
	Style      string     `gorm:"column:style"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
}
type workNoteSourceRow struct {
	RevisionID     uuid.UUID  `gorm:"column:revision_id"`
	WorkID         uuid.UUID  `gorm:"column:work_id"`
	EditionID      *uuid.UUID `gorm:"column:edition_id"`
	AssetID        uuid.UUID  `gorm:"column:asset_id"`
	FileName       string     `gorm:"column:file_name"`
	Format         string     `gorm:"column:format"`
	EditionLabel   string     `gorm:"column:edition_label"`
	RevisionStatus string     `gorm:"column:revision_status"`
	AssetStatus    string     `gorm:"column:asset_status"`
	WorkStatus     string     `gorm:"column:work_status"`
	WorkEditStatus string     `gorm:"column:work_edit_status"`
	RevisionExists bool       `gorm:"column:revision_exists"`
	AssetExists    bool       `gorm:"column:asset_exists"`
	WorkExists     bool       `gorm:"column:work_exists"`
	FileOwner      uuid.UUID  `gorm:"column:file_owner"`
	Public         bool       `gorm:"column:public"`
}

func (s *Service) ListWorkNotes(ctx context.Context, user authctx.CurrentUser, workID uuid.UUID, filter WorkNotesFilter) (WorkNotesResult, error) {
	out := WorkNotesResult{Items: []WorkNoteItem{}}
	if user.ID == uuid.Nil {
		return out, apperr.Unauthorized("Login required")
	}
	if workID == uuid.Nil {
		return out, apperr.BadRequest("books.invalid_work", "Invalid work ID")
	}
	if filter.Kind == "" {
		filter.Kind = "all"
	}
	if filter.Kind != "all" && filter.Kind != "highlight" && filter.Kind != "chapter_note" {
		return out, apperr.BadRequest("books.invalid_note_type", "Invalid note type")
	}
	if filter.Limit < 0 || filter.Limit > 100 || filter.Offset < 0 || filter.Offset > 10000 {
		return out, apperr.BadRequest("books.invalid_pagination", "Invalid notes pagination")
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	out.Limit, out.Offset = filter.Limit, filter.Offset
	work, err := s.GetBookWork(ctx, workID)
	if err != nil {
		return out, err
	}
	out.WorkID, err = uuid.Parse(work.ID)
	if err != nil {
		return out, err
	}
	aliases, err := bookNoteWorkAliases(s.db.WithContext(ctx), out.WorkID)
	if err != nil {
		return out, err
	}
	if len(aliases) == 0 {
		return out, nil
	}
	markSQL := `SELECT m.id, 'highlight'::text AS type, m.revision_id, m.anchor_json::text AS anchor_json, m.label AS title, m.quote, m.note, m.color, m.style, m.created_at, m.updated_at FROM book_reading_marks m JOIN book_asset_revisions r ON r.id = m.revision_id JOIN book_assets a ON a.id = r.asset_id WHERE m.deleted_at IS NULL AND m.kind = 'highlight' AND m.user_id = ? AND a.work_id IN ? AND ? = true`
	privateSQL := `SELECT n.id, 'chapter_note'::text AS type, n.source_revision_id AS revision_id, '{}'::text AS anchor_json, n.title, ''::text AS quote, n.body AS note, ''::text AS color, ''::text AS style, n.created_at, n.updated_at FROM book_private_notes n WHERE n.deleted_at IS NULL AND n.purged_at IS NULL AND n.user_id = ? AND n.work_id IN ? AND ? = true`
	markArgs := []any{user.ID, aliases, filter.Kind != "chapter_note"}
	privateArgs := []any{user.ID, aliases, filter.Kind != "highlight"}
	if filter.RevisionID != uuid.Nil {
		markSQL += " AND m.revision_id = ?"
		markArgs = append(markArgs, filter.RevisionID)
		privateSQL += " AND n.source_revision_id = ?"
		privateArgs = append(privateArgs, filter.RevisionID)
	}
	union := fmt.Sprintf("(%s UNION ALL %s)", markSQL, privateSQL)
	args := append(markArgs, privateArgs...)
	if err := s.db.WithContext(ctx).Raw("SELECT count(*) FROM "+union+" notes", args...).Scan(&out.Total).Error; err != nil {
		return out, err
	}
	var records []workNoteRow
	pageArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	if err := s.db.WithContext(ctx).Raw("SELECT * FROM "+union+" notes ORDER BY updated_at DESC, id ASC LIMIT ? OFFSET ?", pageArgs...).Scan(&records).Error; err != nil {
		return out, err
	}
	ids := make([]uuid.UUID, 0, len(records))
	for _, record := range records {
		if record.RevisionID != nil {
			ids = append(ids, *record.RevisionID)
		}
	}
	sources := map[uuid.UUID]WorkNoteSource{}
	if len(ids) > 0 {
		var rows []workNoteSourceRow
		query := `SELECT r.id AS revision_id, a.id AS asset_id, a.work_id, a.edition_id, r.original_filename AS file_name, a.format, COALESCE(e.edition_statement, '') AS edition_label, r.status AS revision_status, a.status AS asset_status, COALESCE(w.lifecycle_status, '') AS work_status, COALESCE(w.edit_status, '') AS work_edit_status, r.created_by AS file_owner, COALESCE(v.public, false) AS public, r.deleted_at IS NULL AS revision_exists, a.deleted_at IS NULL AS asset_exists, w.deleted_at IS NULL AS work_exists FROM book_asset_revisions r JOIN book_assets a ON a.id = r.asset_id LEFT JOIN book_editions e ON e.id = a.edition_id LEFT JOIN book_works w ON w.id = a.work_id LEFT JOIN book_file_visibilities v ON v.revision_id = r.id WHERE r.id IN ?`
		if err := s.db.WithContext(ctx).Raw(query, ids).Scan(&rows).Error; err != nil {
			return out, err
		}
		for _, row := range rows {
			allowed := row.FileOwner == user.ID || row.Public
			canOpen := allowed && row.RevisionExists && row.AssetExists && row.WorkExists && row.RevisionStatus == model.BookRevisionStatusReady && row.AssetStatus == model.BookResourceStatusActive && row.WorkStatus == model.BookLifecycleStatusActive && row.WorkEditStatus != model.BookEditStatusClosed
			name, format, label := row.FileName, row.Format, row.EditionLabel
			if !allowed {
				name, format, label = "来源文件不可访问", "", ""
			}
			sources[row.RevisionID] = WorkNoteSource{WorkID: row.WorkID, EditionID: row.EditionID, AssetID: row.AssetID, RevisionID: row.RevisionID, FileName: name, Format: format, EditionLabel: label, CanOpen: canOpen}
		}
	}
	for _, record := range records {
		item := WorkNoteItem{ID: record.ID, Type: record.Type, Title: record.Title, Quote: record.Quote, Note: record.Note, Color: record.Color, Style: record.Style, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
		if record.Type == "highlight" {
			var anchor BookReadingMarkAnchor
			if err := json.Unmarshal([]byte(record.AnchorJSON), &anchor); err != nil {
				return out, err
			}
			item.Anchor = &anchor
		}
		if record.RevisionID != nil {
			if source, ok := sources[*record.RevisionID]; ok {
				copied := source
				item.Source = &copied
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
