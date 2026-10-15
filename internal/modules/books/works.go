package books

import (
	"context"
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

// Creating metadata never depends on an upload or on file distribution rights.
type CreateBookWorkInput struct {
	Title         string      `json:"title"`
	Subtitle      string      `json:"subtitle"`
	OriginalTitle string      `json:"original_title"`
	Description   string      `json:"description"`
	Language      string      `json:"language"`
	AuthorIDs     []uuid.UUID `json:"author_ids"`
	Authors       []string    `json:"authors"`
}

type BookWorkDetail struct {
	BookPublicWorkDTO
	RevisionID uuid.UUID `json:"revision_id"`
}

type CreateBookPersonInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type CreateBookEditionInput struct {
	Title         string `json:"title"`
	Publisher     string `json:"publisher"`
	Producer      string `json:"producer"`
	ISBN10        string `json:"isbn10"`
	ISBN13        string `json:"isbn13"`
	Language      string `json:"language"`
	PublishedDate string `json:"published_date"`
	PageCount     int    `json:"page_count"`
	Binding       string `json:"binding"`
	CoverURL      string `json:"cover_url"`
}

type bookWorkSnapshot struct {
	Title           string      `json:"title"`
	Subtitle        string      `json:"subtitle"`
	OriginalTitle   string      `json:"original_title"`
	Description     string      `json:"description"`
	Language        string      `json:"language"`
	AuthorIDs       []uuid.UUID `json:"author_ids"`
	LifecycleStatus string      `json:"lifecycle_status"`
	RedirectTo      *uuid.UUID  `json:"redirect_to"`
}

func loadBookWorkSnapshot(tx *gorm.DB, work model.BookWork) (bookWorkSnapshot, error) {
	snapshot := bookWorkSnapshot{Title: work.Title, Subtitle: work.Subtitle, OriginalTitle: work.OriginalTitle, Description: work.Description, Language: work.Language, AuthorIDs: []uuid.UUID{}, LifecycleStatus: work.LifecycleStatus, RedirectTo: work.RedirectTo}
	err := tx.Model(&model.BookContribution{}).Where("work_id = ? AND edition_id IS NULL AND role = 'author'", work.ID).Order("position ASC, id ASC").Pluck("person_id", &snapshot.AuthorIDs).Error
	return snapshot, err
}

// The caller locks the entity row first, serializing all revision appends.
func appendBookWikiRevision(tx *gorm.DB, entityType string, entityID, editorID uuid.UUID, summary, editType string, snapshot any) (model.Revision, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return model.Revision{}, err
	}
	var current model.Revision
	err = tx.Where("content_type = ? AND content_id = ? AND is_current = true", "book_"+entityType, entityID).First(&current).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Revision{}, err
	}
	revision := model.Revision{ContentType: "book_" + entityType, ContentID: entityID, VersionNumber: 1, ContentSnapshot: encoded, EditorID: editorID, EditSummary: summary, EditType: editType, Status: "approved", IsCurrent: true}
	if current.ID != uuid.Nil {
		revision.VersionNumber = current.VersionNumber + 1
		revision.PreviousRevisionID = &current.ID
		if err := tx.Model(&current).Update("is_current", false).Error; err != nil {
			return revision, err
		}
	}
	err = tx.Create(&revision).Error
	return revision, err
}

func resolveBookAuthors(tx *gorm.DB, userID uuid.UUID, ids []uuid.UUID, names []string) ([]uuid.UUID, error) {
	result := make([]uuid.UUID, 0, len(ids)+len(names))
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		var person model.BookPerson
		if err := tx.Where("id = ? AND lifecycle_status = ?", id, model.BookLifecycleStatusActive).First(&person).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, apperr.BadRequest("books.author_not_found", "Selected author not found")
			}
			return nil, err
		}
		seen[id] = true
		result = append(result, id)
	}
	seenNames := map[string]bool{}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" || seenNames[name] {
			continue
		}
		seenNames[name] = true
		person, err := createBookWikiPerson(tx, userID, CreateBookPersonInput{Name: name})
		if err != nil {
			return nil, err
		}
		result = append(result, person.ID)
	}
	if len(result) == 0 {
		return nil, apperr.BadRequest("books.author_required", "At least one author is required")
	}
	return result, nil
}

func createBookWikiPerson(tx *gorm.DB, userID uuid.UUID, input CreateBookPersonInput) (model.BookPerson, error) {
	person := model.BookPerson{Name: strings.TrimSpace(input.Name), Description: strings.TrimSpace(input.Description), LifecycleStatus: model.BookLifecycleStatusActive, CreatedBy: &userID}
	if person.Name == "" {
		return person, apperr.BadRequest("books.author_required", "Author name is required")
	}
	if err := tx.Create(&person).Error; err != nil {
		return person, err
	}
	_, err := appendBookWikiRevision(tx, "person", person.ID, userID, "创建作者", "creation", person)
	return person, err
}

func (s *Service) CreateBookPerson(ctx context.Context, user authctx.CurrentUser, input CreateBookPersonInput) (model.BookPerson, error) {
	if user.ID == uuid.Nil {
		return model.BookPerson{}, apperr.Unauthorized("Login required")
	}
	var person model.BookPerson
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		person, err = createBookWikiPerson(tx, user.ID, input)
		return err
	})
	return person, err
}

