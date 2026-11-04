package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
	"unicode/utf8"
)

type PublishNoteCollectionInput struct {
	BaseVersion    int64     `json:"base_version"`
	RequestID      uuid.UUID `json:"request_id"`
	Visibility     string    `json:"visibility"`
	ConfirmPublish bool      `json:"confirm_publish"`
}

// Freeze a bounded excerpt and the author's commentary, never a live reference
// to private notes, source file names, reader anchors or revision UUIDs.
func snapshotNoteContents(tx *gorm.DB, owner uuid.UUID, workIDs []uuid.UUID, draft NoteCollectionDraft) (PublishedNotesContent, error) {
	out := PublishedNotesContent{Title: draft.Title, Description: draft.Description, Items: make([]PublishedNoteItem, 0, len(draft.Items))}
	for _, ref := range draft.Items {
		var item PublishedNoteItem
		switch ref.Kind {
		case "chapter_note":
			var note model.BookPrivateNote
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND user_id = ? AND work_id IN ? AND purged_at IS NULL", ref.ID, owner, workIDs).First(&note).Error; err != nil {
				return out, apperr.BadRequest("books.note_collection_item_unavailable", "Selected note is no longer available")
			}
			if utf8.RuneCountInString(note.Body) > 5000 {
				return out, apperr.BadRequest("books.note_comment_too_long", "Shorten commentary to 5000 characters before publishing")
			}
			item = PublishedNoteItem{Kind: "chapter_note", Title: note.Title, Note: note.Body}
			if note.ChapterAnchorJSON != "" && note.ChapterAnchorJSON != "{}" {
				var anchor BookChapterAnchor
				if err := json.Unmarshal([]byte(note.ChapterAnchorJSON), &anchor); err != nil {
					return out, err
				}
				item.Chapter = anchor.ChapterTitle
			}
		case "highlight":
			var mark model.BookReadingMark
			if err := tx.Model(&model.BookReadingMark{}).Clauses(clause.Locking{Strength: "SHARE"}).Joins("JOIN book_asset_revisions rev ON rev.id = book_reading_marks.revision_id").Joins("JOIN book_assets a ON a.id = rev.asset_id").Where("book_reading_marks.id = ? AND book_reading_marks.user_id = ? AND book_reading_marks.kind = 'highlight' AND a.work_id IN ?", ref.ID, owner, workIDs).Select("book_reading_marks.*").First(&mark).Error; err != nil {
				return out, apperr.BadRequest("books.note_collection_item_unavailable", "Selected highlight is no longer available")
			}
			if utf8.RuneCountInString(mark.Quote) > 350 {
				return out, apperr.BadRequest("books.note_quote_too_long", "Selected quote exceeds 350 characters; shorten it before sharing")
			}
			if utf8.RuneCountInString(mark.Note) > 5000 {
				return out, apperr.BadRequest("books.note_comment_too_long", "Shorten commentary to 5000 characters before publishing")
			}
			item = PublishedNoteItem{Kind: "highlight", Title: mark.Label, Quote: mark.Quote, Note: mark.Note, Color: mark.Color}
		default:
			return out, apperr.BadRequest("books.invalid_note_collection", "Invalid item type")
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func (s *Service) PublishNoteCollection(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, input PublishNoteCollectionInput) (NoteCollectionDTO, error) {
	if user.ID == uuid.Nil {
		return NoteCollectionDTO{}, apperr.Unauthorized("Login required")
	}
	if id == uuid.Nil || input.RequestID == uuid.Nil || input.BaseVersion < 1 || !input.ConfirmPublish || (input.Visibility != "link" && input.Visibility != "public") {
		return NoteCollectionDTO{}, apperr.BadRequest("books.invalid_note_publication", "Confirm link/public visibility with a request UUID and current version")
	}
	encodedRequest, _ := json.Marshal(struct {
		ID    uuid.UUID
		Input PublishNoteCollectionInput
	}{id, input})
	digest := sha256.Sum256(encodedRequest)
	requestHash := hex.EncodeToString(digest[:])
	var result NoteCollectionDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		collection, err := ownedNoteCollection(tx, user, id, true)
		if err != nil {
			return err
		}
		var receipt model.BookNoteShareSnapshot
		err = tx.Where("collection_id = ? AND request_id = ?", id, input.RequestID).First(&receipt).Error
		if err == nil {
			if receipt.RequestHash != requestHash {
				return apperr.Conflict("books.note_publish_request_reused", "Request UUID reused for another publication")
			}
			result, err = noteCollectionDTO(collection)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if collection.Version != input.BaseVersion {
			return apperr.Conflict("books.note_collection_conflict", "Collection changed; reload before publishing")
		}
		var draft NoteCollectionDraft
		if err := json.Unmarshal([]byte(collection.DraftJSON), &draft); err != nil {
			return err
		}
		if len(draft.Items) == 0 {
			return apperr.BadRequest("books.note_collection_empty", "Add notes before publishing")
		}
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, collection.WorkID)
		if err != nil {
			return err
		}
		canonical, err := uuid.Parse(work.ID)
		if err != nil {
			return err
		}
		aliases, err := bookNoteWorkAliases(tx, canonical)
		if err != nil {
			return err
		}
		if err := checkNoteCollectionItems(tx, user.ID, aliases, draft.Items); err != nil {
			return err
		}
		content, err := snapshotNoteContents(tx, user.ID, aliases, draft)
		if err != nil {
			return err
		}
		snapshotBytes, err := json.Marshal(content)
		if err != nil {
			return err
		}
		sourceBytes, err := json.Marshal(draft.Items)
		if err != nil {
			return err
		}
		snapshot := model.BookNoteShareSnapshot{CollectionID: id, Version: collection.PublishedVersion + 1, RequestID: input.RequestID, RequestHash: requestHash, ContentJSON: string(snapshotBytes), SourceIndexJSON: string(sourceBytes)}
		if err := tx.Create(&snapshot).Error; err != nil {
			return err
		}
		collection.Version++
		collection.PublishedVersion = snapshot.Version
		collection.ActiveSnapshotID = &snapshot.ID
		collection.Visibility = input.Visibility
		collection.RevokedAt = nil
		if err := tx.Model(&collection).Updates(map[string]any{"version": collection.Version, "published_version": collection.PublishedVersion, "active_snapshot_id": snapshot.ID, "visibility": input.Visibility, "revoked_at": nil}).Error; err != nil {
			return err
		}
		result, err = noteCollectionDTO(collection)
		return err
	})
	return result, err
}
func (s *Service) RevokeNoteCollection(ctx context.Context, user authctx.CurrentUser, id uuid.UUID, baseVersion int64) (NoteCollectionDTO, error) {
	if user.ID == uuid.Nil {
		return NoteCollectionDTO{}, apperr.Unauthorized("Login required")
	}
	if baseVersion < 1 {
		return NoteCollectionDTO{}, apperr.BadRequest("books.invalid_note_collection", "Version required")
	}
	var result NoteCollectionDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		collection, err := ownedNoteCollection(tx, user, id, true)
		if err != nil {
			return err
		}
		if collection.Version == baseVersion+1 && collection.ActiveSnapshotID == nil {
			result, err = noteCollectionDTO(collection)
			return err
		}
		if collection.Version != baseVersion {
			return apperr.Conflict("books.note_collection_conflict", "Collection changed; reload before revoking")
		}
		if collection.ActiveSnapshotID != nil || collection.Visibility != "private" {
			now := time.Now().UTC()
			collection.Version++
			collection.ActiveSnapshotID = nil
			collection.Visibility = "private"
			collection.RevokedAt = &now
			if err := tx.Model(&collection).Updates(map[string]any{"version": collection.Version, "visibility": "private", "active_snapshot_id": nil, "revoked_at": now}).Error; err != nil {
				return err
			}
		}
		result, err = noteCollectionDTO(collection)
		return err
	})
	return result, err
}
func (s *Service) GetSharedNoteCollection(ctx context.Context, shareKey string) (SharedNoteCollectionDTO, error) {
	out := SharedNoteCollectionDTO{}
	if len(shareKey) != 43 || strings.Trim(shareKey, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
		return out, apperr.NotFound("books.note_share_not_found", "Shared notes unavailable")
	}
	var collection model.BookNoteCollection
	err := s.db.WithContext(ctx).Where("share_key = ? AND visibility IN ? AND active_snapshot_id IS NOT NULL", shareKey, []string{"link", "public"}).First(&collection).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return out, apperr.NotFound("books.note_share_not_found", "Shared notes unavailable")
		}
		return out, err
	}
	var snapshot model.BookNoteShareSnapshot
	if err := s.db.WithContext(ctx).Where("id = ? AND collection_id = ?", collection.ActiveSnapshotID, collection.ID).First(&snapshot).Error; err != nil {
		return out, err
	}
	out.Version = snapshot.Version
	out.Visibility = collection.Visibility
	out.PublishedAt = snapshot.CreatedAt
	if err := json.Unmarshal([]byte(snapshot.ContentJSON), &out.PublishedNotesContent); err != nil {
		return out, err
	}
	return out, nil
}

