package shortnote

import (
	"errors"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxContentRunes = 500
const maxMediaURLs = 9

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
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
		return createMedia(tx, note.ID, mediaURLs)
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
		return createMedia(tx, note.ID, mediaURLs)
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
	return s.db.Delete(&note).Error
}

func (s *Service) ToggleLike(user authctx.CurrentUser, id uuid.UUID, liked bool) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if _, err := s.Get(id, user.ID); err != nil {
		return err
	}
	if liked {
		like := model.Like{UserID: user.ID, TargetType: "short_note", TargetID: id}
		return s.db.Where(model.Like{UserID: user.ID, TargetType: "short_note", TargetID: id}).FirstOrCreate(&like).Error
	}
	return s.db.Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, "short_note", id).Delete(&model.Like{}).Error
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
	var likesCount int64
	if err := s.db.Model(&model.Like{}).Where("target_type = ? AND target_id = ?", "short_note", note.ID).Count(&likesCount).Error; err != nil {
		return NoteDTO{}, err
	}
	var target model.DiscussionTarget
	commentsCount := 0
	if err := s.db.Select("comment_count").Where("kind = ? AND resource_id = ?", "short_note", note.ID).First(&target).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return NoteDTO{}, err
	} else if err == nil {
		commentsCount = target.CommentCount
	}
	liked := false
	if viewerID != uuid.Nil {
		var viewerLike model.Like
		if err := s.db.Where("user_id = ? AND target_type = ? AND target_id = ?", viewerID, "short_note", note.ID).First(&viewerLike).Error; err == nil {
			liked = true
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return NoteDTO{}, err
		}
	}
	media := make([]MediaDTO, 0, len(note.Media))
	for _, item := range note.Media {
		media = append(media, MediaDTO{ID: item.ID, URL: item.URL, Position: item.Position})
	}
	return NoteDTO{ID: note.ID, UserID: note.UserID, User: note.User, Content: note.Content, Media: media, LikesCount: likesCount, CommentsCount: commentsCount, Liked: liked, Edited: note.UpdatedAt.After(note.CreatedAt), CreatedAt: note.CreatedAt, UpdatedAt: note.UpdatedAt}, nil
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