func (s *Service) CreateBookWork(ctx context.Context, user authctx.CurrentUser, input CreateBookWorkInput) (BookWorkDetail, error) {
	if user.ID == uuid.Nil {
		return BookWorkDetail{}, apperr.Unauthorized("Login required")
	}
	if strings.TrimSpace(input.Title) == "" {
		return BookWorkDetail{}, apperr.BadRequest("books.title_required", "Book title is required")
	}
	work := model.BookWork{Title: strings.TrimSpace(input.Title), Subtitle: strings.TrimSpace(input.Subtitle), OriginalTitle: strings.TrimSpace(input.OriginalTitle), Description: strings.TrimSpace(input.Description), Language: strings.TrimSpace(input.Language), LifecycleStatus: model.BookLifecycleStatusActive, CreatedBy: &user.ID}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		authors, err := resolveBookAuthors(tx, user.ID, input.AuthorIDs, input.Authors)
		if err != nil {
			return err
		}
		if err := tx.Create(&work).Error; err != nil {
			return err
		}
		for index, id := range authors {
			if err := tx.Create(&model.BookContribution{WorkID: &work.ID, PersonID: id, Role: "author", Position: index + 1}).Error; err != nil {
				return err
			}
		}
		snapshot, err := loadBookWorkSnapshot(tx, work)
		if err != nil {
			return err
		}
		_, err = appendBookWikiRevision(tx, "work", work.ID, user.ID, "创建作品", "creation", snapshot)
		return err
	})
	if err != nil {
		return BookWorkDetail{}, err
	}
	s.InvalidateCatalog()
	return s.GetBookWork(ctx, work.ID)
}

func (s *Service) GetBookWork(ctx context.Context, workID uuid.UUID) (BookWorkDetail, error) {
	var detail BookWorkDetail
	// Keep the metadata and revision token in the same database snapshot.
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		canonicalID := workID
		visited := map[uuid.UUID]bool{}
		for {
			if visited[canonicalID] {
				return apperr.Conflict("books.redirect_cycle", "Invalid work redirect")
			}
			visited[canonicalID] = true
			var work model.BookWork
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", canonicalID).First(&work).Error; err != nil {
				return publicBookNotFound(err)
			}
			if work.LifecycleStatus != model.BookLifecycleStatusMerged || work.RedirectTo == nil {
				break
			}
			canonicalID = *work.RedirectTo
		}
		service := *s
		service.db = tx
		dto, err := service.GetPublicWork(ctx, canonicalID)
		if err != nil {
			return err
		}
		if canonicalID != workID {
			dto.RedirectedFrom = workID.String()
		}
		detail.BookPublicWorkDTO = dto
		var current model.Revision
		if err := tx.Where("content_type = 'book_work' AND content_id = ? AND is_current = true", dto.ID).First(&current).Error; err != nil {
			return publicBookNotFound(err)
		}
		detail.RevisionID = current.ID
		return nil
	})
	return detail, err
}

func (s *Service) CreateBookEdition(ctx context.Context, user authctx.CurrentUser, workID uuid.UUID, input CreateBookEditionInput) (BookPublicEditionDTO, error) {
	if user.ID == uuid.Nil {
		return BookPublicEditionDTO{}, apperr.Unauthorized("Login required")
	}
	if input.PageCount < 0 {
		return BookPublicEditionDTO{}, apperr.BadRequest("validation.invalid_request", "page_count cannot be negative")
	}
	var publishedDate *time.Time
	if raw := strings.TrimSpace(input.PublishedDate); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return BookPublicEditionDTO{}, apperr.BadRequest("validation.invalid_request", "published_date must use YYYY-MM-DD")
		}
		publishedDate = &parsed
	}
	edition := model.BookEdition{WorkID: workID, Title: strings.TrimSpace(input.Title), Publisher: strings.TrimSpace(input.Publisher), Producer: strings.TrimSpace(input.Producer), ISBN10: strings.TrimSpace(input.ISBN10), ISBN13: strings.TrimSpace(input.ISBN13), Language: strings.TrimSpace(input.Language), PublishedDate: publishedDate, PageCount: input.PageCount, Binding: strings.TrimSpace(input.Binding), CoverURL: strings.TrimSpace(input.CoverURL), LifecycleStatus: model.BookLifecycleStatusActive, CreatedBy: &user.ID}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var work model.BookWork
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND lifecycle_status = ? AND edit_status <> ?", workID, model.BookLifecycleStatusActive, model.BookEditStatusClosed).First(&work).Error; err != nil {
			return publicBookNotFound(err)
		}
		if _, err := currentBookWikiRevision(tx, "work", workID); err != nil {
			return publicBookNotFound(err)
		}
		var err error
		edition.PublisherID, err = findOrCreateBookPublisher(tx, edition.Publisher)
		if err != nil {
			return err
		}
		edition.ProducerID, err = findOrCreateBookProducer(tx, edition.Producer)
		if err != nil {
			return err
		}
		if err := tx.Create(&edition).Error; err != nil {
			return err
		}
		_, err = appendBookWikiRevision(tx, "edition", edition.ID, user.ID, "添加出版版本", "creation", edition)
		return err
	})
	if err != nil {
		return BookPublicEditionDTO{}, err
	}
	s.InvalidateCatalog()
	return buildBookPublicEditionDTO(edition, nil, nil), nil
}
