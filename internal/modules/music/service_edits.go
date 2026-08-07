package music

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) MergeArtists(user authctx.CurrentUser, sourceArtistID uuid.UUID, targetArtistID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("music.merge_forbidden", "Admin role required")
	}
	if sourceArtistID == uuid.Nil || targetArtistID == uuid.Nil || sourceArtistID == targetArtistID {
		return apperr.BadRequest("validation.invalid_request", "source_artist_id and target_artist_id must be different valid UUIDs")
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var source model.Artist
		if err := tx.Preload("Aliases").First(&source, "id = ?", sourceArtistID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.artist_not_found", "Source artist not found")
			}
			return err
		}

		var target model.Artist
		if err := tx.Preload("Aliases").First(&target, "id = ?", targetArtistID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.artist_not_found", "Target artist not found")
			}
			return err
		}

		if source.EntryStatus == "closed" {
			return apperr.Unprocessable("music.artist_not_open", "Source artist is not available")
		}
		if target.EntryStatus == "closed" {
			return apperr.Unprocessable("music.artist_not_open", "Target artist is not available")
		}

		if err := tx.Exec(`
			INSERT INTO album_artists (album_id, artist_id, role, custom_role, position, created_at, updated_at)
			SELECT aa.album_id, ?, aa.role, aa.custom_role, aa.position, aa.created_at, aa.updated_at
			FROM album_artists aa
			WHERE aa.artist_id = ?
			  AND NOT EXISTS (
				SELECT 1 FROM album_artists existing
				WHERE existing.album_id = aa.album_id
				  AND existing.artist_id = ?
				  AND existing.role = aa.role
				  AND existing.custom_role = aa.custom_role
			  )
		`, targetArtistID, sourceArtistID, targetArtistID).Error; err != nil {
			return err
		}
		if err := tx.Where("artist_id = ?", sourceArtistID).Delete(&model.AlbumArtist{}).Error; err != nil {
			return err
		}

		if err := tx.Exec(`
			INSERT INTO song_artists (song_id, artist_id, role, custom_role, position, created_at, updated_at)
			SELECT sa.song_id, ?, sa.role, sa.custom_role, sa.position, sa.created_at, sa.updated_at
			FROM song_artists sa
			WHERE sa.artist_id = ?
			  AND NOT EXISTS (
				SELECT 1 FROM song_artists existing
				WHERE existing.song_id = sa.song_id AND existing.artist_id = ?
				  AND existing.role = sa.role AND existing.custom_role = sa.custom_role
			  )
		`, targetArtistID, sourceArtistID, targetArtistID).Error; err != nil {
			return err
		}
		if err := tx.Where("artist_id = ?", sourceArtistID).Delete(&model.SongArtist{}).Error; err != nil {
			return err
		}

		var sourceBookmarks []model.ArtistBookmark
		if err := tx.Where("artist_id = ?", sourceArtistID).Find(&sourceBookmarks).Error; err != nil {
			return err
		}
		for _, row := range sourceBookmarks {
			bookmark := model.ArtistBookmark{UserID: row.UserID, ArtistID: targetArtistID}
			if err := tx.Where("user_id = ? AND artist_id = ?", row.UserID, targetArtistID).FirstOrCreate(&bookmark).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("artist_id = ?", sourceArtistID).Delete(&model.ArtistBookmark{}).Error; err != nil {
			return err
		}

		var sourceAliases []model.ArtistAlias
		if err := tx.Where("artist_id = ?", sourceArtistID).Find(&sourceAliases).Error; err != nil {
			return err
		}

		for _, alias := range append([]string{source.Name}, func() []string {
			names := make([]string, 0, len(sourceAliases))
			for _, item := range sourceAliases {
				names = append(names, item.Alias)
			}
			return names
		}()...) {
			alias = strings.TrimSpace(alias)
			if alias == "" || strings.EqualFold(alias, target.Name) {
				continue
			}
			if err := tx.Where("artist_id = ? AND LOWER(alias) = LOWER(?)", targetArtistID, alias).
				FirstOrCreate(&model.ArtistAlias{ArtistID: targetArtistID, Alias: alias, IsMainName: false}).Error; err != nil {
				return err
			}
		}

		if err := tx.Model(&model.Artist{}).Where("id = ?", sourceArtistID).Updates(map[string]any{"entry_status": "closed", "redirect_to": targetArtistID}).Error; err != nil {
			return err
		}

		mergeRecord := model.ArtistMerge{
			SourceArtistID: sourceArtistID,
			TargetArtistID: targetArtistID,
			MergedBy:       user.ID,
			MergedAt:       time.Now(),
		}
		if err := tx.Create(&mergeRecord).Error; err != nil {
			return err
		}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "music.artist.merge", EntityType: "artist", EntityID: &targetArtistID, Reason: "合并重复艺术家", Metadata: map[string]any{"source_artist_id": sourceArtistID}})
	})
}

