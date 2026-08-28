package books

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPublicCatalogSearchExcludesDraftsAndPrivateFields(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookRating{})
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

	items, total, err := NewService(db).SearchPublicCatalog(context.Background(), "Visible Author", 20, 0)

	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	require.Equal(t, work.ID.String(), items[0].ID)
	require.Equal(t, "Visible Author", items[0].Authors[0].Name)
	require.Len(t, items[0].Editions, 1)
	encoded, marshalErr := json.Marshal(items[0])
	require.NoError(t, marshalErr)
	require.NotContains(t, string(encoded), "object_key")
	require.NotContains(t, string(encoded), "private")

	_, err = NewService(db).GetPublicWork(context.Background(), draft.ID)
	require.Error(t, err)
}

func TestPublicCatalogRoutesDoNotRequireAuthentication(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookRating{})
	work := model.BookWork{Base: model.Base{ID: uuid.New()}, Title: "Public Route Book", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&work).Error)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/books"), NewService(db))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/books/catalog/search?q=Public", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Data struct {
			Items []BookPublicWorkDTO `json:"items"`
			Total int64               `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, int64(1), payload.Data.Total)
	require.Equal(t, work.ID.String(), payload.Data.Items[0].ID)
}
