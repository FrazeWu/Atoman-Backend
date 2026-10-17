package books

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCatalogCountCacheReusesQueriesAndInvalidatesAfterWrite(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookRating{})
	work := model.BookWork{Title: "可见书籍", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&work).Error)
	require.NoError(t, db.Create(&model.BookEdition{WorkID: work.ID, Title: work.Title, ISBN13: "9787111000001", CoverURL: "https://example.test/cover.jpg", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}).Error)
	counts := 0
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:count_catalog_queries", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "count(*)") && tx.Statement.Table == "book_works" {
			counts++
		}
	}))
	service := NewService(db)
	for _, offset := range []int{0, 24} {
		_, total, err := service.SearchPublicCatalog(context.Background(), "", 24, offset)
		require.NoError(t, err)
		require.Equal(t, int64(1), total)
	}
	require.Equal(t, 1, counts, "翻页不应重复统计全部作品")
	for range 2 {
		_, total, err := service.SearchPublicCatalog(context.Background(), "可见", 24, 0)
		require.NoError(t, err)
		require.Equal(t, int64(1), total)
	}
	require.Equal(t, 2, counts, "相同搜索复用精确总数")
	service.InvalidateCatalog()
	_, _, err := service.SearchPublicCatalog(context.Background(), "", 24, 0)
	require.NoError(t, err)
	require.Equal(t, 3, counts, "写入失效后应重新统计")
}

func TestPublicCatalogSearchExcludesDraftsAndPrivateFields(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookEdit{}, &model.BookRating{})
	work := model.BookWork{Base: model.Base{ID: uuid.New()}, Title: "Visible Book", Description: "Public description", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	draft := model.BookWork{Base: model.Base{ID: uuid.New()}, Title: "Draft Book", LifecycleStatus: model.BookLifecycleStatusDraft, EditStatus: model.BookEditStatusDevelopment}
	person := model.BookPerson{Base: model.Base{ID: uuid.New()}, Name: "Visible Author", LifecycleStatus: model.BookLifecycleStatusActive}
	edition := model.BookEdition{Base: model.Base{ID: uuid.New()}, WorkID: work.ID, Title: "Visible Edition", Publisher: "Publisher", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&work).Error)
	require.NoError(t, db.Create(&draft).Error)
	require.NoError(t, db.Create(&person).Error)
	require.NoError(t, db.Create(&edition).Error)
	require.NoError(t, db.Create(&model.BookContribution{Base: model.Base{ID: uuid.New()}, WorkID: &work.ID, PersonID: person.ID, Role: "author", Position: 1}).Error)
	require.NoError(t, db.Create(&model.BookSource{Base: model.Base{ID: uuid.New()}, TargetType: "work", TargetID: work.ID, Kind: "bibliographic", URL: "https://example.test/book"}).Error)
	owner := uuid.New()
	for _, status := range []string{model.BookEditStatusPending, model.BookEditStatusRejected, model.BookEditStatusApproved} {
		edit := model.BookEdit{SubmittedBy: owner, Type: model.BookEditTypeUpdate, EntityType: "work", EntityID: &work.ID, Status: status, PayloadJSON: "{}", ChangesJSON: "{}"}
		require.NoError(t, db.Create(&edit).Error)
		require.NoError(t, db.Create(&model.BookSource{TargetType: "work", TargetID: work.ID, BookEditID: &edit.ID, URL: "https://example.test/" + status}).Error)
	}

	items, total, err := NewService(db).SearchPublicCatalog(context.Background(), "Visible Author", 20, 0)

	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	require.Equal(t, work.ID.String(), items[0].ID)
	require.Equal(t, "Visible Author", items[0].Authors[0].Name)
	require.Len(t, items[0].Editions, 1)
	publicWork, err := NewService(db).GetPublicWork(context.Background(), work.ID)
	require.NoError(t, err)
	require.Len(t, publicWork.Sources, 2)
	require.NotContains(t, []string{publicWork.Sources[0].URL, publicWork.Sources[1].URL}, "https://example.test/pending")
	require.NotContains(t, []string{publicWork.Sources[0].URL, publicWork.Sources[1].URL}, "https://example.test/rejected")
	encoded, marshalErr := json.Marshal(items[0])
	require.NoError(t, marshalErr)
	require.NotContains(t, string(encoded), "object_key")
	require.NotContains(t, string(encoded), "private")

	_, err = NewService(db).GetPublicWork(context.Background(), draft.ID)
	require.Error(t, err)
}

func TestPublicEditionUsesISBNCoverWhenImportedCoverIsMissing(t *testing.T) {
	edition := model.BookEdition{Base: model.Base{ID: uuid.New()}, WorkID: uuid.New(), ISBN13: "978-7-111-00000-1"}
	dto := buildBookPublicEditionDTO(edition, nil, nil)
	require.Empty(t, dto.CoverURL, "新版书目不隐式接入外部封面服务")
}

func TestDiscoveryShowsAllActiveBooksRegardlessOfCoverOrLanguage(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookRating{})
	visible := model.BookWork{Title: "中文书", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	noCover := model.BookWork{Title: "另一本书", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	isbnOnly := model.BookWork{Title: "仅有 ISBN 的书", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	latin := model.BookWork{Title: "English Book", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	for _, work := range []*model.BookWork{&visible, &noCover, &isbnOnly, &latin} {
		require.NoError(t, db.Create(work).Error)
	}
	require.NoError(t, db.Create(&model.BookEdition{WorkID: visible.ID, Title: visible.Title, ISBN13: "9787111000001", CoverURL: "https://example.test/cover.jpg", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}).Error)
	require.NoError(t, db.Create(&model.BookEdition{WorkID: noCover.ID, Title: noCover.Title, LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}).Error)
	require.NoError(t, db.Create(&model.BookEdition{WorkID: isbnOnly.ID, Title: isbnOnly.Title, ISBN13: "9787111000002", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}).Error)
	require.NoError(t, db.Create(&model.BookEdition{WorkID: latin.ID, Title: latin.Title, CoverURL: "https://example.test/cover.jpg", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}).Error)

	service := NewService(db)
	items, total, err := service.SearchPublicCatalog(context.Background(), "", 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(4), total)
	require.Len(t, items, 4)
	itemIDs := []string{items[0].ID, items[1].ID, items[2].ID, items[3].ID}
	require.Contains(t, itemIDs, visible.ID.String())
	require.Contains(t, itemIDs, noCover.ID.String())
	require.Contains(t, itemIDs, isbnOnly.ID.String())
	require.Contains(t, itemIDs, latin.ID.String())

	items, total, err = service.SearchPublicCatalog(context.Background(), "另一本书", 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	require.Equal(t, noCover.ID.String(), items[0].ID)

	items, total, err = service.SearchPublicCatalog(context.Background(), "仅有 ISBN", 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1, "搜索应保留仅有 ISBN、没有实际封面的书目")
	require.Equal(t, isbnOnly.ID.String(), items[0].ID)
}
