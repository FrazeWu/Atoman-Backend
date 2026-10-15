package books

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PreviewBookWorkMergeInput struct {
	SourceID uuid.UUID `json:"source_id"`
	TargetID uuid.UUID `json:"target_id"`
}

type MergeBookWorksInput struct {
	PreviewBookWorkMergeInput
	PreviewToken     string    `json:"preview_token"`
	SourceRevisionID uuid.UUID `json:"source_revision_id"`
	TargetRevisionID uuid.UUID `json:"target_revision_id"`
}

type BookWorkMergePreview struct {
	SourceID         uuid.UUID      `json:"source_id"`
	TargetID         uuid.UUID      `json:"target_id"`
	SourceRevisionID uuid.UUID      `json:"source_revision_id"`
	TargetRevisionID uuid.UUID      `json:"target_revision_id"`
	PreviewToken     string         `json:"preview_token"`
	ConflictPolicy   string         `json:"conflict_policy"`
	RecordCounts     map[string]int `json:"record_counts"`
}

// Only fixed, server-owned table names/predicates enter the SQL below. Journal
// complete relation sets so an undo also detects newly inserted/deleted rows.
type bookMergeTable struct{ name, predicate string }

var bookWorkMergeTables = []bookMergeTable{
	{"book_works", "id IN ?"},
	{"book_editions", "work_id IN ?"},
	{"book_contributions", "work_id IN ?"},
	{"book_sources", "target_type IN ('work','book_work') AND target_id IN ?"},
	{"book_assets", "work_id IN ?"},
	{"book_default_assets", "work_id IN ?"},
	{"book_reading_selections", "work_id IN ?"},
	{"book_ratings", "work_id IN ?"},
	{"book_reviews", "work_id IN ?"},
	{"user_book_shelves", "work_id IN ?"},
	{"book_post_links", "work_id IN ?"},
}

type bookMergeSnapshot map[string]json.RawMessage

func validateBookMergeIDs(input PreviewBookWorkMergeInput) error {
	if input.SourceID == uuid.Nil || input.TargetID == uuid.Nil || input.SourceID == input.TargetID {
		return apperr.BadRequest("books.invalid_merge", "Two different work IDs are required")
	}
	return nil
}

func loadBookMergeHeads(tx *gorm.DB, input PreviewBookWorkMergeInput) (model.Revision, model.Revision, error) {
	for _, id := range []uuid.UUID{input.SourceID, input.TargetID} {
		var work model.BookWork
		if err := tx.Where("id = ? AND lifecycle_status = ? AND edit_status <> ?", id, model.BookLifecycleStatusActive, model.BookEditStatusClosed).First(&work).Error; err != nil {
			return model.Revision{}, model.Revision{}, publicBookNotFound(err)
		}
	}
	source, err := currentBookWikiRevision(tx, "work", input.SourceID)
	if err != nil {
		return source, model.Revision{}, publicBookNotFound(err)
	}
	target, err := currentBookWikiRevision(tx, "work", input.TargetID)
	if err != nil {
		return source, target, publicBookNotFound(err)
	}
	return source, target, nil
}

func captureBookMergeSnapshot(tx *gorm.DB, sourceID, targetID uuid.UUID) (bookMergeSnapshot, error) {
	snapshot := bookMergeSnapshot{}
	for _, table := range bookWorkMergeTables {
		if !tx.Migrator().HasTable(table.name) {
			continue
		}
		var raw string
		query := "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text), '[]'::jsonb)::text FROM " + table.name + " t WHERE " + table.predicate
		if err := tx.Raw(query, []uuid.UUID{sourceID, targetID}).Scan(&raw).Error; err != nil {
			return nil, err
		}
		snapshot[table.name] = json.RawMessage(raw)
	}
	return snapshot, nil
}

func bookMergePreviewToken(snapshot bookMergeSnapshot, source, target model.Revision) (string, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	// Bind both direction and current heads, even if metadata was reverted.
	hash := sha256.New()
	hash.Write(source.ID[:])
	hash.Write(target.ID[:])
	hash.Write(encoded)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Service) PreviewBookWorkMerge(ctx context.Context, user authctx.CurrentUser, input PreviewBookWorkMergeInput) (BookWorkMergePreview, error) {
	if user.ID == uuid.Nil {
		return BookWorkMergePreview{}, apperr.Unauthorized("Login required")
	}
	if err := validateBookMergeIDs(input); err != nil {
		return BookWorkMergePreview{}, err
	}
	var preview BookWorkMergePreview
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, target, err := loadBookMergeHeads(tx, input)
		if err != nil {
			return err
		}
		snapshot, err := captureBookMergeSnapshot(tx, input.SourceID, input.TargetID)
		if err != nil {
			return err
		}
		token, err := bookMergePreviewToken(snapshot, source, target)
		if err != nil {
			return err
		}
		counts := map[string]int{}
		for name, raw := range snapshot {
			var records []json.RawMessage
			if err := json.Unmarshal(raw, &records); err != nil {
				return err
			}
			counts[name] = len(records)
		}
		preview = BookWorkMergePreview{SourceID: input.SourceID, TargetID: input.TargetID, SourceRevisionID: source.ID, TargetRevisionID: target.ID, PreviewToken: token, ConflictPolicy: "keep_target", RecordCounts: counts}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return preview, err
}

