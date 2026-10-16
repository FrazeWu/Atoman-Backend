package books

import (
	"context"
	"database/sql"
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

type BookListCard struct {
	ID               uuid.UUID       `json:"id"`
	OwnerID          uuid.UUID       `json:"owner_id"`
	Visibility       string          `json:"visibility"`
	PublishedVersion int64           `json:"published_version"`
	Title            string          `json:"title"`
	Description      string          `json:"description"`
	ItemCount        int             `json:"item_count"`
	PublishedAt      *time.Time      `json:"published_at"`
	Changes          BookListChanges `json:"changes"`
}

type BookListItemDTO struct {
	BookListEntry
	CanonicalWorkID *uuid.UUID `json:"canonical_work_id"`
	Title           string     `json:"title"`
	Available       bool       `json:"available"`
}

type BookListDetail struct {
	BookListCard
	Items []BookListItemDTO `json:"items"`
}

type BookListSearchResult struct {
	Items  []BookListCard `json:"items"`
	Total  int64          `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

func bookListCard(list model.BookList) (BookListCard, BookListSnapshot, error) {
	card := BookListCard{ID: list.ID, OwnerID: list.UserID, Visibility: list.Visibility, PublishedVersion: list.PublishedVersion, PublishedAt: list.PublishedAt}
	var snapshot BookListSnapshot
	if err := json.Unmarshal([]byte(list.PublishedJSON), &snapshot); err != nil {
		return card, snapshot, err
	}
	card.Title, card.Description, card.ItemCount = snapshot.Title, snapshot.Description, len(snapshot.Items)
	err := json.Unmarshal([]byte(list.ChangesJSON), &card.Changes)
	return card, snapshot, err
}

func visibleBookList(db *gorm.DB, user authctx.CurrentUser, listID uuid.UUID, lock bool) (model.BookList, error) {
	var list model.BookList
	query := db.Where("id = ? AND published_at IS NOT NULL AND (visibility = 'public' OR user_id = ?)", listID, user.ID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	err := query.First(&list).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return list, apperr.NotFound("books.list_not_found", "Book list not found")
	}
	return list, err
}

func (s *Service) GetBookList(ctx context.Context, user authctx.CurrentUser, listID uuid.UUID) (BookListDetail, error) {
	var result BookListDetail
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		list, err := visibleBookList(tx, user, listID, false)
		if err != nil {
			return err
		}
		card, snapshot, err := bookListCard(list)
		if err != nil {
			return err
		}
		works, err := resolveBookListWorks(tx, snapshot.Items)
		if err != nil {
			return err
		}
		result = BookListDetail{BookListCard: card, Items: []BookListItemDTO{}}
		for _, entry := range snapshot.Items {
			item := BookListItemDTO{BookListEntry: entry}
			if work, exists := works[entry.WorkID]; exists {
				canonicalID := work.CanonicalWorkID
				item.CanonicalWorkID, item.Title, item.Available = &canonicalID, work.Title, true
			}
			result.Items = append(result.Items, item)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}

// Search only the published title/description, never DraftJSON or UpdatedAt.
func (s *Service) SearchBookLists(ctx context.Context, query string, limit, offset int) (BookListSearchResult, error) {
	limit, offset = normalizeCatalogPagination(limit, offset)
	result := BookListSearchResult{Items: []BookListCard{}, Limit: limit, Offset: offset}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		base := tx.Model(&model.BookList{}).Where("visibility = 'public' AND published_at IS NOT NULL")
		if query = strings.TrimSpace(query); query != "" {
			pattern := "%" + escapeBookCatalogQuery(query) + "%"
			base = base.Where(`(published_json->>'title' ILIKE ? ESCAPE '\' OR published_json->>'description' ILIKE ? ESCAPE '\')`, pattern, pattern)
		}
		if err := base.Count(&result.Total).Error; err != nil {
			return err
		}
		var lists []model.BookList
		if err := base.Order("published_at DESC, id ASC").Limit(limit).Offset(offset).Find(&lists).Error; err != nil {
			return err
		}
		for _, list := range lists {
			card, _, err := bookListCard(list)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, card)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}

func (s *Service) ListMyBookLists(ctx context.Context, user authctx.CurrentUser, limit, offset int) ([]BookListDraftDTO, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	items := []BookListDraftDTO{}
	var total int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&model.BookList{}).Where("user_id = ?", user.ID)
		if err := query.Count(&total).Error; err != nil {
			return err
		}
		var lists []model.BookList
		if err := query.Order("updated_at DESC, id ASC").Limit(limit).Offset(offset).Find(&lists).Error; err != nil {
			return err
		}
		for _, list := range lists {
			item, err := bookListDraftDTO(list)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return items, total, err
}

type SaveBookListBookmarkInput struct {
	SeenVersion *int64 `json:"seen_version"`
}

type BookListBookmarkDTO struct {
	List        BookListCard `json:"list"`
	SeenVersion int64        `json:"seen_version"`
	HasUpdates  bool         `json:"has_updates"`
}

type BookListBookmarkResult struct {
	Items  []BookListBookmarkDTO `json:"items"`
	Total  int64                 `json:"total"`
	Limit  int                   `json:"limit"`
	Offset int                   `json:"offset"`
}

func (s *Service) SaveBookListBookmark(ctx context.Context, user authctx.CurrentUser, listID uuid.UUID, input SaveBookListBookmarkInput) (BookListBookmarkDTO, error) {
	if user.ID == uuid.Nil {
		return BookListBookmarkDTO{}, apperr.Unauthorized("Login required")
	}
	var result BookListBookmarkDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize with publication/withdrawal so a bookmark cannot grant access
		// to a list that has already become private. Readers never edit the list.
		list, err := visibleBookList(tx, user, listID, true)
		if err != nil {
			return err
		}
		seen := int64(0)
		if input.SeenVersion != nil {
			seen = *input.SeenVersion
		}
		if seen < 0 || seen > list.PublishedVersion {
			return apperr.BadRequest("books.invalid_list_seen_version", "A seen version must refer to an existing publication")
		}
		bookmark := model.BookListBookmark{UserID: user.ID, ListID: listID, SeenVersion: seen}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "list_id"}},
			DoUpdates: clause.Assignments(map[string]any{"seen_version": gorm.Expr("GREATEST(book_list_bookmarks.seen_version, EXCLUDED.seen_version)")}),
		}).Create(&bookmark).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ? AND list_id = ?", user.ID, listID).First(&bookmark).Error; err != nil {
			return err
		}
		card, _, err := bookListCard(list)
		result = BookListBookmarkDTO{List: card, SeenVersion: bookmark.SeenVersion, HasUpdates: bookmark.SeenVersion < list.PublishedVersion}
		return err
	})
	return result, err
}

