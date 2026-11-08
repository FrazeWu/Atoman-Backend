package mail

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MailFollowUpDispatchReport struct {
	Backfilled int `json:"backfilled"`
	Delivered  int `json:"delivered"`
	Repaired   int `json:"repaired"`
	Remaining  int `json:"remaining"`
}

// PostgreSQL and SQLite normalize timestamp precision to microseconds; the
// encrypted user timestamp may have nanoseconds. Normalize before indexing.
func followUpDBTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

const followUpBatchSize = 100
const followUpBackfillSize = 200

// RunMailFollowUpDue runs without browser, Stalwart administration, SMTP,
// external network access, or plaintext mail content. The only information in
// a site notification is a generic prompt for the owner to visit mail.
func RunMailFollowUpDue(ctx context.Context, db *gorm.DB, vault *MailOutboxVault, now time.Time) (MailFollowUpDispatchReport, error) {
	var report MailFollowUpDispatchReport
	if db == nil || vault == nil || now.IsZero() {
		return report, errors.New("Follow-up dispatcher dependencies are unavailable")
	}
	if !db.Migrator().HasTable(&model.MailFollowUpSchedule{}) || !db.Migrator().HasTable(&model.MailFollowUpBackfillState{}) || !db.Migrator().HasTable(&model.Notification{}) {
		return report, errors.New("Follow-up migrations are missing")
	}
	now = now.UTC()
	if err := backfillMailFollowUps(ctx, db, vault, &report); err != nil {
		return report, err
	}
	var pending []model.MailFollowUpSchedule
	if err := db.WithContext(ctx).Where("delivered_at IS NULL AND due_at <= ?", now).
		Order("due_at ASC, record_id ASC").Limit(followUpBatchSize).Find(&pending).Error; err != nil {
		return report, err
	}
	for _, candidate := range pending {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		committed := false
		repaired := false
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var record model.MailOrganizerRecord
			// Same lock order as MutateOrganizer: lock the encrypted organizer record
			// first, then read/update its schedule, avoiding schedule/record deadlocks.
			found := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", candidate.RecordID, candidate.UserID).Limit(1).Find(&record)
			if found.Error != nil {
				return found.Error
			}
			var due model.MailFollowUpSchedule
			selected := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("record_id=? AND user_id=?", candidate.RecordID, candidate.UserID).Limit(1).Find(&due)
			if selected.Error != nil {
				return selected.Error
			}
			if selected.RowsAffected == 0 || due.DeliveredAt != nil || due.DueAt.After(now) {
				return nil
			}
			if found.RowsAffected == 0 {
				repaired = true
				return tx.Where("record_id=? AND user_id=?", due.RecordID, due.UserID).Delete(&model.MailFollowUpSchedule{}).Error
			}
			content, err := vault.Open(record.UserID, record.ID, record.Ciphertext)
			if err != nil {
				return errors.New("Cannot decrypt owner reminder")
			}
			var payload organizerPayload
			if json.Unmarshal(content, &payload) != nil {
				return errors.New("Invalid owner reminder")
			}
			if payload.FollowUpAt == nil {
				repaired = true
				return tx.Where("record_id=? AND user_id=?", due.RecordID, due.UserID).Delete(&model.MailFollowUpSchedule{}).Error
			}
			realDue := followUpDBTime(*payload.FollowUpAt)
			if !realDue.Equal(due.DueAt) {
				// Rescheduling while the worker is due must not emit the old reminder.
				repaired = true
				return tx.Model(&model.MailFollowUpSchedule{}).Where("record_id=? AND user_id=?", due.RecordID, due.UserID).
					Updates(map[string]any{"due_at": realDue, "organizer_revision": record.Revision, "delivered_at": nil, "updated_at": now}).Error
			}
			note := model.Notification{RecipientID: record.UserID, Type: "mail_followup", SourceType: "mail_followup", SourceID: record.ID,
				Meta: model.NotificationMeta{"title": "邮件跟进提醒", "body": "你设置的一封邮件已到跟进时间，请前往邮件模块处理。", "source_label": "邮件", "path": "/mail"}}
			if err := tx.Create(&note).Error; err != nil {
				return err
			}
			updated := tx.Model(&model.MailFollowUpSchedule{}).Where("record_id=? AND user_id=? AND delivered_at IS NULL AND due_at=?", due.RecordID, due.UserID, due.DueAt).
				Update("delivered_at", now)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return errors.New("Follow-up delivery state changed")
			}
			committed = true
			return nil
		})
		if err != nil {
			return report, err
		}
		if committed {
			report.Delivered++
		}
		if repaired {
			report.Repaired++
		}
	}
	var remaining int64
	if err := db.WithContext(ctx).Model(&model.MailFollowUpSchedule{}).Where("delivered_at IS NULL AND due_at <= ?", now).Count(&remaining).Error; err != nil {
		return report, err
	}
	report.Remaining = int(remaining)
	return report, nil
}

// On upgrade, migrate existing encrypted organizer reminders into the due
// index in small idempotent batches. The persistent cursor avoids rescanning
// all mail content on every minute-long timer tick.
func backfillMailFollowUps(ctx context.Context, db *gorm.DB, vault *MailOutboxVault, report *MailFollowUpDispatchReport) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		initial := model.MailFollowUpBackfillState{Key: "default", LastRecordID: uuid.Nil, Completed: false, UpdatedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
			return err
		}
		var progress model.MailFollowUpBackfillState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("key=?", "default").Take(&progress).Error; err != nil {
			return err
		}
		if progress.Completed {
			return nil
		}
		var rows []model.MailOrganizerRecord
		if err := tx.Where("id > ?", progress.LastRecordID).Order("id ASC").Limit(followUpBackfillSize).Find(&rows).Error; err != nil {
			return err
		}
		for _, snapshot := range rows {
			var record model.MailOrganizerRecord
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", snapshot.ID).Take(&record).Error; err != nil {
				return err
			}
			raw, err := vault.Open(record.UserID, record.ID, record.Ciphertext)
			if err != nil {
				return errors.New("Cannot decrypt legacy follow-up")
			}
			var payload organizerPayload
			if json.Unmarshal(raw, &payload) != nil {
				return errors.New("Invalid legacy follow-up")
			}
			if payload.FollowUpAt != nil {
				schedule := model.MailFollowUpSchedule{RecordID: record.ID, UserID: record.UserID, DueAt: followUpDBTime(*payload.FollowUpAt), OrganizerRevision: record.Revision, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
				result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "record_id"}}, DoNothing: true}).Create(&schedule)
				if result.Error != nil {
					return result.Error
				}
				report.Backfilled += int(result.RowsAffected)
			}
			progress.LastRecordID = record.ID
		}
		if len(rows) < followUpBackfillSize {
			progress.Completed = true
		}
		progress.UpdatedAt = time.Now().UTC()
		return tx.Save(&progress).Error
	})
}
