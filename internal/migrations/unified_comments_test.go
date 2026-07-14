package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestUnifiedCommentSchemaCreatesRequiredIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentMention{},
		&model.CommentAttachment{},
		&model.CommentLike{},
		&model.CommentReport{},
		&model.CommentTimeAnchor{},
		&model.TimelineRevisionProposal{},
		&model.DebateArgumentDetail{},
		&model.DebateArgumentReference{},
		&model.DebateArgumentDebateRef{},
	)

	if err := RunUnifiedCommentIndexes(db); err != nil {
		t.Fatalf("run unified comment indexes: %v", err)
	}

	assertIndexExists(t, db, "discussion_targets", "uq_discussion_target_kind_key")
	assertIndexExists(t, db, "comment_entries", "uq_comment_root_floor")
	assertIndexExists(t, db, "comment_likes", "uq_comment_like_user")
	assertIndexExists(t, db, "comment_reports", "uq_comment_report_user")
}
