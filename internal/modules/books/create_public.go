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
	"gorm.io/gorm/clause"
)

type CreatePublicBookInput struct {
	ImportID      string   `json:"import_id"`
	Title         string   `json:"title"`
	Subtitle      string   `json:"subtitle,omitempty"`
	OriginalTitle string   `json:"original_title,omitempty"`
	Description   string   `json:"description,omitempty"`
	Language      string   `json:"language,omitempty"`
	Authors       []string `json:"authors,omitempty"`
	Edition       struct {
		Title         string `json:"title,omitempty"`
		Publisher     string `json:"publisher,omitempty"`
		Producer      string `json:"producer,omitempty"`
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
	importID, err := uuid.Parse(strings.TrimSpace(input.ImportID))
	if err != nil || importID == uuid.Nil {
		return BookPublicWorkDTO{}, apperr.BadRequest("validation.invalid_request", "import_id is required")
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
	authorNames := make([]string, 0, len(input.Authors))
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
		authorNames = append(authorNames, name)
	}

	var workID uuid.UUID
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var bookImport model.UserBookImport
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", importID, user.ID).First(&bookImport).Error; err != nil {
			return bookImportNotFound(err)
		}
		if bookImport.WorkID != nil {
			workID = *bookImport.WorkID
			return nil
		}
		if bookImport.Status != model.BookImportStatusScanning && bookImport.Status != model.BookImportStatusMetadataReady {
			return apperr.Unprocessable("books.import_not_ready", "Uploaded book is not ready to be added to the public catalog")
		}
		var sourceAsset model.UserBookAsset
		if err := tx.Where("import_id = ? AND user_id = ?", bookImport.ID, user.ID).First(&sourceAsset).Error; err != nil {
			return bookAssetNotFound(err)
		}
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
		producerName := strings.TrimSpace(input.Edition.Producer)
		producerID, err := findOrCreateBookProducer(tx, producerName)
		if err != nil {
			return err
		}
		edition := model.BookEdition{
			WorkID: work.ID, Title: strings.TrimSpace(input.Edition.Title), PublisherID: publisherID, Publisher: publisherName, ProducerID: producerID, Producer: producerName,
			ISBN10: strings.TrimSpace(input.Edition.ISBN10), ISBN13: strings.TrimSpace(input.Edition.ISBN13),
			Language: strings.TrimSpace(input.Edition.Language), PublishedDate: publishedDate, PageCount: input.Edition.PageCount,
			Binding: strings.TrimSpace(input.Edition.Binding), CoverURL: strings.TrimSpace(input.Edition.CoverURL),
			LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment, CreatedBy: &user.ID,
		}
		if err := tx.Create(&edition).Error; err != nil {
			return err
		}
		bookImport.Title = title
		bookImport.Author = strings.Join(authorNames, "、")
		bookImport.WorkID = &work.ID
		bookImport.EditionID = &edition.ID
		if err := tx.Save(&bookImport).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.PublishedBookAsset{}).Where("source_asset_id = ?", sourceAsset.ID).Updates(map[string]any{
			"work_id": work.ID, "edition_id": edition.ID,
		}).Error; err != nil {
			return err
		}

		for index, name := range authorNames {
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
			contribution := model.BookContribution{WorkID: &work.ID, PersonID: person.ID, Role: "author", Position: index + 1}
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
