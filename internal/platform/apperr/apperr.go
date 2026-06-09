package apperr

import (
	"errors"
	"net/http"
)

type AppError struct {
	HTTPStatus int            `json:"-"`
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Cause      error          `json:"-"`
}

func (e *AppError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

func (e *AppError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func New(status int, code string, message string, details map[string]any) *AppError {
	if details == nil {
		details = map[string]any{}
	}
	return &AppError{HTTPStatus: status, Code: code, Message: message, Details: details}
}

func Wrap(status int, code string, message string, cause error) *AppError {
	return &AppError{HTTPStatus: status, Code: code, Message: message, Details: map[string]any{}, Cause: cause}
}

func BadRequest(code string, message string) *AppError {
	return New(http.StatusBadRequest, code, message, nil)
}

func Unauthorized(message string) *AppError {
	return New(http.StatusUnauthorized, "auth.unauthorized", message, nil)
}

func Forbidden(code string, message string) *AppError {
	return New(http.StatusForbidden, code, message, nil)
}

func NotFound(code string, message string) *AppError {
	return New(http.StatusNotFound, code, message, nil)
}

func Conflict(code string, message string) *AppError {
	return New(http.StatusConflict, code, message, nil)
}

func Unprocessable(code string, message string) *AppError {
	return New(http.StatusUnprocessableEntity, code, message, nil)
}

func Internal(cause error) *AppError {
	return Wrap(http.StatusInternalServerError, "system.internal_error", "Internal server error", cause)
}

func FromError(err error) *AppError {
	if err == nil {
		return nil
	}
	var app *AppError
	if errors.As(err, &app) {
		return app
	}
	return Internal(err)
}
