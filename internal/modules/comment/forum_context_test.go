package comment

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveForumCommentRejectsOtherTargetKinds(t *testing.T) {
	for _, kind := range []string{TargetKindBlogPost, TargetKindVideo} {
		t.Run(kind, func(t *testing.T) {
			ctx := newCommentTestContext(t, kind, 0)
			created := ctx.create(t, 0, "other module comment", nil)
			_, err := ctx.service.ResolveForumComment(created.ID)
			require.ErrorIs(t, err, ErrCommentNotFound)
		})
	}
}
