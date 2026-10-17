package books

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/blog"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PublishBookPrivateNoteInput struct {
	RequestID     uuid.UUID `json:"request_id"`
	BaseVersion   int64     `json:"base_version"`
	ChannelID     uuid.UUID `json:"channel_id"`
	CollectionID  uuid.UUID `json:"collection_id,omitempty"`
	ConfirmPublic bool      `json:"confirm_public"`
}

type BookNotePublicationDTO struct {
	ContentID   uuid.UUID `json:"content_id"`
	TargetPath  string    `json:"target_path"`
	PublishedAt time.Time `json:"published_at"`
	Duplicate   bool      `json:"duplicate"`
}

func bookNotePublicationDTO(publication model.BookNotePublication, duplicate bool) BookNotePublicationDTO {
	return BookNotePublicationDTO{ContentID: publication.ContentID, TargetPath: "/post/" + publication.ContentID.String(), PublishedAt: publication.CreatedAt.UTC(), Duplicate: duplicate}
}

func (s *Service) PublishBookPrivateNote(ctx context.Context, user authctx.CurrentUser, noteID uuid.UUID, input PublishBookPrivateNoteInput) (BookNotePublicationDTO, error) {
	if user.ID == uuid.Nil {
		return BookNotePublicationDTO{}, apperr.Unauthorized("Login required")
	}
	if noteID == uuid.Nil || input.RequestID == uuid.Nil || input.ChannelID == uuid.Nil || input.BaseVersion < 1 || !input.ConfirmPublic {
		return BookNotePublicationDTO{}, apperr.BadRequest("books.note_publication_confirmation_required", "Confirm public publication and provide request UUID, channel and reviewed note version")
	}
	payload, _ := json.Marshal(struct {
		NoteID uuid.UUID                   `json:"note_id"`
		Input  PublishBookPrivateNoteInput `json:"input"`
	}{NoteID: noteID, Input: input})
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	var result BookNotePublicationDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "book-note-publish:"+user.ID.String()+":"+input.RequestID.String()).Error; err != nil {
			return err
		}
		var publication model.BookNotePublication
		err := tx.Where("user_id = ? AND request_id = ?", user.ID, input.RequestID).First(&publication).Error
		if err == nil {
			if publication.RequestHash != hash {
				return apperr.Conflict("books.note_publication_request_reused", "Request UUID was already used for another publication")
			}
			// A retry must not re-read newer private text or resurrect a withdrawn post.
			result = bookNotePublicationDTO(publication, true)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var note model.BookPrivateNote
		if err := tx.Where("id = ? AND user_id = ?", noteID, user.ID).First(&note).Error; err != nil {
			return privateBookNoteError(err)
		}
		// Follow the same work -> note lock order as private editing and merges.
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, note.WorkID)
		if err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND user_id = ?", noteID, user.ID).First(&note).Error; err != nil {
			return privateBookNoteError(err)
		}
		if input.BaseVersion != note.Version {
			return bookPrivateVersionConflict("books.private_note_conflict", note.Version, false)
		}
		if strings.TrimSpace(note.Body) == "" {
			return apperr.BadRequest("books.empty_note_publication", "A public note needs nonempty content")
		}
		title := strings.TrimSpace(note.Title)
		if title == "" {
			title = work.Title + " 阅读笔记"
		}
		// Reuse channel ownership, moderation, collection, media/reference checks,
		// content versions and the publication outbox. Never copy private tags,
		// source IDs, file selection or reading history into the public content.
		content, err := blog.NewService(tx).CreateBlogContent(user, blog.CreatePostRequest{Title: title, Content: note.Body, ChannelID: input.ChannelID, CollectionID: input.CollectionID, Status: "published", Visibility: "public"})
		if err != nil {
			return err
		}
		publication = model.BookNotePublication{UserID: user.ID, RequestID: input.RequestID, NoteID: note.ID, NoteVersion: note.Version, WorkID: note.WorkID, ContentID: content.ID, RequestHash: hash}
		if content.PublishedAt != nil {
			publication.CreatedAt = content.PublishedAt.UTC().Truncate(time.Microsecond)
		}
		if err := tx.Create(&publication).Error; err != nil {
			return err
		}
		result = bookNotePublicationDTO(publication, false)
		return nil
	})
	return result, err
}