func (s *Service) DeleteBookListBookmark(ctx context.Context, user authctx.CurrentUser, listID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	// Also permits removing one's own bookmark after the list was withdrawn.
	return s.db.WithContext(ctx).Where("user_id = ? AND list_id = ?", user.ID, listID).Delete(&model.BookListBookmark{}).Error
}

func (s *Service) ListBookListBookmarks(ctx context.Context, user authctx.CurrentUser, limit, offset int) (BookListBookmarkResult, error) {
	if user.ID == uuid.Nil {
		return BookListBookmarkResult{}, apperr.Unauthorized("Login required")
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	result := BookListBookmarkResult{Items: []BookListBookmarkDTO{}, Limit: limit, Offset: offset}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&model.BookList{}).Joins("JOIN book_list_bookmarks b ON b.list_id = book_lists.id").
			Where("b.user_id = ? AND published_at IS NOT NULL AND (visibility = 'public' OR book_lists.user_id = ?)", user.ID, user.ID)
		if err := query.Count(&result.Total).Error; err != nil {
			return err
		}
		var rows []struct {
			model.BookList
			SeenVersion int64
		}
		if err := query.Select("book_lists.*, b.seen_version").Order("b.created_at DESC, book_lists.id ASC").Limit(limit).Offset(offset).Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			card, _, err := bookListCard(row.BookList)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, BookListBookmarkDTO{List: card, SeenVersion: row.SeenVersion, HasUpdates: row.SeenVersion < row.PublishedVersion})
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
