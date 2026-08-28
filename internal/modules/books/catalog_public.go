package books

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BookPublicPersonDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type BookPublicSourceDTO struct {
	Kind  string `json:"kind,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
	Note  string `json:"note,omitempty"`
}

type BookPublicPostDTO struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Summary     string     `json:"summary,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

type BookPublicEditionDTO struct {
	ID            string     `json:"id"`
	WorkID        string     `json:"work_id"`
	Title         string     `json:"title,omitempty"`
	Publisher     string     `json:"publisher,omitempty"`
	ISBN10        string     `json:"isbn10,omitempty"`
	ISBN13        string     `json:"isbn13,omitempty"`
	Language      string     `json:"language,omitempty"`
	PublishedDate *time.Time `json:"published_date,omitempty"`
	PageCount     int        `json:"page_count,omitempty"`
	Binding       string     `json:"binding,omitempty"`
	CoverURL      string     `json:"cover_url,omitempty"`
}

type BookPublicWorkDTO struct {
	ID              string                 `json:"id"`
	Title           string                 `json:"title"`
	Subtitle        string                 `json:"subtitle,omitempty"`
	OriginalTitle   string                 `json:"original_title,omitempty"`
	Description     string                 `json:"description,omitempty"`
	Language        string                 `json:"language,omitempty"`
	LifecycleStatus string                 `json:"lifecycle_status"`
	RatingScore     float64                `json:"rating_score"`
	RatingCount     int64                  `json:"rating_count"`
	RedirectedFrom  string                 `json:"redirected_from,omitempty"`
	Authors         []BookPublicPersonDTO  `json:"authors"`
	Editions        []BookPublicEditionDTO `json:"editions"`
	Sources         []BookPublicSourceDTO  `json:"sources,omitempty"`
	RelatedPosts    []BookPublicPostDTO    `json:"related_posts,omitempty"`
}

type BookPublicEditionDetailDTO struct {
	Edition BookPublicEditionDTO  `json:"edition"`
	Work    BookPublicWorkDTO     `json:"work"`
	Sources []BookPublicSourceDTO `json:"sources,omitempty"`
}

func (s *Service) SearchPublicCatalog(ctx context.Context, query string, limit, offset int) ([]BookPublicWorkDTO, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, errors.New("book catalog database is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query = strings.TrimSpace(query)
	base := s.db.WithContext(ctx).Model(&model.BookWork{}).
		Where("lifecycle_status = ? AND edit_status <> ?", model.BookLifecycleStatusActive, model.BookEditStatusClosed)
	if query != "" {
		pattern := "%" + escapeBookCatalogQuery(query) + "%"
		base = base.Where(`(title ILIKE ? ESCAPE '\' OR original_title ILIKE ? ESCAPE '\' OR subtitle ILIKE ? ESCAPE '\' OR EXISTS (
			SELECT 1 FROM book_contributions bc
			JOIN book_people bp ON bp.id = bc.person_id
			WHERE bc.work_id = book_works.id AND bc.deleted_at IS NULL AND bp.deleted_at IS NULL AND bp.name ILIKE ? ESCAPE '\'
		))`, pattern, pattern, pattern, pattern)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var works []model.BookWork
	if err := base.Order("title ASC").Offset(offset).Limit(limit).Find(&works).Error; err != nil {
		return nil, 0, err
	}
	result, err := s.buildPublicWorkDTOs(ctx, works, false)
	return result, total, err
}

func (s *Service) GetPublicWork(ctx context.Context, workID uuid.UUID) (BookPublicWorkDTO, error) {
	if s == nil || s.db == nil {
		return BookPublicWorkDTO{}, errors.New("book catalog database is required")
	}
	var work model.BookWork
	if err := s.db.WithContext(ctx).Where("id = ? AND lifecycle_status = ? AND edit_status <> ?", workID, model.BookLifecycleStatusActive, model.BookEditStatusClosed).First(&work).Error; err != nil {
		var merged model.BookWork
		if mergedErr := s.db.WithContext(ctx).Where("id = ? AND lifecycle_status = ? AND redirect_to IS NOT NULL", workID, model.BookLifecycleStatusMerged).First(&merged).Error; mergedErr == nil {
			if merged.RedirectTo != nil {
				redirected, redirectErr := s.GetPublicWork(ctx, *merged.RedirectTo)
				if redirectErr == nil {
					redirected.RedirectedFrom = workID.String()
					return redirected, nil
				}
			}
		}
		return BookPublicWorkDTO{}, publicBookNotFound(err)
	}
	works, err := s.buildPublicWorkDTOs(ctx, []model.BookWork{work}, true)
	if err != nil {
		return BookPublicWorkDTO{}, err
	}
	return works[0], nil
}

func (s *Service) GetPublicEdition(ctx context.Context, editionID uuid.UUID) (BookPublicEditionDetailDTO, error) {
	if s == nil || s.db == nil {
		return BookPublicEditionDetailDTO{}, errors.New("book catalog database is required")
	}
	var edition model.BookEdition
	if err := s.db.WithContext(ctx).Where("id = ? AND lifecycle_status = ?", editionID, model.BookLifecycleStatusActive).First(&edition).Error; err != nil {
		return BookPublicEditionDetailDTO{}, publicBookNotFound(err)
	}
	work, err := s.GetPublicWork(ctx, edition.WorkID)
	if err != nil {
		return BookPublicEditionDetailDTO{}, err
	}
	var sources []model.BookSource
	if err := s.db.WithContext(ctx).Where("target_type = ? AND target_id = ?", "edition", edition.ID).Order("created_at ASC").Find(&sources).Error; err != nil {
		return BookPublicEditionDetailDTO{}, err
	}
	return BookPublicEditionDetailDTO{
		Edition: buildBookPublicEditionDTO(edition),
		Work:    work,
		Sources: buildBookPublicSources(sources),
	}, nil
}

func (s *Service) buildPublicWorkDTOs(ctx context.Context, works []model.BookWork, includeSources bool) ([]BookPublicWorkDTO, error) {
	if len(works) == 0 {
		return []BookPublicWorkDTO{}, nil
	}
	workIDs := make([]uuid.UUID, 0, len(works))
	for _, work := range works {
		workIDs = append(workIDs, work.ID)
	}
	var editions []model.BookEdition
	if err := s.db.WithContext(ctx).Where("work_id IN ? AND lifecycle_status = ?", workIDs, model.BookLifecycleStatusActive).Order("published_date ASC NULLS LAST, title ASC").Find(&editions).Error; err != nil {
		return nil, err
	}
	var contributions []model.BookContribution
	if err := s.db.WithContext(ctx).Where("work_id IN ? AND edition_id IS NULL AND role IN ?", workIDs, []string{"author", "translator", "editor"}).Order("position ASC").Find(&contributions).Error; err != nil {
		return nil, err
	}
	type ratingAggregate struct {
		WorkID      uuid.UUID `gorm:"column:work_id"`
		RatingScore float64   `gorm:"column:rating_score"`
		RatingCount int64     `gorm:"column:rating_count"`
	}
	var ratingRows []ratingAggregate
	if err := s.db.WithContext(ctx).Model(&model.BookRating{}).
		Select("work_id, COALESCE(AVG(score), 0) AS rating_score, COUNT(*) AS rating_count").
		Where("work_id IN ?", workIDs).Group("work_id").Find(&ratingRows).Error; err != nil {
		return nil, err
	}
	ratings := make(map[uuid.UUID]ratingAggregate, len(ratingRows))
	for _, row := range ratingRows {
		ratings[row.WorkID] = row
	}
	personIDs := make([]uuid.UUID, 0, len(contributions))
	for _, contribution := range contributions {
		personIDs = append(personIDs, contribution.PersonID)
	}
	people := make(map[uuid.UUID]model.BookPerson)
	if len(personIDs) > 0 {
		var rows []model.BookPerson
		if err := s.db.WithContext(ctx).Where("id IN ? AND lifecycle_status = ?", personIDs, model.BookLifecycleStatusActive).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, person := range rows {
			people[person.ID] = person
		}
	}
	var sources []model.BookSource
	if includeSources {
		if err := s.db.WithContext(ctx).Where("target_type = ? AND target_id IN ?", "work", workIDs).Order("created_at ASC").Find(&sources).Error; err != nil {
			return nil, err
		}
	}
	var relatedPosts []struct {
		WorkID      uuid.UUID  `gorm:"column:work_id"`
		PostID      uuid.UUID  `gorm:"column:post_id"`
		Title       string     `gorm:"column:title"`
		Summary     string     `gorm:"column:summary"`
		PublishedAt *time.Time `gorm:"column:published_at"`
	}
	if s.db.Migrator().HasTable(&model.BookPostLink{}) && s.db.Migrator().HasTable(&model.Post{}) {
		if err := s.db.WithContext(ctx).Table("book_post_links AS links").Select("links.work_id, posts.id AS post_id, posts.title, posts.summary, posts.published_at").Joins("JOIN posts ON posts.id = links.post_id AND posts.status = ? AND posts.visibility = ?", "published", "public").Where("links.work_id IN ? AND links.deleted_at IS NULL", workIDs).Order("posts.published_at DESC NULLS LAST").Scan(&relatedPosts).Error; err != nil {
			return nil, err
		}
	}
	result := make([]BookPublicWorkDTO, 0, len(works))
	for _, work := range works {
		dto := BookPublicWorkDTO{
			ID:              work.ID.String(),
			Title:           work.Title,
			Subtitle:        work.Subtitle,
			OriginalTitle:   work.OriginalTitle,
			Description:     work.Description,
			Language:        work.Language,
			LifecycleStatus: work.LifecycleStatus,
			RatingScore:     work.RatingScore,
			RatingCount:     work.RatingCount,
			Authors:         []BookPublicPersonDTO{},
			Editions:        []BookPublicEditionDTO{},
			RelatedPosts:    []BookPublicPostDTO{},
		}
		if rating, ok := ratings[work.ID]; ok {
			dto.RatingScore = math.Round(rating.RatingScore*10) / 10
			dto.RatingCount = rating.RatingCount
		}
		for _, contribution := range contributions {
			if contribution.WorkID == nil || *contribution.WorkID != work.ID {
				continue
			}
			person, ok := people[contribution.PersonID]
			if ok {
				dto.Authors = append(dto.Authors, BookPublicPersonDTO{ID: person.ID.String(), Name: person.Name, Role: contribution.Role})
			}
		}
		for _, edition := range editions {
			if edition.WorkID == work.ID {
				dto.Editions = append(dto.Editions, buildBookPublicEditionDTO(edition))
			}
		}
		if includeSources {
			for _, source := range sources {
				if source.TargetID == work.ID {
					dto.Sources = append(dto.Sources, buildBookPublicSourceDTO(source))
				}
			}
		}
		for _, post := range relatedPosts {
			if post.WorkID == work.ID {
				dto.RelatedPosts = append(dto.RelatedPosts, BookPublicPostDTO{ID: post.PostID.String(), Title: post.Title, Summary: post.Summary, PublishedAt: post.PublishedAt})
			}
		}
		result = append(result, dto)
	}
	return result, nil
}

func normalizeCatalogPagination(limit, offset int) (int, int) {
	if limit < 1 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func escapeBookCatalogQuery(query string) string {
	query = strings.ReplaceAll(query, `\`, `\\`)
	query = strings.ReplaceAll(query, "%", `\%`)
	return strings.ReplaceAll(query, "_", `\_`)
}

func buildBookPublicEditionDTO(edition model.BookEdition) BookPublicEditionDTO {
	return BookPublicEditionDTO{
		ID: edition.ID.String(), WorkID: edition.WorkID.String(), Title: edition.Title,
		Publisher: edition.Publisher, ISBN10: edition.ISBN10, ISBN13: edition.ISBN13,
		Language: edition.Language, PublishedDate: edition.PublishedDate, PageCount: edition.PageCount,
		Binding: edition.Binding, CoverURL: edition.CoverURL,
	}
}

func buildBookPublicSources(sources []model.BookSource) []BookPublicSourceDTO {
	result := make([]BookPublicSourceDTO, 0, len(sources))
	for _, source := range sources {
		result = append(result, buildBookPublicSourceDTO(source))
	}
	return result
}

func buildBookPublicSourceDTO(source model.BookSource) BookPublicSourceDTO {
	return BookPublicSourceDTO{Kind: source.Kind, Title: source.Title, URL: source.URL, Note: source.Note}
}

func publicBookNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("books.catalog_not_found", "Public book not found")
	}
	return err
}
