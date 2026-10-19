package shortnote

import (
	"errors"
	"fmt"
	"strings"

	"atoman/internal/model"
	"atoman/internal/modules/reference"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxContentRunes = 500
const maxMediaURLs = 9

type Service struct {
	db         *gorm.DB
	references *reference.Service
}

func NewService(db *gorm.DB, references ...*reference.Service) *Service {
	var refSvc *reference.Service
	if len(references) > 0 && references[0] != nil {
		refSvc = references[0]
	} else {
		refSvc = reference.NewService(db)
	}
	return &Service{db: db, references: refSvc}
}

func (s *Service) Create(user authctx.CurrentUser, input noteInput) (NoteDTO, error) {
	if user.ID == uuid.Nil {
		return NoteDTO{}, apperr.Unauthorized("Login required")
	}
	content, mediaURLs, attachType, attachID, err := validateInput(input)
	if err != nil {
		return NoteDTO{}, err
	}
	note := model.ShortNote{
		UserID:              user.ID,
		Content:             content,
		MediaAttachmentType: attachType,
		MediaAttachmentID:   attachID,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&note).Error; err != nil {
			return err
		}
		if err := createMedia(tx, note.ID, mediaURLs); err != nil {
			return err
		}
		_, err := s.syncShortNoteReferences(tx, note)
		return err
	}); err != nil {
		return NoteDTO{}, err
	}
	return s.Get(note.ID, user.ID)
}

func (s *Service) List(page, pageSize int, viewerID uuid.UUID, authorID *uuid.UUID) ([]NoteDTO, int64, error) {
	var notes []model.ShortNote
	var total int64
	query := s.db.Model(&model.ShortNote{})
	if authorID != nil {
		query = query.Where("user_id = ?", *authorID)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Session(&gorm.Session{}).Preload("User").Preload("Media", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC") }).
		Order("created_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&notes).Error; err != nil {
		return nil, 0, err
	}
	items := make([]NoteDTO, 0, len(notes))
	for _, note := range notes {
		item, err := s.dto(note, viewerID)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, nil
}

func (s *Service) Get(id, viewerID uuid.UUID) (NoteDTO, error) {
	var note model.ShortNote
	if err := s.db.Preload("User").Preload("Media", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC") }).First(&note, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return NoteDTO{}, apperr.NotFound("short_note.not_found", "Short note not found")
		}
		return NoteDTO{}, err
	}
	return s.dto(note, viewerID)
}

func (s *Service) Update(user authctx.CurrentUser, id uuid.UUID, input noteInput) (NoteDTO, error) {
	content, mediaURLs, attachType, attachID, err := validateInput(input)
	if err != nil {
		return NoteDTO{}, err
	}
	note, err := s.ownedNote(user, id)
	if err != nil {
		return NoteDTO{}, err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"content":               content,
			"media_attachment_type": attachType,
			"media_attachment_id":   attachID,
		}
		if err := tx.Model(&note).Updates(updates).Error; err != nil {
			return err
		}
		note.Content = content
		note.MediaAttachmentType = attachType
		note.MediaAttachmentID = attachID
		if err := tx.Where("short_note_id = ?", note.ID).Delete(&model.ShortNoteMedia{}).Error; err != nil {
			return err
		}
		if err := createMedia(tx, note.ID, mediaURLs); err != nil {
			return err
		}
		_, err := s.syncShortNoteReferences(tx, note)
		return err
	}); err != nil {
		return NoteDTO{}, err
	}
	return s.Get(id, user.ID)
}

