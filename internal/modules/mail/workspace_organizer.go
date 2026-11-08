package mail

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxOrganizerLabels  = 24
	maxOrganizerRecords = 2500
)

type OrganizerLabelInfo struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Color    string    `json:"color"`
	Revision uint64    `json:"revision"`
}
type OrganizerRecordInfo struct {
	MessageID   string      `json:"message_id"`
	SnoozeUntil *time.Time  `json:"snooze_until,omitempty"`
	FollowUpAt  *time.Time  `json:"follow_up_at,omitempty"`
	LabelIDs    []uuid.UUID `json:"label_ids"`
	Revision    uint64      `json:"revision"`
}
type OrganizerState struct {
	Labels  []OrganizerLabelInfo  `json:"labels"`
	Records []OrganizerRecordInfo `json:"records"`
}
type OrganizerLabelInput struct {
	Name     string `json:"name"`
	Color    string `json:"color"`
	Revision uint64 `json:"revision,omitempty"`
}
type OrganizerActionInput struct {
	MessageID string     `json:"message_id"`
	Action    string     `json:"action"`
	DueAt     *time.Time `json:"due_at,omitempty"`
	LabelID   uuid.UUID  `json:"label_id,omitempty"`
	Revision  uint64     `json:"revision,omitempty"`
}
type organizerPayload struct {
	SnoozeUntil *time.Time  `json:"snooze_until,omitempty"`
	FollowUpAt  *time.Time  `json:"follow_up_at,omitempty"`
	LabelIDs    []uuid.UUID `json:"label_ids"`
}

