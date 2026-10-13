package books

import (
	"context"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type CreatePublicBookInput struct {
	Title         string   `json:"title"`
	Subtitle      string   `json:"subtitle,omitempty"`
	OriginalTitle string   `json:"original_title,omitempty"`
	Description   string   `json:"description,omitempty"`
	Language      string   `json:"language,omitempty"`
	Authors       []string `json:"authors,omitempty"`
	Edition       struct {
		Title         string `json:"title,omitempty"`
		Publisher     string `json:"publisher,omitempty"`
		ISBN10        string `json:"isbn10,omitempty"`
		ISBN13        string `json:"isbn13,omitempty"`
		Language      string `json:"language,omitempty"`
		PublishedDate string `json:"published_date,omitempty"`
		PageCount     int    `json:"page_count,omitempty"`
		Binding       string `json:"binding,omitempty"`
		CoverURL      string `json:"cover_url,omitempty"`
	} `json:"edition"`
	SourceURL string `json:"source_url,omitempty"`
}

func (s *Service) CreatePublicBook(ctx context.Context, user authctx.CurrentUser, input CreatePublicBookInput) (BookPublicWorkDTO, error) {
	if user.ID == uuid.Nil {
		return BookPublicWorkDTO{}, apperr.Unauthorized("Login required")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return BookPublicWorkDTO{}, apperr.BadRequest("validation.invalid_request", "book title is required")
	}
	if input.Edition.PageCount < 0 {
		return BookPublicWorkDTO{}, apperr.BadRequest("validation.invalid_request", "page_count cannot be negative")
	}
	var publishedDate *time.Time
	if raw := strings.TrimSpace(input.Edition.PublishedDate); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return BookPublicWorkDTO{}, apperr.BadRequest("validation.invalid_request", "published_date must use YYYY-MM-DD")
		}
		publishedDate = &parsed
	}
	sourceURL := strings.TrimSpace(input.SourceURL)
	if sourceURL != "" {
		if err := validateSourceURL(sourceURL); err != nil {
			return BookPublicWorkDTO{}, apperr.BadRequest("validation.invalid_request", "source_url must be a valid HTTP(S) URL")
		}
	}

	var workID uuid.UUID
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		work := model.BookWork{
			Title: title, Subtitle: strings.TrimSpace(input.Subtitle), OriginalTitle: strings.TrimSpace(input.OriginalTitle),
			Description: strings.TrimSpace(input.Description), Language: strings.TrimSpace(input.Language),
			LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment, CreatedBy: &user.ID,
		}
		if err := tx.Create(&work).Error; err != nil {
			return err
		}
		workID = work.ID

		publisherName := strings.TrimSpace(input.Edition.Publisher)
		publisherID, err := findOrCreateBookPublisher(tx, publisherName)
		if err != nil {
			return err
		}
		edition := model.BookEdition{
			WorkID: work.ID, Title: strings.TrimSpace(input.Edition.Title), PublisherID: publisherID, Publisher: publisherName,
			ISBN10: strings.TrimSpace(input.Edition.ISBN10), ISBN13: strings.TrimSpace(input.Edition.ISBN13),
			Language: strings.TrimSpace(input.Edition.Language), PublishedDate: publishedDate, PageCount: input.Edition.PageCount,
			Binding: strings.TrimSpace(input.Edition.Binding), CoverURL: strings.TrimSpace(input.Edition.CoverURL),
			LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment, CreatedBy: &user.ID,
		}
		if err := tx.Create(&edition).Error; err != nil {
			return err
		}

		seenAuthors := make(map[string]struct{}, len(input.Authors))
		for _, rawName := range input.Authors {
			name := strings.TrimSpace(rawName)
			if name == "" {
				continue
			}
			if _, exists := seenAuthors[name]; exists {
				continue
			}
			seenAuthors[name] = struct{}{}
			var person model.BookPerson
			err := tx.Where("name = ? AND deleted_at IS NULL", name).First(&person).Error
			if err == gorm.ErrRecordNotFound {
				person = model.BookPerson{Name: name, SortName: name, LifecycleStatus: model.BookLifecycleStatusActive, CreatedBy: &user.ID}
				if err := tx.Create(&person).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			contribution := model.BookContribution{WorkID: &work.ID, PersonID: person.ID, Role: "author", Position: len(seenAuthors)}
			if err := tx.Create(&contribution).Error; err != nil {
				return err
			}
		}

		if sourceURL != "" {
			for _, target := range []struct {
				typeName string
				id       uuid.UUID
			}{{bookWorkSourceTarget, work.ID}, {bookEditionSourceTarget, edition.ID}} {
				if err := tx.Create(&model.BookSource{TargetType: target.typeName, TargetID: target.id, Kind: "bibliographic", Title: "创建时提供的资料来源", URL: sourceURL, SubmittedBy: &user.ID}).Error; err != nil {
					return err
				}
			}
		}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "books.work.created", EntityType: "book_work", EntityID: &work.ID})
	})
	if err != nil {
		return BookPublicWorkDTO{}, err
	}
	s.InvalidateCatalog()
	return s.GetPublicWork(ctx, workID)
}
