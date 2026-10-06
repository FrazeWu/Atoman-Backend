package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func TestHealthRoutesExposeLivenessAndReadiness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerHealthRoutes(r, testdb.Open(t))

	for _, path := range []string{"/healthz", "/readyz"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
}

func TestHealthRouteReportsRedisStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerHealthRoutesWithRedis(r, testdb.Open(t), func(context.Context) redisHealthStatus {
		return redisHealthStatus{Configured: true, Reachable: true}
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Redis redisHealthStatus `json:"redis"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if !body.Data.Redis.Configured || !body.Data.Redis.Reachable {
		t.Fatalf("unexpected redis status: %#v", body.Data.Redis)
	}
}
