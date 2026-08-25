package shortnote

import (
	"errors"
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
	content, mediaURLs, err := validateInput(input)
	if err != nil {
		return NoteDTO{}, err
	}
	note := model.ShortNote{UserID: user.ID, Content: content}
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

func (s *Service) List(page, pageSize int, viewerID uuid.UUID) ([]NoteDTO, int64, error) {
	var notes []model.ShortNote
	var total int64
	if err := s.db.Model(&model.ShortNote{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := s.db.Preload("User").Preload("Media", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC") }).
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
	content, mediaURLs, err := validateInput(input)
	if err != nil {
		return NoteDTO{}, err
	}
	note, err := s.ownedNote(user, id)
	if err != nil {
		return NoteDTO{}, err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&note).Update("content", content).Error; err != nil {
			return err
		}
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
	return NoteDTO{ID: note.ID, UserID: note.UserID, User: note.User, Content: note.Content, Media: media, LikesCount: likesCount, DislikesCount: dislikesCount, VoteScore: likesCount - dislikesCount, ViewerVote: viewerVote, CommentsCount: commentsCount, Liked: liked, Edited: note.UpdatedAt.After(note.CreatedAt), CreatedAt: note.CreatedAt, UpdatedAt: note.UpdatedAt}, nil
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

func validateInput(input noteInput) (string, []string, error) {
	content := strings.TrimSpace(input.Content)
	if content == "" || len([]rune(content)) > maxContentRunes {
		return "", nil, apperr.BadRequest("validation.invalid_request", "content must be between 1 and 500 characters")
	}
	if len(input.MediaURLs) > maxMediaURLs {
		return "", nil, apperr.BadRequest("validation.invalid_request", "media_urls must contain at most 9 items")
	}
	return content, input.MediaURLs, nil
}

func createMedia(tx *gorm.DB, noteID uuid.UUID, urls []string) error {
	for position, url := range urls {
		if err := tx.Create(&model.ShortNoteMedia{ShortNoteID: noteID, URL: url, Position: position}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) syncShortNoteReferences(tx *gorm.DB, note model.ShortNote) ([]reference.ResolvedReference, error) {
	return s.references.ReplacePublished(tx, reference.Source{
		Type: "short_note", ID: note.ID, ActorID: note.UserID, Audience: reference.AudiencePublic,
		MentionNotificationType: "content_mention", NotificationSourceType: "content_reference_short_note",
		Meta: map[string]interface{}{"module": "blog", "path": "/posts/notes/" + note.ID.String()},
	}, []reference.Field{{Name: "content", Content: note.Content}})
}