func lockBookMergeGraph(tx *gorm.DB) error {
	// Merges are rare catalog operations. Briefly exclude writes to the book
	// relation tables, including inserts, instead of requiring every existing
	// rating/shelf writer to implement a new locking protocol. Plain reads work.
	if err := tx.Exec("SET LOCAL lock_timeout = '5s'").Error; err != nil {
		return err
	}
	for i, table := range bookWorkMergeTables {
		if !tx.Migrator().HasTable(table.name) {
			continue
		}
		mode := "SHARE ROW EXCLUSIVE"
		if i == 0 {
			mode = "EXCLUSIVE"
		} // Also excludes work SELECT FOR UPDATE.
		if err := tx.Exec("LOCK TABLE " + table.name + " IN " + mode + " MODE").Error; err != nil {
			return err
		}
	}
	return tx.Exec("SET CONSTRAINTS fk_book_assets_work, fk_book_revisions_asset, fk_book_default_asset, fk_book_selection_asset DEFERRED").Error
}

func (s *Service) MergeBookWorks(ctx context.Context, user authctx.CurrentUser, input MergeBookWorksInput) (model.BookWikiMerge, error) {
	if user.ID == uuid.Nil {
		return model.BookWikiMerge{}, apperr.Unauthorized("Login required")
	}
	if err := validateBookMergeIDs(input.PreviewBookWorkMergeInput); err != nil {
		return model.BookWikiMerge{}, err
	}
	if input.PreviewToken == "" || input.SourceRevisionID == uuid.Nil || input.TargetRevisionID == uuid.Nil {
		return model.BookWikiMerge{}, apperr.BadRequest("books.merge_preview_required", "Preview and current revisions are required")
	}
	var merge model.BookWikiMerge
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBookMergeGraph(tx); err != nil {
			return err
		}
		source, target, err := loadBookMergeHeads(tx, input.PreviewBookWorkMergeInput)
		if err != nil {
			return err
		}
		before, err := captureBookMergeSnapshot(tx, input.SourceID, input.TargetID)
		if err != nil {
			return err
		}
		token, err := bookMergePreviewToken(before, source, target)
		if err != nil {
			return err
		}
		if source.ID != input.SourceRevisionID || target.ID != input.TargetRevisionID || token != input.PreviewToken {
			return apperr.Conflict("books.merge_preview_stale", "Related data changed; preview the merge again")
		}
		if err := moveBookWorkRelations(tx, input.SourceID, input.TargetID); err != nil {
			return err
		}
		if err := tx.Model(&model.BookWork{}).Where("id = ?", input.SourceID).Updates(map[string]any{"lifecycle_status": model.BookLifecycleStatusMerged, "edit_status": model.BookEditStatusClosed, "redirect_to": input.TargetID}).Error; err != nil {
			return err
		}
		sourceRevision, targetRevision, err := appendBookMergeRevisions(tx, user.ID, input.SourceID, input.TargetID, "merge")
		if err != nil {
			return err
		}
		after, err := captureBookMergeSnapshot(tx, input.SourceID, input.TargetID)
		if err != nil {
			return err
		}
		beforeJSON, err := json.Marshal(before)
		if err != nil {
			return err
		}
		afterJSON, err := json.Marshal(after)
		if err != nil {
			return err
		}
		merge = model.BookWikiMerge{SourceID: input.SourceID, TargetID: input.TargetID, CreatedBy: user.ID, Status: "merged", BeforeJSON: string(beforeJSON), AfterJSON: string(afterJSON), SourceRevisionID: sourceRevision.ID, TargetRevisionID: targetRevision.ID}
		return tx.Create(&merge).Error
	})
	if err == nil {
		s.InvalidateCatalog()
	}
	return merge, err
}

