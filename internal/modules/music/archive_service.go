package music

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
	"gorm.io/gorm/clause"
)

type musicArchivedSongSnapshot struct {
	ID     uuid.UUID `json:"id"`
	Status string    `json:"status"`
}

type musicArchiveSnapshot struct {
	AlbumStatus      string                      `json:"album_status"`
	AlbumEntryStatus string                      `json:"album_entry_status"`
	SongIDs          []musicArchivedSongSnapshot `json:"songs"`
	SongStatus       string                      `json:"song_status"`
}

type MusicArchivedEntry struct {
	ID         uuid.UUID `json:"id"`
	EntityType string    `json:"entity_type"`
	Title      string    `json:"title"`
	ArchivedAt time.Time `json:"archived_at"`
	Reason     string    `json:"reason"`
}

func requireMusicArchiveAdmin(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if user.Role != authctx.RoleAdmin && user.Role != authctx.RoleOwner {
		return apperr.Forbidden("music.archive_forbidden", "Administrator access is required")
	}
	return nil
}

func validateMusicArchiveInput(entityType, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return apperr.BadRequest("validation.invalid_request", "archive reason is required")
	}
	if entityType != "album" && entityType != "song" {
		return apperr.BadRequest("validation.invalid_request", "invalid archive entity type")
	}
	return nil
}

// ArchiveMusicEntry hides catalog entries without deleting audio or edit history.
// The audit snapshot records the precise songs affected by an album archive,
// so restoring it never resurrects songs archived separately beforehand.
func (s *Service) ArchiveMusicEntry(user authctx.CurrentUser, entityType string, id uuid.UUID, reason string) error {
	if err := requireMusicArchiveAdmin(user); err != nil {
		return err
	}
	if err := validateMusicArchiveInput(entityType, reason); err != nil {
		return err
	}
	snapshot := musicArchiveSnapshot{SongIDs: []musicArchivedSongSnapshot{}}
	return s.db.Transaction(func(tx *gorm.DB) error {
		switch entityType {
		case "album":
			var album model.Album
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&album, "id = ?", id).Error; err != nil {
				return err
			}
			if album.LifecycleStatus != model.MusicLifecycleActive {
				return apperr.Conflict("music.archive_invalid_state", "Only active entries can be archived")
			}
			snapshot.AlbumStatus, snapshot.AlbumEntryStatus = album.Status, album.EntryStatus
			var songs []model.Song
			if err := tx.Where("album_id = ? AND lifecycle_status = ?", id, model.MusicLifecycleActive).
				Clauses(clause.Locking{Strength: "UPDATE"}).Find(&songs).Error; err != nil {
				return err
			}
			for _, song := range songs {
				snapshot.SongIDs = append(snapshot.SongIDs, musicArchivedSongSnapshot{ID: song.ID, Status: song.Status})
			}
			if err := tx.Model(&album).Updates(map[string]any{"lifecycle_status": model.MusicLifecycleRetired, "entry_status": "closed", "status": "closed"}).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.Song{}).Where("album_id = ? AND lifecycle_status = ?", id, model.MusicLifecycleActive).
				Updates(map[string]any{"lifecycle_status": model.MusicLifecycleRetired, "status": "closed"}).Error; err != nil {
				return err
			}
		case "song":
			var song model.Song
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&song, "id = ?", id).Error; err != nil {
				return err
			}
			if song.LifecycleStatus != model.MusicLifecycleActive {
				return apperr.Conflict("music.archive_invalid_state", "Only active entries can be archived")
			}
			snapshot.SongStatus = song.Status
			if err := tx.Model(&song).Updates(map[string]any{"lifecycle_status": model.MusicLifecycleRetired, "status": "closed"}).Error; err != nil {
				return err
			}
		}
		metadata, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		return tx.Create(&model.AuditLog{ActorID: &user.ID, Action: "music.archive", EntityType: entityType, EntityID: &id, Reason: strings.TrimSpace(reason), Metadata: string(metadata)}).Error
	})
}

