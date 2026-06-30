package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestSetAlbumProtectionCanBeReenabledAfterSoftDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.ContentProtection{})

	admin := model.User{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "hash",
		Role:     authctx.RoleAdmin,
		IsActive: true,
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	contentID := uuid.New()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{
			ID:       admin.UUID,
			Username: admin.Username,
			Role:     authctx.RoleAdmin,
		})
		c.Next()
	})
	router.PUT("/api/v1/albums/:id/protection", SetAlbumProtectionHandler(db))
	router.DELETE("/api/v1/albums/:id/protection", RemoveAlbumProtectionHandler(db))

	putProtection(t, router, contentID.String(), SetProtectionInput{
		ProtectionLevel: "full",
		Reason:          "initial",
	}, http.StatusOK)

	deleteProtection(t, router, contentID.String(), http.StatusOK)

	putProtection(t, router, contentID.String(), SetProtectionInput{
		ProtectionLevel: "semi",
		Reason:          "reenabled",
	}, http.StatusOK)

	var rows []model.ContentProtection
	if err := db.Unscoped().
		Where("content_type = ? AND content_id = ?", "album", contentID).
		Order("created_at ASC").
		Find(&rows).Error; err != nil {
		t.Fatalf("load protections: %v", err)
	}

	if len(rows) != 2 {
		t.Fatalf("expected 2 protection rows including soft-deleted history, got %d", len(rows))
	}
	if !rows[0].DeletedAt.Valid {
		t.Fatalf("expected first protection row to be soft-deleted")
	}
	if rows[1].DeletedAt.Valid {
		t.Fatalf("expected re-enabled protection row to remain live")
	}
	if rows[1].ProtectionLevel != "semi" {
		t.Fatalf("expected latest protection level semi, got %q", rows[1].ProtectionLevel)
	}
}

func TestIsContentProtectionDuplicateKeyErrorRecognizesLiveUniqueIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.ContentProtection{})

	contentID := uuid.New()
	protection := model.ContentProtection{
		ContentType:     "album",
		ContentID:       contentID,
		ProtectionLevel: "full",
		ProtectedBy:     uuid.New(),
	}
	if err := db.Create(&protection).Error; err != nil {
		t.Fatalf("create protection: %v", err)
	}

	duplicate := model.ContentProtection{
		ContentType:     "album",
		ContentID:       contentID,
		ProtectionLevel: "semi",
		ProtectedBy:     uuid.New(),
	}
	err := db.Create(&duplicate).Error
	if err == nil {
		t.Fatal("expected duplicate live protection to fail")
	}
	if !isContentProtectionDuplicateKeyError(err) {
		t.Fatalf("expected content protection duplicate key error, got %v", err)
	}
}

func putProtection(t *testing.T, router *gin.Engine, id string, input SetProtectionInput, wantStatus int) {
	t.Helper()

	body, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/v1/albums/"+id+"/protection", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != wantStatus {
		t.Fatalf("PUT protection status = %d, want %d, body=%s", w.Code, wantStatus, w.Body.String())
	}
}

func deleteProtection(t *testing.T, router *gin.Engine, id string, wantStatus int) {
	t.Helper()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/albums/"+id+"/protection", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != wantStatus {
		t.Fatalf("DELETE protection status = %d, want %d, body=%s", w.Code, wantStatus, w.Body.String())
	}
}
