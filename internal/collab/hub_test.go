package collab

import (
	"net/http"
	"testing"
)

func TestWebSocketCheckOriginRejectsUnknownOrigin(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "")
	req, err := http.NewRequest(http.MethodGet, "/api/v1/collab/ws/room", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example")

	if upgrader.CheckOrigin(req) {
		t.Fatal("expected unknown origin to be rejected")
	}
}

func TestWebSocketCheckOriginAllowsDefaultDevelopmentOrigins(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "")

	for _, origin := range []string{
		"http://localhost:5173",
		"http://localhost:3000",
		"http://127.0.0.1:5173",
		"http://127.0.0.1:3000",
	} {
		t.Run(origin, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "/api/v1/collab/ws/room", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Origin", origin)

			if !upgrader.CheckOrigin(req) {
				t.Fatalf("expected %s to be allowed", origin)
			}
		})
	}
}

func TestWebSocketCheckOriginAllowsConfiguredOrigins(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "https://studio.example, https://atoman.example")
	req, err := http.NewRequest(http.MethodGet, "/api/v1/collab/ws/room", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "https://studio.example")

	if !upgrader.CheckOrigin(req) {
		t.Fatal("expected configured origin to be allowed")
	}
}

func TestWebSocketCheckOriginAllowsEmptyOrigin(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "")
	req, err := http.NewRequest(http.MethodGet, "/api/v1/collab/ws/room", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if !upgrader.CheckOrigin(req) {
		t.Fatal("expected empty origin to be allowed")
	}
}