// ListArchivedMusicEntries is an admin-only view, deliberately separate from
// public discovery/search. It never returns media object keys or full lyrics.
func (s *Service) ListArchivedMusicEntries(user authctx.CurrentUser, page, pageSize int) ([]MusicArchivedEntry, int64, error) {
	if err := requireMusicArchiveAdmin(user); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	if err := s.db.Raw(`SELECT (SELECT COUNT(*) FROM "Albums" WHERE lifecycle_status = ? AND deleted_at IS NULL) +
                               (SELECT COUNT(*) FROM "Songs" WHERE lifecycle_status = ? AND deleted_at IS NULL)`,
		model.MusicLifecycleRetired, model.MusicLifecycleRetired).Scan(&total).Error; err != nil {
		return nil, 0, err
	}
	entries := []MusicArchivedEntry{}
	if err := s.db.Raw(`SELECT id, 'album' AS entity_type, title, updated_at AS archived_at FROM "Albums" WHERE lifecycle_status = ? AND deleted_at IS NULL
        UNION ALL SELECT id, 'song' AS entity_type, title, updated_at AS archived_at FROM "Songs" WHERE lifecycle_status = ? AND deleted_at IS NULL
        ORDER BY archived_at DESC, id DESC LIMIT ? OFFSET ?`,
		model.MusicLifecycleRetired, model.MusicLifecycleRetired, pageSize, (page-1)*pageSize).Scan(&entries).Error; err != nil {
		return nil, 0, err
	}
	for i := range entries {
		var log model.AuditLog
		err := s.db.Where("action = ? AND entity_type = ? AND entity_id = ?", "music.archive", entries[i].EntityType, entries[i].ID).
			Order("created_at DESC").First(&log).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, err
		}
		if err == nil {
			entries[i].Reason = log.Reason
			entries[i].ArchivedAt = log.CreatedAt
		}
	}
	return entries, total, nil
}

// RestoreArchivedMusicEntry reverses only the metadata lifecycle transition;
// no audio or history is copied, overwritten or deleted.
func (s *Service) RestoreArchivedMusicEntry(user authctx.CurrentUser, entityType string, id uuid.UUID, reason string) error {
	if err := requireMusicArchiveAdmin(user); err != nil {
		return err
	}
	if err := validateMusicArchiveInput(entityType, reason); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var log model.AuditLog
		if err := tx.Where("action = ? AND entity_type = ? AND entity_id = ?", "music.archive", entityType, id).
			Order("created_at DESC").First(&log).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.Conflict("music.restore_missing_archive_history", "Archive history is missing; automatic restore is unavailable")
			}
			return err
		}
		var snapshot musicArchiveSnapshot
		if log.Metadata == "" || json.Unmarshal([]byte(log.Metadata), &snapshot) != nil || snapshot.SongIDs == nil {
			return apperr.Conflict("music.restore_missing_archive_history", "Legacy archive lacks a safe restoration snapshot")
		}
		switch entityType {
		case "album":
			var album model.Album
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&album, "id = ?", id).Error; err != nil {
				return err
			}
			if album.LifecycleStatus != model.MusicLifecycleRetired {
				return apperr.Conflict("music.restore_invalid_state", "Album is not archived")
			}
			if err := tx.Model(&album).Updates(map[string]any{"lifecycle_status": model.MusicLifecycleActive, "entry_status": snapshot.AlbumEntryStatus, "status": snapshot.AlbumStatus}).Error; err != nil {
				return err
			}
			for _, song := range snapshot.SongIDs {
				if err := tx.Model(&model.Song{}).Where("id = ? AND album_id = ? AND lifecycle_status = ?", song.ID, id, model.MusicLifecycleRetired).
					Updates(map[string]any{"lifecycle_status": model.MusicLifecycleActive, "status": song.Status}).Error; err != nil {
					return err
				}
			}
		case "song":
			var song model.Song
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&song, "id = ?", id).Error; err != nil {
				return err
			}
			if song.LifecycleStatus != model.MusicLifecycleRetired {
				return apperr.Conflict("music.restore_invalid_state", "Song is not archived")
			}
			if song.AlbumID != nil {
				var album model.Album
				if err := tx.First(&album, "id = ?", song.AlbumID).Error; err != nil {
					return err
				}
				if album.LifecycleStatus != model.MusicLifecycleActive {
					return apperr.Conflict("music.restore_album_archived", "Restore the parent album first")
				}
			}
			if err := tx.Model(&song).Updates(map[string]any{"lifecycle_status": model.MusicLifecycleActive, "status": snapshot.SongStatus}).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.AuditLog{ActorID: &user.ID, Action: "music.restore", EntityType: entityType, EntityID: &id, Reason: strings.TrimSpace(reason)}).Error
	})
}