func organizerColor(raw string) bool {
	switch raw {
	case "slate", "red", "orange", "yellow", "green", "blue", "purple", "pink":
		return true
	}
	return false
}
func organizerLabelInput(raw OrganizerLabelInput) (OrganizerLabelInput, error) {
	raw.Name = strings.TrimSpace(raw.Name)
	if !utf8.ValidString(raw.Name) || len([]rune(raw.Name)) < 1 || len([]rune(raw.Name)) > 40 || strings.ContainsAny(raw.Name, "\r\n\t\x00") {
		return raw, apperr.BadRequest("mail.organizer_invalid_label", "Label name must be 1–40 characters")
	}
	if !organizerColor(raw.Color) {
		return raw, apperr.BadRequest("mail.organizer_invalid_color", "Choose an allowed label color")
	}
	return raw, nil
}
func (s *MailWorkspaceService) openOrganizerLabel(row model.MailOrganizerLabel) (OrganizerLabelInfo, error) {
	raw, err := s.vault.Open(row.UserID, row.ID, row.Ciphertext)
	if err != nil {
		return OrganizerLabelInfo{}, workspaceErr(err)
	}
	var v OrganizerLabelInfo
	if json.Unmarshal(raw, &v) != nil || v.Name == "" {
		return v, workspaceErr(errors.New("invalid private label"))
	}
	v.ID, v.Revision = row.ID, row.Revision
	return v, nil
}
func (s *MailWorkspaceService) listOrganizerLabels(ctx context.Context, id uuid.UUID) ([]OrganizerLabelInfo, error) {
	return s.listOrganizerLabelsDB(s.db.WithContext(ctx), id)
}
func (s *MailWorkspaceService) listOrganizerLabelsDB(db *gorm.DB, id uuid.UUID) ([]OrganizerLabelInfo, error) {
	var rows []model.MailOrganizerLabel
	if err := db.Where("user_id=?", id).Order("created_at ASC").Limit(maxOrganizerLabels + 1).Find(&rows).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	if len(rows) > maxOrganizerLabels {
		return nil, apperr.New(503, "mail.organizer_labels_limit", "Too many private labels", nil)
	}
	out := make([]OrganizerLabelInfo, 0, len(rows))
	for _, row := range rows {
		info, err := s.openOrganizerLabel(row)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}
func (s *MailWorkspaceService) openOrganizerRecord(row model.MailOrganizerRecord) (OrganizerRecordInfo, error) {
	raw, err := s.vault.Open(row.UserID, row.ID, row.Ciphertext)
	if err != nil {
		return OrganizerRecordInfo{}, workspaceErr(err)
	}
	var v organizerPayload
	if json.Unmarshal(raw, &v) != nil || len(v.LabelIDs) > maxOrganizerLabels {
		return OrganizerRecordInfo{}, workspaceErr(errors.New("invalid private organizer record"))
	}
	if v.LabelIDs == nil {
		v.LabelIDs = []uuid.UUID{}
	}
	return OrganizerRecordInfo{MessageID: row.EmailID, SnoozeUntil: v.SnoozeUntil, FollowUpAt: v.FollowUpAt, LabelIDs: v.LabelIDs, Revision: row.Revision}, nil
}
func (s *MailWorkspaceService) MailOrganizer(ctx context.Context, user authctx.CurrentUser) (OrganizerState, error) {
	empty := OrganizerState{Labels: []OrganizerLabelInfo{}, Records: []OrganizerRecordInfo{}}
	if _, _, err := s.auth(ctx, user); err != nil {
		return empty, err
	}
	labels, err := s.listOrganizerLabels(ctx, user.ID)
	if err != nil {
		return empty, err
	}
	empty.Labels = labels
	allowed := make(map[uuid.UUID]bool, len(labels))
	for _, label := range labels {
		allowed[label.ID] = true
	}
	var rows []model.MailOrganizerRecord
	if err := s.db.WithContext(ctx).Where("user_id=?", user.ID).Order("updated_at DESC").Limit(maxOrganizerRecords + 1).Find(&rows).Error; err != nil {
		return empty, apperr.Internal(err)
	}
	if len(rows) > maxOrganizerRecords {
		return empty, apperr.New(503, "mail.organizer_records_limit", "Too many reminders", nil)
	}
	for _, row := range rows {
		info, err := s.openOrganizerRecord(row)
		if err != nil {
			return empty, err
		}
		filtered := make([]uuid.UUID, 0, len(info.LabelIDs))
		for _, id := range info.LabelIDs {
			if allowed[id] {
				filtered = append(filtered, id)
			}
		}
		info.LabelIDs = filtered
		empty.Records = append(empty.Records, info)
	}
	return empty, nil
}
func (s *MailWorkspaceService) CreateOrganizerLabel(ctx context.Context, user authctx.CurrentUser, input OrganizerLabelInput) (OrganizerLabelInfo, error) {
	var empty OrganizerLabelInfo
	if _, _, err := s.auth(ctx, user); err != nil {
		return empty, err
	}
	valid, err := organizerLabelInput(input)
	if err != nil {
		return empty, err
	}
	var info OrganizerLabelInfo
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if e := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "mail-labels:"+user.ID.String()).Error; e != nil {
				return e
			}
		}
		rows, e := s.listOrganizerLabelsDB(tx, user.ID)
		if e != nil {
			return e
		}
		if len(rows) >= maxOrganizerLabels {
			return apperr.Conflict("mail.organizer_label_limit", "Maximum 24 labels")
		}
		for _, item := range rows {
			if strings.EqualFold(item.Name, valid.Name) {
				return apperr.Conflict("mail.organizer_duplicate_label", "Label already exists")
			}
		}
		id := uuid.New()
		raw, _ := json.Marshal(valid)
		blob, e := s.vault.Seal(user.ID, id, raw)
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		row := model.MailOrganizerLabel{ID: id, UserID: user.ID, Ciphertext: blob, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if e = tx.Create(&row).Error; e != nil {
			return e
		}
		info = OrganizerLabelInfo{ID: id, Name: valid.Name, Color: valid.Color, Revision: 1}
		return nil
	})
	if err != nil {
		return empty, err
	}
	return info, nil
}
func (s *MailWorkspaceService) UpdateOrganizerLabel(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, input OrganizerLabelInput) (OrganizerLabelInfo, error) {
	var empty OrganizerLabelInfo
	if _, _, err := s.auth(ctx, user); err != nil {
		return empty, err
	}
	valid, err := organizerLabelInput(input)
	if err != nil {
		return empty, err
	}
	if valid.Revision == 0 {
		return empty, apperr.BadRequest("mail.organizer_missing_revision", "Label revision required")
	}
	labels, err := s.listOrganizerLabels(ctx, user.ID)
	if err != nil {
		return empty, err
	}
	for _, label := range labels {
		if label.ID != id && strings.EqualFold(label.Name, valid.Name) {
			return empty, apperr.Conflict("mail.organizer_duplicate_label", "Label already exists")
		}
	}
	raw, _ := json.Marshal(valid)
	blob, err := s.vault.Seal(user.ID, id, raw)
	if err != nil {
		return empty, workspaceErr(err)
	}
	result := s.db.WithContext(ctx).Model(&model.MailOrganizerLabel{}).
		Where("id=? AND user_id=? AND revision=?", id, user.ID, valid.Revision).
		Updates(map[string]any{"ciphertext": blob, "revision": valid.Revision + 1, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return empty, apperr.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return empty, apperr.Conflict("mail.organizer_label_conflict", "Label changed or unavailable")
	}
	return OrganizerLabelInfo{ID: id, Name: valid.Name, Color: valid.Color, Revision: valid.Revision + 1}, nil
}
func (s *MailWorkspaceService) DeleteOrganizerLabel(ctx context.Context, user authctx.CurrentUser, id uuid.UUID) error {
	if _, _, err := s.auth(ctx, user); err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Where("id=? AND user_id=?", id, user.ID).Delete(&model.MailOrganizerLabel{})
	if result.Error != nil {
		return apperr.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return apperr.NotFound("mail.organizer_label_missing", "Label unavailable")
	}
	// Historical encrypted references are filtered by MailOrganizer(); never
	// rewrite other ciphertext while a label is being removed.
	return nil
}
func validOrganizerDue(t *time.Time) bool {
	if t == nil {
		return false
	}
	now := time.Now().UTC()
	return t.After(now.Add(4*time.Minute)) && t.Before(now.Add(90*24*time.Hour))
}
func (s *MailWorkspaceService) MutateOrganizer(ctx context.Context, user authctx.CurrentUser, input OrganizerActionInput) (OrganizerRecordInfo, error) {
	var empty OrganizerRecordInfo
	if _, _, err := s.auth(ctx, user); err != nil {
		return empty, err
	}
	if !validJMAPScopedID(input.MessageID) {
		return empty, apperr.BadRequest("mail.organizer_bad_message", "Invalid message identifier")
	}
	switch input.Action {
	case "snooze", "follow_up":
		if !validOrganizerDue(input.DueAt) {
			return empty, apperr.BadRequest("mail.organizer_bad_date", "Choose a time from five minutes to 90 days ahead")
		}
	case "unsnooze", "clear_follow_up":
	case "label_add", "label_remove":
		if input.LabelID == uuid.Nil {
			return empty, apperr.BadRequest("mail.organizer_bad_label", "Label required")
		}
	default:
		return empty, apperr.BadRequest("mail.organizer_bad_action", "Unknown organizer action")
	}
	cred, accountID, err := s.auth(ctx, user)
	if err != nil {
		return empty, err
	}
	// Never attach metadata to a guessed email ID, even if caller supplies
	// a real opaque ID from another Atoman account.
	if _, _, err = s.states(ctx, cred, accountID, []string{input.MessageID}); err != nil {
		return empty, apperr.NotFound("mail.organizer_message_missing", "Message unavailable")
	}
	if input.Action == "label_add" || input.Action == "label_remove" {
		var count int64
		if e := s.db.WithContext(ctx).Model(&model.MailOrganizerLabel{}).
			Where("id=? AND user_id=?", input.LabelID, user.ID).Count(&count).Error; e != nil {
			return empty, apperr.Internal(e)
		}
		if count != 1 {
			return empty, apperr.NotFound("mail.organizer_label_missing", "Label unavailable")
		}
	}
	var updated OrganizerRecordInfo
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.MailOrganizerRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND email_id=?", user.ID, input.MessageID).Take(&row).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		exists := err == nil
		payload := organizerPayload{LabelIDs: []uuid.UUID{}}
		if exists {
			v, e := s.openOrganizerRecord(row)
			if e != nil {
				return e
			}
			payload.SnoozeUntil, payload.FollowUpAt, payload.LabelIDs = v.SnoozeUntil, v.FollowUpAt, v.LabelIDs
			if input.Revision != row.Revision {
				return apperr.Conflict("mail.organizer_conflict", "Reminder changed in another session")
			}
		} else {
			if input.Revision != 0 {
				return apperr.Conflict("mail.organizer_conflict", "Reminder changed in another session")
			}
			var n int64
			if e := tx.Model(&model.MailOrganizerRecord{}).Where("user_id=?", user.ID).Count(&n).Error; e != nil {
				return e
			}
			if n >= maxOrganizerRecords {
				return apperr.Conflict("mail.organizer_record_limit", "Too many reminders")
			}
			row.ID, row.UserID, row.EmailID, row.Revision = uuid.New(), user.ID, input.MessageID, 0
			row.CreatedAt = time.Now().UTC()
		}
		switch input.Action {
		case "snooze":
			payload.SnoozeUntil = input.DueAt
		case "unsnooze":
			payload.SnoozeUntil = nil
		case "follow_up":
			payload.FollowUpAt = input.DueAt
		case "clear_follow_up":
			payload.FollowUpAt = nil
		case "label_add":
			for _, id := range payload.LabelIDs {
				if id == input.LabelID {
					return nil
				}
			}
			if len(payload.LabelIDs) >= maxOrganizerLabels {
				return apperr.Conflict("mail.organizer_too_many_labels", "Too many labels")
			}
			payload.LabelIDs = append(payload.LabelIDs, input.LabelID)
		case "label_remove":
			next := make([]uuid.UUID, 0, len(payload.LabelIDs))
			for _, id := range payload.LabelIDs {
				if id != input.LabelID {
					next = append(next, id)
				}
			}
			payload.LabelIDs = next
		}
		raw, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		blob, e := s.vault.Seal(user.ID, row.ID, raw)
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		if exists {
			result := tx.Model(&model.MailOrganizerRecord{}).Where("id=? AND user_id=? AND revision=?", row.ID, user.ID, row.Revision).
				Updates(map[string]any{"ciphertext": blob, "revision": row.Revision + 1, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return apperr.Conflict("mail.organizer_conflict", "Reminder changed in another session")
			}
		} else {
			row.Revision, row.Ciphertext, row.UpdatedAt = 1, blob, now
			if e = tx.Create(&row).Error; e != nil {
				return e
			}
		}
		// Index only the selected due timestamp for off-browser delivery.
		// This mutation and its schedule are committed atomically, including
		// reschedules and cancellations, before any worker can see them.
		if input.Action == "follow_up" && payload.FollowUpAt != nil {
			currentRevision := row.Revision
			if exists {
				currentRevision++
			}
			schedule := model.MailFollowUpSchedule{RecordID: row.ID, UserID: user.ID, DueAt: followUpDBTime(*payload.FollowUpAt), OrganizerRevision: currentRevision, CreatedAt: now, UpdatedAt: now}
			e := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "record_id"}}, DoUpdates: clause.Assignments(map[string]any{
				"due_at": schedule.DueAt, "organizer_revision": schedule.OrganizerRevision, "delivered_at": nil, "updated_at": now,
			})}).Create(&schedule).Error
			if e != nil {
				return e
			}
		} else if input.Action == "clear_follow_up" {
			if e := tx.Where("record_id=? AND user_id=?", row.ID, user.ID).Delete(&model.MailFollowUpSchedule{}).Error; e != nil {
				return e
			}
		}
		updated = OrganizerRecordInfo{MessageID: input.MessageID, SnoozeUntil: payload.SnoozeUntil,
			FollowUpAt: payload.FollowUpAt, LabelIDs: payload.LabelIDs, Revision: row.Revision}
		if exists {
			updated.Revision++
		}
		return nil
	})
	if err != nil {
		return empty, err
	}
	if updated.MessageID == "" {
		// A duplicate label-add is idempotent; read the current record.
		var row model.MailOrganizerRecord
		if e := s.db.WithContext(ctx).Where("user_id=? AND email_id=?", user.ID, input.MessageID).Take(&row).Error; e != nil {
			return empty, apperr.Internal(e)
		}
		return s.openOrganizerRecord(row)
	}
	return updated, nil
}
