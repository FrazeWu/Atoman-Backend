package books

import (
	"context"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestGetBookWorkFacetsAndFiltering(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Revision{},
		&model.BookWork{},
		&model.BookEdition{},
		&model.BookPerson{},
		&model.BookContribution{},
		&model.BookRating{},
		&model.BookSource{},
	)

	service := NewService(db)
	u := model.User{UUID: uuid.New(), Username: "author", Email: "author@test.local", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&u).Error)
	user := authctx.CurrentUser{ID: u.UUID}

	// Create works in different languages, publication years, and ratings
	w1, err := service.CreateBookWork(context.Background(), user, CreateBookWorkInput{
		Title:    "中文古典小说",
		Language: "zh",
		Authors:  []string{"曹雪芹"},
	})
	require.NoError(t, err)

	w2, err := service.CreateBookWork(context.Background(), user, CreateBookWorkInput{
		Title:    "English Classic Novel",
		Language: "en",
		Authors:  []string{"Jane Austen"},
	})
	require.NoError(t, err)

	// Add editions with published dates
	d2021 := time.Date(2021, 5, 1, 0, 0, 0, 0, time.UTC)
	d1995 := time.Date(1995, 3, 1, 0, 0, 0, 0, time.UTC)

	w1ID := uuid.MustParse(w1.ID)
	w2ID := uuid.MustParse(w2.ID)

	require.NoError(t, db.Create(&model.BookEdition{
		WorkID:          w1ID,
		Title:           "中文古典小说 2021版",
		Language:        "zh",
		PublishedDate:   &d2021,
		LifecycleStatus: model.BookLifecycleStatusActive,
		EditStatus:      model.BookEditStatusDevelopment,
	}).Error)

	require.NoError(t, db.Create(&model.BookEdition{
		WorkID:          w2ID,
		Title:           "English Classic 1995 Edition",
		Language:        "en",
		PublishedDate:   &d1995,
		LifecycleStatus: model.BookLifecycleStatusActive,
		EditStatus:      model.BookEditStatusDevelopment,
	}).Error)

	// Test GetBookWorkFacets
	facets, err := service.GetBookWorkFacets(context.Background(), "")
	require.NoError(t, err)
	require.NotEmpty(t, facets.Languages)
	require.NotEmpty(t, facets.Decades)

	// Verify language facet counts
	foundZh, foundEn := false, false
	for _, l := range facets.Languages {
		if l.Key == "zh" {
			foundZh = true
			require.Equal(t, "中文", l.Label)
			require.EqualValues(t, 1, l.Count)
		}
		if l.Key == "en" {
			foundEn = true
			require.Equal(t, "英语", l.Label)
			require.EqualValues(t, 1, l.Count)
		}
	}
	require.True(t, foundZh)
	require.True(t, foundEn)

	// Verify decade facet counts (2020s and classic)
	found2020s, foundClassic := false, false
	for _, d := range facets.Decades {
		if d.Key == "2020s" {
			found2020s = true
			require.EqualValues(t, 1, d.Count)
		}
		if d.Key == "classic" {
			foundClassic = true
			require.EqualValues(t, 1, d.Count)
		}
	}
	require.True(t, found2020s)
	require.True(t, foundClassic)

	// Test SearchBookWorksFiltered by language
	zhWorks, totalZh, err := service.SearchBookWorksFiltered(context.Background(), "", CatalogFilterOptions{
		Languages: []string{"zh"},
	}, 10, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, totalZh)
	require.Len(t, zhWorks, 1)
	require.Equal(t, "中文古典小说", zhWorks[0].Title)

	// Test SearchBookWorksFiltered by publication era (year_from >= 2020)
	recentWorks, totalRecent, err := service.SearchBookWorksFiltered(context.Background(), "", CatalogFilterOptions{
		YearFrom: 2020,
	}, 10, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, totalRecent)
	require.Len(t, recentWorks, 1)
	require.Equal(t, "中文古典小说", recentWorks[0].Title)

	// Test SearchBookWorksFiltered by older era (year_to <= 2000)
	classicWorks, totalClassic, err := service.SearchBookWorksFiltered(context.Background(), "", CatalogFilterOptions{
		YearTo: 2000,
	}, 10, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, totalClassic)
	require.Len(t, classicWorks, 1)
	require.Equal(t, "English Classic Novel", classicWorks[0].Title)
}
