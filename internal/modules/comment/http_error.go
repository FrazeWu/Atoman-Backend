package comment

import (
	"errors"
	"net/http"

	"atoman/internal/platform/apperr"
)

func AppError(err error) error {
	var existing *apperr.AppError
	if errors.As(err, &existing) {
		return existing
	}
	status, code, message := http.StatusInternalServerError, "comment.internal_error", "Internal server error"
	switch {
	case errors.Is(err, ErrAuthenticationRequired):
		status, code, message = 401, "comment.authentication_required", "Authentication is required"
	case errors.Is(err, ErrTargetLocked):
		status, code, message = 403, "comment.target_locked", "Discussion target is locked"
	case errors.Is(err, ErrTargetNotVisible), errors.Is(err, ErrCommentForbidden):
		status, code, message = 403, "comment.forbidden", "Comment operation is forbidden"
	case errors.Is(err, ErrTargetNotFound):
		status, code, message = 404, "comment.target_not_found", "Discussion target not found"
	case errors.Is(err, ErrCommentNotFound):
		status, code, message = 404, "comment.not_found", "Comment not found"
	case errors.Is(err, ErrInvalidCommentID):
		status, code, message = 400, "comment.invalid_id", "Invalid comment ID"
	case errors.Is(err, ErrCommentRateLimited):
		status, code, message = 429, "comment.rate_limited", "Comment rate limit exceeded"
	case errors.Is(err, ErrDuplicateComment):
		status, code, message = 409, "comment.duplicate", "Duplicate comment"
	case errors.Is(err, ErrUnknownTargetKind), errors.Is(err, ErrInvalidTargetResource):
		status, code, message = 400, "comment.invalid_target", "Invalid discussion target"
	case errors.Is(err, ErrInvalidContent):
		status, code, message = 400, "comment.invalid_content", "Invalid comment content"
	case errors.Is(err, ErrInvalidReply):
		status, code, message = 400, "comment.invalid_reply", "Invalid comment reply"
	case errors.Is(err, ErrInvalidAttachment):
		status, code, message = 400, "comment.invalid_attachment", "Invalid comment attachment"
	case errors.Is(err, ErrInvalidMention):
		status, code, message = 400, "comment.invalid_mention", "Invalid comment mention"
	case errors.Is(err, ErrInvalidReport):
		status, code, message = 400, "comment.invalid_report", "Invalid comment report"
	case errors.Is(err, ErrInvalidModeration):
		status, code, message = 400, "comment.invalid_moderation", "Invalid moderation action"
	case errors.Is(err, ErrInvalidMark):
		status, code, message = 400, "comment.invalid_mark", "Invalid marked comment"
	case errors.Is(err, ErrInvalidListOptions):
		status, code, message = 400, "comment.invalid_list", "Invalid list options"
	case errors.Is(err, ErrInvalidJSON):
		status, code, message = 400, "comment.invalid_json", "Invalid request body"
	case errors.Is(err, ErrUnsupportedMediaType):
		status, code, message = 415, "comment.unsupported_media_type", "Content-Type must be JSON"
	case errors.Is(err, ErrRequestTooLarge):
		status, code, message = 413, "comment.request_too_large", "Request body is too large"
	}
	return apperr.New(status, code, message, nil)
}
