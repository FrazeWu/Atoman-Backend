package comment

import (
	"net/http/httptest"
	"testing"

	"atoman/internal/platform/apperr"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAppErrorMapsStableCommentFailures(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{ErrCommentNotFound, 404, "comment.not_found"},
		{ErrCommentForbidden, 403, "comment.forbidden"},
		{ErrTargetLocked, 403, "comment.target_locked"},
		{ErrInvalidReply, 400, "comment.invalid_reply"},
		{ErrCommentRateLimited, 429, "comment.rate_limited"},
	}
	for _, test := range tests {
		mapped, ok := AppError(test.err).(*apperr.AppError)
		require.True(t, ok)
		require.Equal(t, test.status, mapped.HTTPStatus)
		require.Equal(t, test.code, mapped.Code)
	}
}

func TestWriteCommentErrorUsesPublicMapping(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{ErrCommentNotFound, 404},
		{ErrCommentForbidden, 403},
		{ErrInvalidReply, 400},
		{ErrCommentRateLimited, 429},
	} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		writeCommentError(context, test.err)
		require.Equal(t, test.status, recorder.Code)
	}
}