func (s *Service) Delete(user authctx.CurrentUser, id uuid.UUID) error {
	note, err := s.ownedNote(user, id)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.references.RemoveSource(tx, "short_note", note.ID); err != nil {
			return err
		}
		if err := tx.Where("short_note_id = ?", note.ID).Delete(&model.ShortNoteMedia{}).Error; err != nil {
			return err
		}
		if err := tx.Where("short_note_id = ?", note.ID).Delete(&model.ShortNoteVote{}).Error; err != nil {
			return err
		}
		if err := tx.Where("short_note_id = ?", note.ID).Delete(&model.ShortNoteRead{}).Error; err != nil {
			return err
		}
		if err := tx.Where("target_type = ? AND target_id = ?", "short_note", note.ID).Delete(&model.Like{}).Error; err != nil {
			return err
		}
		var targetIDs []uuid.UUID
		if err := tx.Model(&model.DiscussionTarget{}).Where("kind = ? AND resource_id = ?", "short_note", note.ID).Pluck("id", &targetIDs).Error; err != nil {
			return err
		}
		if len(targetIDs) > 0 {
			var commentIDs []uuid.UUID
			if err := tx.Model(&model.CommentEntry{}).Where("target_id IN ?", targetIDs).Pluck("id", &commentIDs).Error; err != nil {
				return err
			}
			if len(commentIDs) > 0 {
				for _, item := range []any{&model.CommentMention{}, &model.CommentAttachment{}, &model.CommentLike{}, &model.CommentReport{}, &model.CommentTimeAnchor{}} {
					if err := tx.Where("comment_id IN ?", commentIDs).Delete(item).Error; err != nil {
						return err
					}
				}
				if err := tx.Where("target_id IN ?", targetIDs).Delete(&model.CommentEntry{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("target_id IN ?", targetIDs).Delete(&model.CommentPublishRecord{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", targetIDs).Delete(&model.DiscussionTarget{}).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&note).Error
	})
}

func (s *Service) ToggleLike(user authctx.CurrentUser, id uuid.UUID, liked bool) error {
	direction := "none"
	if liked {
		direction = "up"
	}
	_, err := s.SetVote(user, id, direction)
	return err
}

func (s *Service) SetVote(user authctx.CurrentUser, id uuid.UUID, direction string) (NoteDTO, error) {
	if user.ID == uuid.Nil {
		return NoteDTO{}, apperr.Unauthorized("Login required")
	}
	if direction != "up" && direction != "down" && direction != "none" {
		return NoteDTO{}, apperr.BadRequest("validation.invalid_request", "direction must be up, down, or none")
	}
	if _, err := s.Get(id, user.ID); err != nil {
		return NoteDTO{}, err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, "short_note", id).Delete(&model.Like{}).Error; err != nil {
			return err
		}
		if direction == "none" {
			return tx.Where("short_note_id = ? AND user_id = ?", id, user.ID).Delete(&model.ShortNoteVote{}).Error
		}
		vote := model.ShortNoteVote{ShortNoteID: id, UserID: user.ID, Direction: direction}
		return tx.Where("short_note_id = ? AND user_id = ?", id, user.ID).Assign(map[string]any{"direction": direction}).FirstOrCreate(&vote).Error
	}); err != nil {
		return NoteDTO{}, err
	}
	return s.Get(id, user.ID)
}

func (s *Service) ownedNote(user authctx.CurrentUser, id uuid.UUID) (model.ShortNote, error) {
	if user.ID == uuid.Nil {
		return model.ShortNote{}, apperr.Unauthorized("Login required")
	}
	var note model.ShortNote
	if err := s.db.First(&note, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ShortNote{}, apperr.NotFound("short_note.not_found", "Short note not found")
		}
		return model.ShortNote{}, err
	}
	if note.UserID != user.ID {
		return model.ShortNote{}, apperr.Forbidden("short_note.forbidden", "You don't have permission to modify this short note")
	}
	return note, nil
}

func (s *Service) dto(note model.ShortNote, viewerID uuid.UUID) (NoteDTO, error) {
	likesCount, dislikesCount, viewerVote, err := s.voteSummary(note.ID, viewerID)
	if err != nil {
		return NoteDTO{}, err
	}
	var target model.DiscussionTarget
	commentsCount := 0
	if err := s.db.Select("comment_count").Where("kind = ? AND resource_id = ?", "short_note", note.ID).Limit(1).Find(&target).Error; err != nil {
		return NoteDTO{}, err
	}
	commentsCount = target.CommentCount
	liked := viewerVote == "up"
	media := make([]MediaDTO, 0, len(note.Media))
	for _, item := range note.Media {
		media = append(media, MediaDTO{ID: item.ID, URL: item.URL, Position: item.Position})
	}
	mediaAttachment, err := s.resolveMediaAttachment(note)
	if err != nil {
		return NoteDTO{}, err
	}
	return NoteDTO{
		ID:              note.ID,
		UserID:          note.UserID,
		User:            note.User,
		Content:         note.Content,
		Media:           media,
		MediaAttachment: mediaAttachment,
		LikesCount:      likesCount,
		DislikesCount:   dislikesCount,
		VoteScore:       likesCount - dislikesCount,
		ViewerVote:      viewerVote,
		CommentsCount:   commentsCount,
		Liked:           liked,
		Edited:          note.UpdatedAt.After(note.CreatedAt),
		CreatedAt:       note.CreatedAt,
		UpdatedAt:       note.UpdatedAt,
	}, nil
}

func (s *Service) voteSummary(noteID, viewerID uuid.UUID) (int64, int64, string, error) {
	var votes []model.ShortNoteVote
	if err := s.db.Where("short_note_id = ?", noteID).Find(&votes).Error; err != nil {
		return 0, 0, "", err
	}
	explicitVotes := make(map[uuid.UUID]string, len(votes))
	var likesCount, dislikesCount int64
	for _, vote := range votes {
		explicitVotes[vote.UserID] = vote.Direction
		if vote.Direction == "up" {
			likesCount++
		} else {
			dislikesCount++
		}
	}
	var legacyLikes []model.Like
	if err := s.db.Where("target_type = ? AND target_id = ?", "short_note", noteID).Find(&legacyLikes).Error; err != nil {
		return 0, 0, "", err
	}
	for _, like := range legacyLikes {
		if _, hasExplicitVote := explicitVotes[like.UserID]; !hasExplicitVote {
			likesCount++
		}
	}
	viewerVote := "none"
	if viewerID != uuid.Nil {
		if direction, ok := explicitVotes[viewerID]; ok {
			viewerVote = direction
		} else {
			for _, like := range legacyLikes {
				if like.UserID == viewerID {
					viewerVote = "up"
					break
				}
			}
		}
	}
	return likesCount, dislikesCount, viewerVote, nil
}

func validateInput(input noteInput) (string, []string, *string, *uuid.UUID, error) {
	content := strings.TrimSpace(input.Content)
	if content == "" || len([]rune(content)) > maxContentRunes {
		return "", nil, nil, nil, apperr.BadRequest("validation.invalid_request", "content must be between 1 and 500 characters")
	}
	if len(input.MediaURLs) > maxMediaURLs {
		return "", nil, nil, nil, apperr.BadRequest("validation.invalid_request", "media_urls must contain at most 9 items")
	}
	var attachType *string
	var attachID *uuid.UUID
	if input.MediaAttachmentType != nil && strings.TrimSpace(*input.MediaAttachmentType) != "" {
		t := strings.TrimSpace(*input.MediaAttachmentType)
		if t != "song" && t != "album" && t != "video" {
			return "", nil, nil, nil, apperr.BadRequest("validation.invalid_request", "media_attachment_type must be song, album, or video")
		}
		if input.MediaAttachmentID == nil || *input.MediaAttachmentID == uuid.Nil {
			return "", nil, nil, nil, apperr.BadRequest("validation.invalid_request", "media_attachment_id is required when media_attachment_type is set")
		}
		attachType = &t
		attachID = input.MediaAttachmentID
	} else if input.MediaAttachmentID != nil && *input.MediaAttachmentID != uuid.Nil {
		return "", nil, nil, nil, apperr.BadRequest("validation.invalid_request", "media_attachment_type is required when media_attachment_id is set")
	}
	return content, input.MediaURLs, attachType, attachID, nil
}

func createMedia(tx *gorm.DB, noteID uuid.UUID, urls []string) error {
	for position, url := range urls {
		if err := tx.Create(&model.ShortNoteMedia{ShortNoteID: noteID, URL: url, Position: position}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) resolveMediaAttachment(note model.ShortNote) (*MediaAttachmentDTO, error) {
	if note.MediaAttachmentType == nil || note.MediaAttachmentID == nil || *note.MediaAttachmentType == "" || *note.MediaAttachmentID == uuid.Nil {
		return nil, nil
	}
	switch *note.MediaAttachmentType {
	case "song":
		var song model.Song
		err := s.db.Model(&model.Song{}).
			Preload("Album").
			Preload("Album.Artists").
			Preload("Artists").
			Where("id = ?", *note.MediaAttachmentID).
			First(&song).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return &MediaAttachmentDTO{Type: "song", ID: *note.MediaAttachmentID}, nil
			}
			return nil, err
		}

		artistName := ""
		if len(song.Artists) > 0 && strings.TrimSpace(song.Artists[0].Name) != "" {
			artistName = strings.TrimSpace(song.Artists[0].Name)
		} else if song.Album != nil && len(song.Album.Artists) > 0 && strings.TrimSpace(song.Album.Artists[0].Name) != "" {
			artistName = strings.TrimSpace(song.Album.Artists[0].Name)
		}

		var albumTitle string
		var albumID *uuid.UUID
		year := 0
		if song.Album != nil {
			albumTitle = strings.TrimSpace(song.Album.Title)
			albumID = song.AlbumID
			year = song.Album.Year
			if year == 0 && song.Album.ReleaseYear != 0 {
				year = song.Album.ReleaseYear
			}
		}
		if year == 0 && !song.ReleaseDate.IsZero() {
			year = song.ReleaseDate.Year()
		}

		coverURL := song.CoverURL
		if coverURL == "" && song.Album != nil {
			coverURL = song.Album.CoverURL
		}

		var subParts []string
		if artistName != "" {
			subParts = append(subParts, artistName)
		}
		if albumTitle != "" {
			subParts = append(subParts, albumTitle)
		}
		subtitle := strings.Join(subParts, " · ")

		return &MediaAttachmentDTO{
			Type:       "song",
			ID:         song.ID,
			Title:      song.Title,
			Subtitle:   subtitle,
			CoverURL:   coverURL,
			AudioURL:   song.AudioURL,
			Duration:   song.DurationSec,
			Artist:     artistName,
			AlbumID:    albumID,
			AlbumTitle: albumTitle,
			Year:       year,
		}, nil
	case "album":
		var album model.Album
		err := s.db.Model(&model.Album{}).
			Preload("Artists").
			Preload("Songs").
			Where("id = ?", *note.MediaAttachmentID).
			First(&album).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return &MediaAttachmentDTO{Type: "album", ID: *note.MediaAttachmentID}, nil
			}
			return nil, err
		}

		artistName := ""
		if len(album.Artists) > 0 && strings.TrimSpace(album.Artists[0].Name) != "" {
			artistName = strings.TrimSpace(album.Artists[0].Name)
		}

		year := album.Year
		if year == 0 && album.ReleaseYear != 0 {
			year = album.ReleaseYear
		}
		if year == 0 && !album.ReleaseDate.IsZero() {
			year = album.ReleaseDate.Year()
		}

		var subParts []string
		if artistName != "" {
			subParts = append(subParts, artistName)
		}
		if year > 0 {
			subParts = append(subParts, fmt.Sprintf("%d", year))
		}
		if len(album.Songs) > 0 {
			subParts = append(subParts, fmt.Sprintf("%d 首曲目", len(album.Songs)))
		}
		subtitle := strings.Join(subParts, " · ")

		return &MediaAttachmentDTO{
			Type:      "album",
			ID:        album.ID,
			Title:     album.Title,
			Subtitle:  subtitle,
			CoverURL:  album.CoverURL,
			Artist:    artistName,
			Year:      year,
			SongCount: len(album.Songs),
		}, nil
	case "video":
		var row struct {
			ID           uuid.UUID `gorm:"column:id"`
			Title        string    `gorm:"column:title"`
			ThumbnailURL string    `gorm:"column:thumbnail_url"`
			DurationSec  int       `gorm:"column:duration_sec"`
		}
		err := s.db.Model(&model.Video{}).Select("id, title, thumbnail_url, duration_sec").Where("id = ?", *note.MediaAttachmentID).First(&row).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return &MediaAttachmentDTO{Type: "video", ID: *note.MediaAttachmentID}, nil
			}
			return nil, err
		}
		return &MediaAttachmentDTO{
			Type:     "video",
			ID:       row.ID,
			Title:    row.Title,
			CoverURL: row.ThumbnailURL,
			Duration: row.DurationSec,
		}, nil
	default:
		return nil, nil
	}
}

func (s *Service) syncShortNoteReferences(tx *gorm.DB, note model.ShortNote) ([]reference.ResolvedReference, error) {
	refs, err := s.references.ReplacePublished(tx, reference.Source{
		Type: "short_note", ID: note.ID, ActorID: note.UserID, Audience: reference.AudiencePublic,
		MentionNotificationType: "content_mention", NotificationSourceType: "content_reference_short_note",
		Meta: map[string]interface{}{"module": "blog", "path": "/posts/notes/" + note.ID.String()},
	}, []reference.Field{{Name: "content", Content: note.Content}})
	if err != nil {
		return nil, err
	}
	if note.MediaAttachmentType != nil && note.MediaAttachmentID != nil && *note.MediaAttachmentType != "" && *note.MediaAttachmentID != uuid.Nil {
		ref := model.ContentReference{
			SourceType:  "short_note",
			SourceID:    note.ID,
			SourceField: "media_attachment",
			TargetType:  *note.MediaAttachmentType,
			TargetID:    *note.MediaAttachmentID,
			StartOffset: 0,
			EndOffset:   0,
		}
		if err := tx.Create(&ref).Error; err != nil {
			return nil, err
		}
	}
	return refs, nil
}
