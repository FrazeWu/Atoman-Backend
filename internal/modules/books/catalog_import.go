package books

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	bookWorkSourceTarget     = "book_work"
	bookEditionSourceTarget  = "book_edition"
	bookPersonSourceTarget   = "book_person"
	openLibraryWorkSource    = "open_library_work"
	openLibraryEditionSource = "open_library_edition"
	openLibraryPersonSource  = "open_library_person"
)

// CatalogImportSummary describes changes made by a metadata import.
type CatalogImportSummary struct {
	Records               int
	NewWorks              int
	ExistingWorks         int
	NewEditions           int
	ExistingEditions      int
	NewPeople             int
	ExistingPeople        int
	NewContributions      int
	ExistingContributions int
}

// CatalogImporter writes provider records into the public catalog without
// promoting them to an active, publicly curated book.
type CatalogImporter struct {
	db *gorm.DB
}

func NewCatalogImporter(db *gorm.DB) *CatalogImporter {
	return &CatalogImporter{db: db}
}

func (i *CatalogImporter) ImportFromProvider(ctx context.Context, provider CatalogProvider, query string, limit int) (CatalogImportSummary, error) {
	if provider == nil {
		return CatalogImportSummary{}, errors.New("book catalog provider is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	records, err := provider.Search(ctx, query, limit)
	if err != nil {
		return CatalogImportSummary{}, err
	}
	return i.Import(ctx, records)
}

func (i *CatalogImporter) Import(ctx context.Context, records []CatalogBook) (CatalogImportSummary, error) {
	var summary CatalogImportSummary
	if i == nil || i.db == nil {
		return summary, errors.New("book catalog importer database is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for index, record := range records {
		var recordSummary CatalogImportSummary
		err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return importCatalogRecord(tx, record, &recordSummary)
		})
		if err != nil {
			return summary, fmt.Errorf("import catalog record %d: %w", index+1, err)
		}
		summary.add(recordSummary)
	}
	return summary, nil
}

func (summary *CatalogImportSummary) add(other CatalogImportSummary) {
	summary.Records += other.Records
	summary.NewWorks += other.NewWorks
	summary.ExistingWorks += other.ExistingWorks
	summary.NewEditions += other.NewEditions
	summary.ExistingEditions += other.ExistingEditions
	summary.NewPeople += other.NewPeople
	summary.ExistingPeople += other.ExistingPeople
	summary.NewContributions += other.NewContributions
	summary.ExistingContributions += other.ExistingContributions
}

func importCatalogRecord(tx *gorm.DB, record CatalogBook, summary *CatalogImportSummary) error {
	title := strings.TrimSpace(record.Title)
	if title == "" {
		return errors.New("book title is required")
	}
	workID := strings.TrimSpace(record.ExternalWorkID)
	workSourceURL := strings.TrimSpace(record.WorkSourceURL)
	if workID == "" && workSourceURL == "" {
		return errors.New("book work source is required")
	}
	if workSourceURL == "" {
		workSourceURL = openLibrarySourceBaseURL + "/works/" + workID
	}
	if err := validateSourceURL(workSourceURL); err != nil {
		return fmt.Errorf("invalid work source: %w", err)
	}

	work, created, err := findOrCreateBookWork(tx, record, title, workSourceURL)
	if err != nil {
		return err
	}
	if created {
		summary.NewWorks++
	} else {
		summary.ExistingWorks++
	}

	if strings.TrimSpace(record.ExternalEditionID) != "" {
		_, editionCreated, err := findOrCreateBookEdition(tx, record, work.ID)
		if err != nil {
			return err
		}
		if editionCreated {
			summary.NewEditions++
		} else {
			summary.ExistingEditions++
		}
	}

	for _, author := range record.Authors {
		person, personCreated, err := findOrCreateBookPerson(tx, author)
		if err != nil {
			return err
		}
		if personCreated {
			summary.NewPeople++
		} else {
			summary.ExistingPeople++
		}
		contribution := model.BookContribution{}
		err = tx.Where("work_id = ? AND person_id = ? AND role = ?", work.ID, person.ID, "author").First(&contribution).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			err = tx.Create(&model.BookContribution{
				WorkID:   &work.ID,
				PersonID: person.ID,
				Role:     "author",
				Position: 1,
			}).Error
			if err == nil {
				summary.NewContributions++
			}
		case err == nil:
			summary.ExistingContributions++
		}
		if err != nil {
			return fmt.Errorf("save author contribution: %w", err)
		}
	}

	summary.Records++
	return nil
}

func findOrCreateBookWork(tx *gorm.DB, record CatalogBook, title, sourceURL string) (model.BookWork, bool, error) {
	var source model.BookSource
	err := tx.Where("target_type = ? AND url = ?", bookWorkSourceTarget, sourceURL).First(&source).Error
	if err == nil {
		var work model.BookWork
		if err := tx.First(&work, "id = ?", source.TargetID).Error; err != nil {
			return work, false, fmt.Errorf("load work from source: %w", err)
		}
		return work, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.BookWork{}, false, fmt.Errorf("find work source: %w", err)
	}

	work := model.BookWork{
		Title:           title,
		Subtitle:        strings.TrimSpace(record.Subtitle),
		Description:     strings.TrimSpace(record.Description),
		Language:        strings.TrimSpace(record.Language),
		LifecycleStatus: model.BookLifecycleStatusDraft,
		EditStatus:      model.BookEditStatusDevelopment,
	}
	if err := tx.Create(&work).Error; err != nil {
		return work, false, fmt.Errorf("create book work: %w", err)
	}
	if err := tx.Create(&model.BookSource{
		TargetType: bookWorkSourceTarget,
		TargetID:   work.ID,
		Kind:       openLibraryWorkSource,
		Title:      "Open Library work " + strings.TrimSpace(record.ExternalWorkID),
		URL:        sourceURL,
	}).Error; err != nil {
		return work, false, fmt.Errorf("save work source: %w", err)
	}
	return work, true, nil
}

