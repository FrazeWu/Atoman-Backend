package books

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookAnnotations(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.UserBookImport{}, &model.UserBookAsset{}, &model.UserBookAnnotation{})

	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	other := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}

	importID, assetID := uuid.New(), uuid.New()
	key := "books/private/notes-test.epub"
	seedReadyBookAsset(t, db, owner.ID, importID, assetID, key, []byte("epub content"))

	service := NewService(db)

	gin.SetMode(gin.TestMode)
	ownerRouter := gin.New()
	ownerRouter.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, owner)
		c.Next()
	})
	RegisterRoutes(ownerRouter.Group("/api/v1/books"), service)

	otherRouter := gin.New()
	otherRouter.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, other)
		c.Next()
	})
	RegisterRoutes(otherRouter.Group("/api/v1/books"), service)

	// 1. Create Annotation
	createPayload := CreateBookAnnotationInput{
		Format:       "epub",
		EPUBCFI:      "epubcfi(/6/4[chap01]!/4/2/10/1:0)",
		Text:         "This is an insightful passage on distributed systems.",
		Color:        "yellow",
		Style:        "highlight",
		Note:         "Important quote to remember",
		ChapterTitle: "Chapter 1: Foundations",
	}
	body, err := json.Marshal(createPayload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/books/assets/%s/annotations", assetID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	ownerRouter.ServeHTTP(resp, req)
	require.Equal(t, http.StatusCreated, resp.Code)

	var createdEnvelope struct {
		Data BookAnnotationDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &createdEnvelope))
	created1 := createdEnvelope.Data
	require.NotEmpty(t, created1.ID)
	require.Equal(t, assetID.String(), created1.AssetID)
	require.Equal(t, "This is an insightful passage on distributed systems.", created1.Text)
	require.Equal(t, "yellow", created1.Color)
	require.Equal(t, "highlight", created1.Style)
	require.Equal(t, "Important quote to remember", created1.Note)
	require.Equal(t, "Chapter 1: Foundations", created1.ChapterTitle)
	require.Equal(t, "HTTP reader", created1.BookTitle)

	// Create a second annotation (with color "pink" and note)
	createPayload2 := CreateBookAnnotationInput{
		Format:       "epub",
		EPUBCFI:      "epubcfi(/6/4[chap01]!/4/2/20/1:0)",
		Text:         "Second architecture quote.",
		Color:        "pink",
		Style:        "highlight",
		Note:         "Architecture thought",
		ChapterTitle: "Chapter 1: Foundations",
	}
	body2, err := json.Marshal(createPayload2)
	require.NoError(t, err)
	req2 := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/books/assets/%s/annotations", assetID), bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	resp2 := httptest.NewRecorder()
	ownerRouter.ServeHTTP(resp2, req2)
	require.Equal(t, http.StatusCreated, resp2.Code)
	var createdEnvelope2 struct {
		Data BookAnnotationDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp2.Body.Bytes(), &createdEnvelope2))
	created2 := createdEnvelope2.Data

	// Create a third annotation (without note)
	createPayload3 := CreateBookAnnotationInput{
		Format:  "epub",
		EPUBCFI: "epubcfi(/6/4[chap02]!/4/2/2/1:0)",
		Text:    "Third highlight without any note.",
		Color:   "blue",
		Style:   "underline",
	}
	body3, err := json.Marshal(createPayload3)
	require.NoError(t, err)
	req3 := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/books/assets/%s/annotations", assetID), bytes.NewReader(body3))
	req3.Header.Set("Content-Type", "application/json")
	resp3 := httptest.NewRecorder()
	ownerRouter.ServeHTTP(resp3, req3)
	require.Equal(t, http.StatusCreated, resp3.Code)

	// 2. Query Asset Annotations
	listReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/books/assets/%s/annotations", assetID), nil)
	listResp := httptest.NewRecorder()
	ownerRouter.ServeHTTP(listResp, listReq)
	require.Equal(t, http.StatusOK, listResp.Code)

	var listEnvelope struct {
		Data []BookAnnotationDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(listResp.Body.Bytes(), &listEnvelope))
	require.Len(t, listEnvelope.Data, 3)
	require.Equal(t, created1.ID, listEnvelope.Data[0].ID)
	require.Equal(t, created2.ID, listEnvelope.Data[1].ID)

	// 3. Query Global Notes
	// Search by keyword in text or note
	searchReq := httptest.NewRequest(http.MethodGet, "/api/v1/books/notes?q=insightful", nil)
	searchResp := httptest.NewRecorder()
	ownerRouter.ServeHTTP(searchResp, searchReq)
	require.Equal(t, http.StatusOK, searchResp.Code)

	var searchEnvelope struct {
		Data BookAnnotationListResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(searchResp.Body.Bytes(), &searchEnvelope))
	require.Equal(t, int64(1), searchEnvelope.Data.Total)
	require.Len(t, searchEnvelope.Data.Items, 1)
	require.Equal(t, created1.ID, searchEnvelope.Data.Items[0].ID)

	// Filter by has_note=true
	hasNoteReq := httptest.NewRequest(http.MethodGet, "/api/v1/books/notes?has_note=true", nil)
	hasNoteResp := httptest.NewRecorder()
	ownerRouter.ServeHTTP(hasNoteResp, hasNoteReq)
	require.Equal(t, http.StatusOK, hasNoteResp.Code)
	var hasNoteEnvelope struct {
		Data BookAnnotationListResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(hasNoteResp.Body.Bytes(), &hasNoteEnvelope))
	require.Equal(t, int64(2), hasNoteEnvelope.Data.Total)
	require.Len(t, hasNoteEnvelope.Data.Items, 2)

	// Filter by color=pink
	pinkReq := httptest.NewRequest(http.MethodGet, "/api/v1/books/notes?color=pink", nil)
	pinkResp := httptest.NewRecorder()
	ownerRouter.ServeHTTP(pinkResp, pinkReq)
	require.Equal(t, http.StatusOK, pinkResp.Code)
	var pinkEnvelope struct {
		Data BookAnnotationListResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(pinkResp.Body.Bytes(), &pinkEnvelope))
	require.Equal(t, int64(1), pinkEnvelope.Data.Total)
	require.Equal(t, created2.ID, pinkEnvelope.Data.Items[0].ID)

	// 4. Update Note & Color & Style
	newNote := "Updated note text"
	newColor := "green"
	newStyle := "underline"
	updatePayload := UpdateBookAnnotationInput{
		Note:  &newNote,
		Color: &newColor,
		Style: &newStyle,
	}
	updateBody, err := json.Marshal(updatePayload)
	require.NoError(t, err)

	updateReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/books/annotations/%s", created1.ID), bytes.NewReader(updateBody))
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	ownerRouter.ServeHTTP(updateResp, updateReq)
	require.Equal(t, http.StatusOK, updateResp.Code)

	var updateEnvelope struct {
		Data BookAnnotationDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(updateResp.Body.Bytes(), &updateEnvelope))
	require.Equal(t, "Updated note text", updateEnvelope.Data.Note)
	require.Equal(t, "green", updateEnvelope.Data.Color)
	require.Equal(t, "underline", updateEnvelope.Data.Style)

	// 5. Delete Annotation
	delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/books/annotations/%s", created1.ID), nil)
	delResp := httptest.NewRecorder()
	ownerRouter.ServeHTTP(delResp, delReq)
	require.Equal(t, http.StatusOK, delResp.Code)

	var delEnvelope struct {
		Data map[string]bool `json:"data"`
	}
	require.NoError(t, json.Unmarshal(delResp.Body.Bytes(), &delEnvelope))
	require.True(t, delEnvelope.Data["deleted"])

	// Verify count is now 2
	listReqAfter := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/books/assets/%s/annotations", assetID), nil)
	listRespAfter := httptest.NewRecorder()
	ownerRouter.ServeHTTP(listRespAfter, listReqAfter)
	require.Equal(t, http.StatusOK, listRespAfter.Code)
	var listEnvelopeAfter struct {
		Data []BookAnnotationDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(listRespAfter.Body.Bytes(), &listEnvelopeAfter))
	require.Len(t, listEnvelopeAfter.Data, 2)

	// 6. Access Control (other user cannot access/modify)
	// other cannot list annotations for owner's asset
	otherListReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/books/assets/%s/annotations", assetID), nil)
	otherListResp := httptest.NewRecorder()
	otherRouter.ServeHTTP(otherListResp, otherListReq)
	require.Equal(t, http.StatusNotFound, otherListResp.Code)

	// other cannot create annotation on owner's asset
	otherCreateReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/books/assets/%s/annotations", assetID), bytes.NewReader(body))
	otherCreateReq.Header.Set("Content-Type", "application/json")
	otherCreateResp := httptest.NewRecorder()
	otherRouter.ServeHTTP(otherCreateResp, otherCreateReq)
	require.Equal(t, http.StatusNotFound, otherCreateResp.Code)

	// other cannot update owner's annotation
	otherUpdateReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/books/annotations/%s", created2.ID), bytes.NewReader(updateBody))
	otherUpdateReq.Header.Set("Content-Type", "application/json")
	otherUpdateResp := httptest.NewRecorder()
	otherRouter.ServeHTTP(otherUpdateResp, otherUpdateReq)
	require.Equal(t, http.StatusNotFound, otherUpdateResp.Code)

	// other cannot delete owner's annotation
	otherDelReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/books/annotations/%s", created2.ID), nil)
	otherDelResp := httptest.NewRecorder()
	otherRouter.ServeHTTP(otherDelResp, otherDelReq)
	require.Equal(t, http.StatusNotFound, otherDelResp.Code)

	// other gets 0 global notes
	otherGlobalReq := httptest.NewRequest(http.MethodGet, "/api/v1/books/notes", nil)
	otherGlobalResp := httptest.NewRecorder()
	otherRouter.ServeHTTP(otherGlobalResp, otherGlobalReq)
	require.Equal(t, http.StatusOK, otherGlobalResp.Code)
	var otherGlobalEnvelope struct {
		Data BookAnnotationListResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(otherGlobalResp.Body.Bytes(), &otherGlobalEnvelope))
	require.Equal(t, int64(0), otherGlobalEnvelope.Data.Total)
	require.Empty(t, otherGlobalEnvelope.Data.Items)
}
