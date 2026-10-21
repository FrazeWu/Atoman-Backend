package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

func metadataMergeTables(kind string) ([]bookMergeTable, error) {
	table, err := metadataTable(kind)
	if err != nil {
		return nil, err
	}
	column := "person_id"
	if kind == "edition" {
		column = "edition_id"
	}
	tables := []bookMergeTable{{table, "id IN ?"}, {"book_contributions", column + " IN ?"}, {"book_sources", "target_type IN ('" + kind + "','book_" + kind + "') AND target_id IN ?"}}
	if kind == "edition" {
		tables = append(tables, bookMergeTable{"book_assets", "edition_id IN ?"})
	}
	return tables, nil
}
func captureMetadataMerge(tx *gorm.DB, kind string, source, target uuid.UUID) (bookMergeSnapshot, error) {
	tables, err := metadataMergeTables(kind)
	if err != nil {
		return nil, err
	}
	snapshot := bookMergeSnapshot{}
	for _, table := range tables {
		if !tx.Migrator().HasTable(table.name) {
			continue
		}
		var raw string
		if err := tx.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "+table.name+" t WHERE "+table.predicate, []uuid.UUID{source, target}).Scan(&raw).Error; err != nil {
			return nil, err
		}
		snapshot[table.name] = json.RawMessage(raw)
	}
	return snapshot, nil
}
func lockMetadataMerge(tx *gorm.DB) error {
	// Fixed order shared with work merges; metadata edits hold row locks on these tables.
	for _, table := range []string{"book_works", "book_editions", "book_people", "book_contributions", "book_sources", "book_assets"} {
		if !tx.Migrator().HasTable(table) {
			continue
		}
		if err := tx.Exec("LOCK TABLE " + table + " IN EXCLUSIVE MODE").Error; err != nil {
			return err
		}
	}
	return nil
}
func metadataMergeHeads(tx *gorm.DB, input PreviewBookWorkMergeInput) (model.Revision, model.Revision, error) {
	table, err := metadataTable(input.EntityType)
	if err != nil {
		return model.Revision{}, model.Revision{}, err
	}
	var workID uuid.UUID
	for _, id := range []uuid.UUID{input.SourceID, input.TargetID} {
		var row struct {
			LifecycleStatus string
			EditStatus      string
			WorkID          uuid.UUID
		}
		if err := tx.Table(table).Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
			return model.Revision{}, model.Revision{}, publicBookNotFound(err)
		}
		if row.LifecycleStatus != "active" || row.EditStatus == "closed" {
			return model.Revision{}, model.Revision{}, apperr.Conflict("books.entry_closed", "Entry cannot be merged")
		}
		if input.EntityType == "edition" {
			if workID != uuid.Nil && workID != row.WorkID {
				return model.Revision{}, model.Revision{}, apperr.BadRequest("books.edition_work_mismatch", "Editions must belong to the same work")
			}
			workID = row.WorkID
		}
	}
	source, err := currentBookWikiRevision(tx, input.EntityType, input.SourceID)
	if err != nil {
		return source, model.Revision{}, err
	}
	target, err := currentBookWikiRevision(tx, input.EntityType, input.TargetID)
	return source, target, err
}
func (s *Service) previewBookMetadataMerge(ctx context.Context, user authctx.CurrentUser, input PreviewBookWorkMergeInput) (BookWorkMergePreview, error) {
	if user.ID == uuid.Nil {
		return BookWorkMergePreview{}, apperr.Unauthorized("Login required")
	}
	if err := validateBookMergeIDs(input); err != nil {
		return BookWorkMergePreview{}, err
	}
	var result BookWorkMergePreview
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, target, err := metadataMergeHeads(tx, input)
		if err != nil {
			return err
		}
		snapshot, err := captureMetadataMerge(tx, input.EntityType, input.SourceID, input.TargetID)
		if err != nil {
			return err
		}
		token, err := bookMergePreviewToken(snapshot, source, target)
		if err != nil {
			return err
		}
		counts := map[string]int{}
		for key, raw := range snapshot {
			var rows []json.RawMessage
			_ = json.Unmarshal(raw, &rows)
			counts[key] = len(rows)
		}
		result = BookWorkMergePreview{SourceID: input.SourceID, TargetID: input.TargetID, SourceRevisionID: source.ID, TargetRevisionID: target.ID, PreviewToken: token, ConflictPolicy: "保留目标资料；重复贡献关系去重；保留旧 ID 重定向和完整撤销日志", RecordCounts: counts}
		return nil
	})
	return result, err
}
func appendMetadataMergeRevision(tx *gorm.DB, kind string, id, editor uuid.UUID, editType string) (model.Revision, error) {
	if kind == "person" {
		var row model.BookPerson
		if err := tx.First(&row, "id = ?", id).Error; err != nil {
			return model.Revision{}, err
		}
		return appendBookWikiRevision(tx, kind, id, editor, "作者合并/撤销", editType, row)
	}
	var row model.BookEdition
	if err := tx.First(&row, "id = ?", id).Error; err != nil {
		return model.Revision{}, err
	}
	snapshot, err := loadBookEditionSnapshot(tx, row)
	if err != nil {
		return model.Revision{}, err
	}
	return appendBookWikiRevision(tx, kind, id, editor, "出版版本合并/撤销", editType, snapshot)
}
func (s *Service) mergeBookMetadata(ctx context.Context, user authctx.CurrentUser, input MergeBookWorksInput) (model.BookWikiMerge, error) {
	if user.ID == uuid.Nil {
		return model.BookWikiMerge{}, apperr.Unauthorized("Login required")
	}
	if err := validateBookMergeIDs(input.PreviewBookWorkMergeInput); err != nil {
		return model.BookWikiMerge{}, err
	}
	var result model.BookWikiMerge
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockMetadataMerge(tx); err != nil {
			return err
		}
		source, target, err := metadataMergeHeads(tx, input.PreviewBookWorkMergeInput)
		if err != nil {
			return err
		}
		before, err := captureMetadataMerge(tx, input.EntityType, input.SourceID, input.TargetID)
		if err != nil {
			return err
		}
		token, err := bookMergePreviewToken(before, source, target)
		if err != nil {
			return err
		}
		if token != input.PreviewToken || source.ID != input.SourceRevisionID || target.ID != input.TargetRevisionID {
			return apperr.Conflict("books.merge_preview_stale", "Preview again before merging")
		}
		column, other := "person_id", "s.edition_id IS NOT DISTINCT FROM t.edition_id AND s.work_id IS NOT DISTINCT FROM t.work_id"
		if input.EntityType == "edition" && tx.Migrator().HasTable(&model.BookAsset{}) {
			column = "edition_id"
			other = "s.person_id = t.person_id AND s.work_id IS NOT DISTINCT FROM t.work_id"
		}
		if err := tx.Exec("DELETE FROM book_contributions s USING book_contributions t WHERE s."+column+" = ? AND t."+column+" = ? AND s.role=t.role AND "+other, input.SourceID, input.TargetID).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE book_contributions SET "+column+" = ? WHERE "+column+" = ?", input.TargetID, input.SourceID).Error; err != nil {
			return err
		}
		if input.EntityType == "edition" {
			if err := tx.Model(&model.BookAsset{}).Where("edition_id = ?", input.SourceID).Update("edition_id", input.TargetID).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec("UPDATE book_sources SET target_id = ? WHERE target_id = ? AND target_type IN (?,?)", input.TargetID, input.SourceID, input.EntityType, "book_"+input.EntityType).Error; err != nil {
			return err
		}
		table, _ := metadataTable(input.EntityType)
		if err := tx.Table(table).Where("id = ?", input.SourceID).Updates(map[string]any{"lifecycle_status": "merged", "redirect_to": input.TargetID}).Error; err != nil {
			return err
		}
		source, err = appendMetadataMergeRevision(tx, input.EntityType, input.SourceID, user.ID, "merge")
		if err != nil {
			return err
		}
		target, err = appendMetadataMergeRevision(tx, input.EntityType, input.TargetID, user.ID, "merge")
		if err != nil {
			return err
		}
		after, err := captureMetadataMerge(tx, input.EntityType, input.SourceID, input.TargetID)
		if err != nil {
			return err
		}
		a, _ := json.Marshal(before)
		b, _ := json.Marshal(after)
		result = model.BookWikiMerge{SourceID: input.SourceID, TargetID: input.TargetID, CreatedBy: user.ID, Status: "merged", BeforeJSON: string(a), AfterJSON: string(b), SourceRevisionID: source.ID, TargetRevisionID: target.ID}
		return tx.Create(&result).Error
	})
	if err == nil {
		s.InvalidateCatalog()
	}
	return result, err
}
func (s *Service) revertBookMetadataMerge(ctx context.Context, user authctx.CurrentUser, journal model.BookWikiMerge, contentType string) (model.BookWikiMerge, error) {
	kind := strings.TrimPrefix(contentType, "book_")
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockMetadataMerge(tx); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&journal, "id = ?", journal.ID).Error; err != nil {
			return err
		}
		if journal.Status != "merged" {
			return apperr.Conflict("books.merge_already_reverted", "Already reverted")
		}
		for _, head := range []struct{ id, revision uuid.UUID }{{journal.SourceID, journal.SourceRevisionID}, {journal.TargetID, journal.TargetRevisionID}} {
			current, err := currentBookWikiRevision(tx, kind, head.id)
			if err != nil {
				return err
			}
			if current.ID != head.revision {
				return apperr.Conflict("books.merge_revert_conflict", "Metadata changed after merge")
			}
		}
		snapshot, err := captureMetadataMerge(tx, kind, journal.SourceID, journal.TargetID)
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(snapshot)
		var same bool
		if err := tx.Raw("SELECT ?::jsonb = ?::jsonb", string(encoded), journal.AfterJSON).Scan(&same).Error; err != nil {
			return err
		}
		if !same {
			return apperr.Conflict("books.merge_revert_conflict", "Relations changed after merge")
		}
		var before bookMergeSnapshot
		if err := json.Unmarshal([]byte(journal.BeforeJSON), &before); err != nil {
			return err
		}
		tables, _ := metadataMergeTables(kind)
		var rows []struct {
			ID              uuid.UUID
			LifecycleStatus string     `json:"lifecycle_status"`
			RedirectTo      *uuid.UUID `json:"redirect_to"`
		}
		if err := json.Unmarshal(before[tables[0].name], &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if err := tx.Table(tables[0].name).Where("id = ?", row.ID).Updates(map[string]any{"lifecycle_status": row.LifecycleStatus, "redirect_to": row.RedirectTo}).Error; err != nil {
				return err
			}
		}
		for _, table := range tables[1:] {
			if !tx.Migrator().HasTable(table.name) || len(before[table.name]) == 0 {
				continue
			}
			if table.name == "book_assets" {
				// Restore only edition ownership; never delete files with pinned revisions.
				if err := tx.Exec("UPDATE book_assets a SET edition_id = original.edition_id FROM jsonb_populate_recordset(NULL::book_assets, ?::jsonb) original WHERE a.id = original.id", string(before[table.name])).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Exec("DELETE FROM "+table.name+" WHERE "+table.predicate, []uuid.UUID{journal.SourceID, journal.TargetID}).Error; err != nil {
				return err
			}
			if err := tx.Exec("INSERT INTO "+table.name+" SELECT * FROM jsonb_populate_recordset(NULL::"+table.name+", ?::jsonb)", string(before[table.name])).Error; err != nil {
				return err
			}
		}
		for _, id := range []uuid.UUID{journal.SourceID, journal.TargetID} {
			if _, err := appendMetadataMergeRevision(tx, kind, id, user.ID, "merge_revert"); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		journal.Status = "reverted"
		journal.RevertedBy = &user.ID
		journal.RevertedAt = &now
		return tx.Model(&journal).Select("status", "reverted_by", "reverted_at", "updated_at").Updates(&journal).Error
	})
	if err == nil {
		s.InvalidateCatalog()
	}
	return journal, err
}