func (s *Service) SubmitEdit(user authctx.CurrentUser, req SubmitEditRequest) (model.MusicEdit, error) {
	if user.ID == uuid.Nil {
		return model.MusicEdit{}, apperr.Unauthorized("Login required")
	}
	if req.Type == "" || req.EntityType == "" || req.Reason == "" {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "type, entity_type and reason are required")
	}
	if req.Type == "update_artist" || req.Type == "update_album" {
		return model.MusicEdit{}, apperr.BadRequest("music.revision_required", "artist and album updates must use revisions")
	}
	if req.Type == "merge_album" {
		return model.MusicEdit{}, apperr.BadRequest("music.direct_merge_required", "album merges must use the direct merge endpoint")
	}

	payloadJSON, err := marshalObject(req.Payload, map[string]any{})
	if err != nil {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "payload must be an object")
	}
	changesJSON, err := marshalObject(req.Changes, map[string]any{})
	if err != nil {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "changes must be an object")
	}
	sourcesJSON, err := json.Marshal(req.Sources)
	if err != nil {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "sources are invalid")
	}

	edit := model.MusicEdit{
		Type:        req.Type,
		EntityType:  req.EntityType,
		EntityID:    req.EntityID,
		SubmittedBy: user.ID,
		Status:      "open",
		Reason:      req.Reason,
		PayloadJSON: string(payloadJSON),
		ChangesJSON: string(changesJSON),
		SourcesJSON: string(sourcesJSON),
		Votable:     true,
	}
	autoApplyTypes := map[string]struct{}{
		"create_artist": {},
		"create_album":  {},
	}

	if _, shouldAutoApply := autoApplyTypes[req.Type]; !shouldAutoApply {
		if err := s.repo.CreateEdit(&edit); err != nil {
			return model.MusicEdit{}, err
		}
		return edit, nil
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		if err := repo.CreateEdit(&edit); err != nil {
			return err
		}
		if err := applyEdit(tx, &edit); err != nil {
			edit.Status = "failed_prerequisite"
			edit.FailureReason = err.Error()
			return repo.SaveEdit(&edit)
		}
		edit.Status = "applied"
		edit.AutoApplied = true
		edit.Votable = false
		return repo.SaveEdit(&edit)
	})
	if err != nil {
		return model.MusicEdit{}, err
	}
	return edit, nil
}

func (s *Service) Vote(user authctx.CurrentUser, editID uuid.UUID, req VoteRequest) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if req.Vote != "yes" && req.Vote != "no" {
		return apperr.BadRequest("validation.invalid_request", "vote must be yes or no")
	}

	edit, err := s.repo.GetEdit(editID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.edit_not_found", "Edit not found")
		}
		return err
	}
	if edit.Status != "open" {
		return apperr.Unprocessable("music.edit_not_open", "Edit is not open")
	}

	vote := model.MusicEditVote{EditID: editID, UserID: user.ID, Vote: req.Vote, Comment: req.Comment}
	return s.db.Where("edit_id = ? AND user_id = ?", editID, user.ID).Assign(map[string]any{"vote": req.Vote, "comment": req.Comment}).FirstOrCreate(&vote).Error
}