func moveBookWorkRelations(tx *gorm.DB, sourceID, targetID uuid.UUID) error {
	for _, unique := range []struct{ table, key string }{
		{"book_ratings", "user_id"}, {"book_reviews", "user_id"}, {"user_book_shelves", "user_id"}, {"book_post_links", "post_id"},
		{"book_default_assets", "language"}, {"book_reading_selections", "user_id"},
	} {
		if !tx.Migrator().HasTable(unique.table) {
			continue
		}
		if err := tx.Exec("DELETE FROM "+unique.table+" s USING "+unique.table+" t WHERE s.work_id = ? AND t.work_id = ? AND s."+unique.key+" = t."+unique.key, sourceID, targetID).Error; err != nil {
			return err
		}
	}
	if err := tx.Exec(`DELETE FROM book_contributions s USING book_contributions t
WHERE s.work_id = ? AND t.work_id = ? AND s.person_id = t.person_id AND s.role = t.role
AND s.edition_id IS NOT DISTINCT FROM t.edition_id`, sourceID, targetID).Error; err != nil {
		return err
	}
	for _, table := range bookWorkMergeTables {
		if table.name == "book_works" || table.name == "book_sources" || !tx.Migrator().HasTable(table.name) {
			continue
		}
		if err := tx.Exec("UPDATE "+table.name+" SET work_id = ? WHERE work_id = ?", targetID, sourceID).Error; err != nil {
			return err
		}
	}
	return tx.Exec("UPDATE book_sources SET target_id = ? WHERE target_type IN ('work','book_work') AND target_id = ?", targetID, sourceID).Error
}

func appendBookMergeRevisions(tx *gorm.DB, userID, sourceID, targetID uuid.UUID, editType string) (model.Revision, model.Revision, error) {
	var revisions []model.Revision
	for _, id := range []uuid.UUID{sourceID, targetID} {
		var work model.BookWork
		if err := tx.First(&work, "id = ?", id).Error; err != nil {
			return model.Revision{}, model.Revision{}, err
		}
		snapshot, err := loadBookWorkSnapshot(tx, work)
		if err != nil {
			return model.Revision{}, model.Revision{}, err
		}
		revision, err := appendBookWikiRevision(tx, "work", id, userID, "作品合并/撤销", editType, snapshot)
		if err != nil {
			return model.Revision{}, model.Revision{}, err
		}
		revisions = append(revisions, revision)
	}
	return revisions[0], revisions[1], nil
}

func (s *Service) RevertBookWorkMerge(ctx context.Context, user authctx.CurrentUser, id uuid.UUID) (model.BookWikiMerge, error) {
	if user.ID == uuid.Nil {
		return model.BookWikiMerge{}, apperr.Unauthorized("Login required")
	}
	var merge model.BookWikiMerge
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockBookMergeGraph(tx); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&merge, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("books.merge_not_found", "Merge not found")
			}
			return err
		}
		if merge.Status != "merged" {
			return apperr.Conflict("books.merge_already_reverted", "Merge is already reverted")
		}
		for _, head := range []struct{ entityID, revisionID uuid.UUID }{{merge.SourceID, merge.SourceRevisionID}, {merge.TargetID, merge.TargetRevisionID}} {
			current, err := currentBookWikiRevision(tx, "work", head.entityID)
			if err != nil {
				return err
			}
			if current.ID != head.revisionID {
				return apperr.Conflict("books.merge_revert_conflict", "Entry revisions changed after the merge")
			}
		}
		current, err := captureBookMergeSnapshot(tx, merge.SourceID, merge.TargetID)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(current)
		if err != nil {
			return err
		}
		var unchanged bool
		if err := tx.Raw("SELECT ?::jsonb = ?::jsonb", string(encoded), merge.AfterJSON).Scan(&unchanged).Error; err != nil {
			return err
		}
		if !unchanged {
			return apperr.Conflict("books.merge_revert_conflict", "Related data changed after the merge; undo would overwrite newer data")
		}
		var before bookMergeSnapshot
		if err := json.Unmarshal([]byte(merge.BeforeJSON), &before); err != nil {
			return err
		}
		// FKs are deferred inside this transaction; file revisions themselves are
		// never deleted or rewritten, and per-revision progress stays untouched.
		for _, table := range bookWorkMergeTables {
			raw, exists := before[table.name]
			if !exists {
				continue
			}
			if err := tx.Exec("DELETE FROM "+table.name+" WHERE "+table.predicate, []uuid.UUID{merge.SourceID, merge.TargetID}).Error; err != nil {
				return err
			}
			query := fmt.Sprintf("INSERT INTO %s SELECT * FROM jsonb_populate_recordset(NULL::%s, ?::jsonb)", table.name, table.name)
			if err := tx.Exec(query, string(raw)).Error; err != nil {
				return err
			}
		}
		if _, _, err := appendBookMergeRevisions(tx, user.ID, merge.SourceID, merge.TargetID, "merge_revert"); err != nil {
			return err
		}
		now := time.Now().UTC()
		merge.Status = "reverted"
		merge.RevertedBy = &user.ID
		merge.RevertedAt = &now
		return tx.Model(&merge).Select("status", "reverted_by", "reverted_at", "updated_at").Updates(&merge).Error
	})
	if err == nil {
		s.InvalidateCatalog()
	}
	return merge, err
}