// Public discovery deliberately indexes only actively published 'public'
// snapshots, never private drafts or unlisted link-only shares.
type PublicNoteShareCard struct {
	SharePath   string    `json:"share_path"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	NoteCount   int       `json:"note_count"`
	PublishedAt time.Time `json:"published_at"`
}
type PublicNoteSharesResult struct {
	Items []PublicNoteShareCard `json:"items"`
	Total int64                 `json:"total"`
}

func (s *Service) SearchPublicNoteShares(ctx context.Context, limit, offset int) (PublicNoteSharesResult, error) {
	result := PublicNoteSharesResult{Items: []PublicNoteShareCard{}}
	if limit < 1 || limit > 50 || offset < 0 || offset > 10000 {
		return result, apperr.BadRequest("books.invalid_pagination", "Public notes page out of range")
	}
	q := s.db.WithContext(ctx).Model(&model.BookNoteCollection{}).Where("visibility = 'public' AND active_snapshot_id IS NOT NULL")
	if err := q.Count(&result.Total).Error; err != nil {
		return result, err
	}
	var collections []model.BookNoteCollection
	if err := q.Order("updated_at DESC, id ASC").Limit(limit).Offset(offset).Find(&collections).Error; err != nil {
		return result, err
	}
	for _, collection := range collections {
		var snapshot model.BookNoteShareSnapshot
		if err := s.db.WithContext(ctx).Where("id = ? AND collection_id = ?", collection.ActiveSnapshotID, collection.ID).First(&snapshot).Error; err != nil {
			return result, err
		}
		var content PublishedNotesContent
		if err := json.Unmarshal([]byte(snapshot.ContentJSON), &content); err != nil {
			return result, err
		}
		result.Items = append(result.Items, PublicNoteShareCard{SharePath: "/books/note-shares/" + collection.ShareKey, Title: content.Title, Description: content.Description, NoteCount: len(content.Items), PublishedAt: snapshot.CreatedAt})
	}
	return result, nil
}