func (s *Service) ApproveEdit(user authctx.CurrentUser, editID uuid.UUID, reason string) (model.MusicEdit, error) {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return model.MusicEdit{}, apperr.Forbidden("music.edit_forbidden", "Moderator role required")
	}

	var out model.MusicEdit
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		claimed, err := repo.ClaimOpenEdit(editID, "applying")
		if err != nil {
			return err
		}
		if !claimed {
			if _, err := repo.GetEdit(editID); errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.edit_not_found", "Edit not found")
			} else if err != nil {
				return err
			}
			return apperr.Unprocessable("music.edit_not_open", "Edit is not open")
		}

		edit, err := repo.GetEdit(editID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.edit_not_found", "Edit not found")
			}
			return err
		}
		if err := applyEdit(tx, &edit); err != nil {
			return err
		}

		edit.Status = "applied"
		if err := repo.SaveEdit(&edit); err != nil {
			return err
		}
		decision := model.MusicEditDecision{EditID: edit.ID, DeciderID: user.ID, Decision: "approve", Reason: reason}
		if err := tx.Create(&decision).Error; err != nil {
			return err
		}
		if err := audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "music.edit.approve", EntityType: "music_edit", EntityID: &edit.ID, Reason: reason}); err != nil {
			return err
		}
		out = edit
		return nil
	})
	if err != nil {
		failed := model.MusicEdit{}
		if getErr := s.db.First(&failed, "id = ?", editID).Error; getErr == nil && failed.Status == "open" {
			failed.Status = "failed_prerequisite"
			failed.FailureReason = err.Error()
			if saveErr := s.repo.SaveEdit(&failed); saveErr == nil {
				out = failed
			}
		}
		return out, err
	}
	return out, nil
}

func (s *Service) RejectEdit(user authctx.CurrentUser, editID uuid.UUID, reason string) (model.MusicEdit, error) {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return model.MusicEdit{}, apperr.Forbidden("music.edit_forbidden", "Moderator role required")
	}

	var out model.MusicEdit
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		claimed, err := repo.ClaimOpenEdit(editID, "rejected")
		if err != nil {
			return err
		}
		if !claimed {
			if _, err := repo.GetEdit(editID); errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.edit_not_found", "Edit not found")
			} else if err != nil {
				return err
			}
			return apperr.Unprocessable("music.edit_not_open", "Edit is not open")
		}

		edit, err := repo.GetEdit(editID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.edit_not_found", "Edit not found")
			}
			return err
		}

		edit.Status = "rejected"
		if err := repo.SaveEdit(&edit); err != nil {
			return err
		}
		if err := tx.Create(&model.MusicEditDecision{EditID: edit.ID, DeciderID: user.ID, Decision: "reject", Reason: reason}).Error; err != nil {
			return err
		}
		if err := audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "music.edit.reject", EntityType: "music_edit", EntityID: &edit.ID, Reason: reason}); err != nil {
			return err
		}
		out = edit
		return nil
	})
	return out, err
}

func (s *Service) CancelEdit(user authctx.CurrentUser, editID uuid.UUID, reason string) (model.MusicEdit, error) {
	_ = reason
	if user.ID == uuid.Nil {
		return model.MusicEdit{}, apperr.Unauthorized("Login required")
	}

	edit, err := s.repo.GetEdit(editID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.MusicEdit{}, apperr.NotFound("music.edit_not_found", "Edit not found")
		}
		return model.MusicEdit{}, err
	}
	if edit.Status != "open" {
		return model.MusicEdit{}, apperr.Unprocessable("music.edit_not_open", "Edit is not open")
	}
	if edit.SubmittedBy != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return model.MusicEdit{}, apperr.Forbidden("music.edit_forbidden", "Only submitter or moderator can cancel")
	}

	edit.Status = "cancelled"
	if err := s.repo.SaveEdit(&edit); err != nil {
		return model.MusicEdit{}, err
	}
	return edit, nil
}

func marshalObject(value map[string]any, fallback map[string]any) ([]byte, error) {
	if value == nil {
		value = fallback
	}
	return json.Marshal(value)
}
