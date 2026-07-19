package migrations

import (
	"encoding/json"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const debateRevisionContentType = "debate"

type debateRevisionSnapshot struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
}

// RunDebateWikiMigration replaces the legacy argument graph with the debate
// wiki schema. Destructive cleanup is gated by columns/tables that only exist
// in the legacy schema, so rerunning it preserves new votes and projections.
func RunDebateWikiMigration(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		legacy := hasLegacyDebateSchema(tx)
		if legacy {
			if err := cleanLegacyDebateData(tx); err != nil {
				return err
			}
		}

		if err := tx.AutoMigrate(
			&model.Debate{},
			&model.Revision{},
			&model.ContentProtection{},
			&model.DebateConclusionEvent{},
			&model.DebateRevisionReference{},
			&model.DebateRelation{},
			&model.DebateVote{},
		); err != nil {
			return err
		}

		if err := backfillDebateRevisions(tx, legacy); err != nil {
			return err
		}
		if legacy {
			return dropLegacyDebateColumns(tx)
		}
		return nil
	})
}

func hasLegacyDebateSchema(db *gorm.DB) bool {
	return db.Migrator().HasColumn("debate_votes", "argument_id") ||
		db.Migrator().HasColumn("debate_relations", "user_id") ||
		db.Migrator().HasTable("debate_argument_details")
}

func cleanLegacyDebateData(db *gorm.DB) error {
	if db.Migrator().HasTable("debate_argument_details") {
		var argumentIDs []uuid.UUID
		if err := db.Table("debate_argument_details").Pluck("comment_id", &argumentIDs).Error; err != nil {
			return err
		}
		if len(argumentIDs) > 0 {
			for _, table := range []string{"comment_mentions", "comment_attachments", "comment_likes", "comment_reports"} {
				if db.Migrator().HasTable(table) {
					if err := db.Exec("DELETE FROM "+table+" WHERE comment_id IN ?", argumentIDs).Error; err != nil {
						return err
					}
				}
			}
			if db.Migrator().HasTable("comment_entries") {
				if err := db.Exec("DELETE FROM comment_entries WHERE id IN ?", argumentIDs).Error; err != nil {
					return err
				}
			}
		}
	}

	for _, table := range []string{
		"debate_argument_references",
		"debate_argument_debate_refs",
		"debate_argument_details",
		"vote_histories",
		"debate_conclude_votes",
	} {
		if db.Migrator().HasTable(table) {
			if err := db.Migrator().DropTable(table); err != nil {
				return err
			}
		}
	}
	if db.Migrator().HasTable("debate_relations") {
		if err := db.Migrator().DropTable("debate_relations"); err != nil {
			return err
		}
	}
	if db.Migrator().HasColumn("debate_votes", "argument_id") {
		if err := db.Migrator().DropTable("debate_votes"); err != nil {
			return err
		}
	}
	return nil
}

func backfillDebateRevisions(db *gorm.DB, legacy bool) error {
	if !db.Migrator().HasTable("debates") {
		return nil
	}
	type debateRow struct {
		ID                uuid.UUID
		UserID            uuid.UUID
		Title             string
		Description       string
		Content           string
		Tags              string
		Status            string
		CreatedAt         time.Time
		CurrentRevisionID *uuid.UUID
	}
	var debates []debateRow
	if err := db.Table("debates").Select("id, user_id, title, description, content, tags, status, created_at, current_revision_id").Scan(&debates).Error; err != nil {
		return err
	}
	for _, debate := range debates {
		updates := map[string]any{}
		if debate.Status == "open" || debate.Status == "concluded" {
			updates["status"] = model.DebateStatusActive
		}
		if legacy {
			updates["conclusion_type"] = ""
			updates["current_conclusion_event_id"] = nil
		}

		var revision model.Revision
		result := db.Where("content_type = ? AND content_id = ? AND version_number = ?", debateRevisionContentType, debate.ID, 1).Limit(1).Find(&revision)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			snapshot, err := json.Marshal(debateRevisionSnapshot{
				Title: debate.Title, Description: debate.Description, Content: debate.Content, Tags: parseLegacyTags(debate.Tags),
			})
			if err != nil {
				return err
			}
			revision = model.Revision{
				ContentType: debateRevisionContentType, ContentID: debate.ID, VersionNumber: 1,
				ContentSnapshot: snapshot, EditorID: debate.UserID, EditSummary: "Initial version",
				EditType: "creation", Status: "approved", IsCurrent: true, CreatedAt: debate.CreatedAt,
			}
			if err := db.Create(&revision).Error; err != nil {
				return err
			}
		}
		if debate.CurrentRevisionID == nil {
			updates["current_revision_id"] = revision.ID
		}
		if len(updates) > 0 {
			if err := db.Table("debates").Where("id = ?", debate.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func parseLegacyTags(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []string{}
	}
	var tags []string
	if json.Unmarshal([]byte(trimmed), &tags) == nil {
		return tags
	}
	if pq.Array(&tags).Scan(trimmed) == nil {
		return tags
	}
	return []string{}
}

func dropLegacyDebateColumns(db *gorm.DB) error {
	for _, column := range []string{
		"argument_count", "vote_count", "conclusion_summary", "conclude_vote_count", "conclude_threshold", "concluded_at",
	} {
		if db.Migrator().HasColumn(&model.Debate{}, column) {
			if err := db.Migrator().DropColumn(&model.Debate{}, column); err != nil {
				return err
			}
		}
	}
	return nil
}