func findOrCreateBookEdition(tx *gorm.DB, record CatalogBook, workID uuid.UUID) (model.BookEdition, bool, error) {
	editionID := strings.TrimSpace(record.ExternalEditionID)
	sourceURL := strings.TrimSpace(record.EditionSourceURL)
	if sourceURL == "" {
		sourceURL = openLibrarySourceBaseURL + "/books/" + editionID
	}
	if err := validateSourceURL(sourceURL); err != nil {
		return model.BookEdition{}, false, fmt.Errorf("invalid edition source: %w", err)
	}

	var source model.BookSource
	err := tx.Where("target_type = ? AND url = ?", bookEditionSourceTarget, sourceURL).First(&source).Error
	if err == nil {
		var edition model.BookEdition
		if err := tx.First(&edition, "id = ?", source.TargetID).Error; err != nil {
			return edition, false, fmt.Errorf("load edition from source: %w", err)
		}
		if edition.WorkID != workID {
			return edition, false, errors.New("edition source is linked to another work")
		}
		return edition, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.BookEdition{}, false, fmt.Errorf("find edition source: %w", err)
	}

	var publishedDate *time.Time
	if record.PublishedYear > 0 {
		date := time.Date(record.PublishedYear, time.January, 1, 0, 0, 0, 0, time.UTC)
		publishedDate = &date
	}
	edition := model.BookEdition{
		WorkID:          workID,
		Title:           strings.TrimSpace(record.Title),
		Publisher:       strings.TrimSpace(record.Publisher),
		ISBN10:          strings.TrimSpace(record.ISBN10),
		ISBN13:          strings.TrimSpace(record.ISBN13),
		Language:        strings.TrimSpace(record.Language),
		PublishedDate:   publishedDate,
		PageCount:       record.PageCount,
		CoverURL:        strings.TrimSpace(record.CoverURL),
		CoverSource:     sourceURL,
		LifecycleStatus: model.BookLifecycleStatusDraft,
		EditStatus:      model.BookEditStatusDevelopment,
	}
	if err := tx.Create(&edition).Error; err != nil {
		return edition, false, fmt.Errorf("create book edition: %w", err)
	}
	if err := tx.Create(&model.BookSource{
		TargetType: bookEditionSourceTarget,
		TargetID:   edition.ID,
		Kind:       openLibraryEditionSource,
		Title:      "Open Library edition " + editionID,
		URL:        sourceURL,
	}).Error; err != nil {
		return edition, false, fmt.Errorf("save edition source: %w", err)
	}
	return edition, true, nil
}

func findOrCreateBookPerson(tx *gorm.DB, author CatalogAuthor) (model.BookPerson, bool, error) {
	name := strings.TrimSpace(author.Name)
	if name == "" {
		return model.BookPerson{}, false, errors.New("book author name is required")
	}
	externalID := strings.TrimSpace(author.ExternalID)
	if openLibraryID(externalID, "", "A") == "" {
		return model.BookPerson{}, false, errors.New("book author source is invalid")
	}
	sourceURL := openLibrarySourceBaseURL + "/authors/" + externalID
	if err := validateSourceURL(sourceURL); err != nil {
		return model.BookPerson{}, false, fmt.Errorf("invalid author source: %w", err)
	}

	var source model.BookSource
	err := tx.Where("target_type = ? AND url = ?", bookPersonSourceTarget, sourceURL).First(&source).Error
	if err == nil {
		var person model.BookPerson
		if err := tx.First(&person, "id = ?", source.TargetID).Error; err != nil {
			return person, false, fmt.Errorf("load author from source: %w", err)
		}
		return person, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.BookPerson{}, false, fmt.Errorf("find author source: %w", err)
	}

	person := model.BookPerson{
		Name:            name,
		SortName:        name,
		LifecycleStatus: model.BookLifecycleStatusDraft,
	}
	if err := tx.Create(&person).Error; err != nil {
		return person, false, fmt.Errorf("create author: %w", err)
	}
	if err := tx.Create(&model.BookSource{
		TargetType: bookPersonSourceTarget,
		TargetID:   person.ID,
		Kind:       openLibraryPersonSource,
		Title:      "Open Library author " + externalID,
		URL:        sourceURL,
	}).Error; err != nil {
		return person, false, fmt.Errorf("save author source: %w", err)
	}
	return person, true, nil
}

func validateSourceURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("source URL must be an absolute HTTP or HTTPS URL")
	}
	if len(parsed.String()) > 2048 {
		return errors.New("source URL is too long")
	}
	return nil
}
