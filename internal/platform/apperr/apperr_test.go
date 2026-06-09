package apperr

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestAppErrorFields(t *testing.T) {
	err := New(http.StatusForbidden, "auth.forbidden", "Forbidden", map[string]any{"role": "user"})
	if err.HTTPStatus != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", err.HTTPStatus)
	}
	if err.Code != "auth.forbidden" {
		t.Fatalf("expected code auth.forbidden, got %q", err.Code)
	}
	if err.Message != "Forbidden" {
		t.Fatalf("expected message Forbidden, got %q", err.Message)
	}
	if err.Details["role"] != "user" {
		t.Fatalf("expected role detail user, got %#v", err.Details)
	}
}

func TestFromErrorDefaultsToInternal(t *testing.T) {
	app := FromError(errors.New("boom"))
	if app.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", app.HTTPStatus)
	}
	if app.Code != "system.internal_error" {
		t.Fatalf("expected system.internal_error, got %q", app.Code)
	}
}

func TestFromErrorReturnsExistingAppError(t *testing.T) {
	original := NotFound("blog.post_not_found", "Post not found")
	app := FromError(original)
	if app != original {
		t.Fatalf("expected same app error pointer")
	}
}

func TestFromErrorReturnsWrappedAppError(t *testing.T) {
	original := NotFound("blog.post_not_found", "Post not found")
	app := FromError(fmt.Errorf("load post: %w", original))
	if app != original {
		t.Fatalf("expected wrapped app error pointer")
	}
}
