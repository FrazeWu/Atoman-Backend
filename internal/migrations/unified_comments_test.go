package migrations

import (
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestUnifiedCommentSchemaCreatesRequiredIndexes(t *testing.T) {
	db := migrateUnifiedCommentSchema(t)

	assertIndexExists(t, db, "discussion_targets", "uq_discussion_target_kind_key")
	assertIndexExists(t, db, "comment_entries", "uq_comment_root_floor")
	assertIndexExists(t, db, "comment_likes", "uq_comment_like_user")
	assertIndexExists(t, db, "comment_reports", "uq_comment_report_user")
}

func TestUnifiedCommentIndexesEnforceWriteBehavior(t *testing.T) {
	db := migrateUnifiedCommentSchema(t)

	target := model.DiscussionTarget{Kind: "blog_post", ResourceKey: "post-1"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := db.Create(&model.DiscussionTarget{Kind: target.Kind, ResourceKey: target.ResourceKey}).Error; err == nil {
		t.Fatal("expected duplicate discussion target to be rejected")
	}

	commentID := uuid.New()
	userID := uuid.New()
	like := model.CommentLike{CommentID: commentID, UserID: userID}
	if err := db.Create(&like).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&model.CommentLike{CommentID: commentID, UserID: userID}).Error; err == nil {
		t.Fatal("expected duplicate active like to be rejected")
	}
	if err := db.Delete(&like).Error; err != nil {
		t.Fatalf("soft delete like: %v", err)
	}
	if err := db.Create(&model.CommentLike{CommentID: commentID, UserID: userID}).Error; err != nil {
		t.Fatalf("recreate like after soft delete: %v", err)
	}

	reporterID := uuid.New()
	report := model.CommentReport{CommentID: commentID, ReporterID: reporterID, Reason: "spam"}
	if err := db.Create(&report).Error; err != nil {
		t.Fatalf("create report: %v", err)
	}
	if err := db.Create(&model.CommentReport{CommentID: commentID, ReporterID: reporterID, Reason: "spam"}).Error; err == nil {
		t.Fatal("expected duplicate active report to be rejected")
	}
	if err := db.Delete(&report).Error; err != nil {
		t.Fatalf("soft delete report: %v", err)
	}
	if err := db.Create(&model.CommentReport{CommentID: commentID, ReporterID: reporterID, Reason: "spam"}).Error; err == nil {
		t.Fatal("expected report uniqueness to include deleted history")
	}

	floor := 1
	root := model.CommentEntry{
		TargetID:    target.ID,
		AuthorID:    userID,
		FloorNumber: &floor,
		Content:     "first root",
		ContentHash: "first-root",
	}
	if err := db.Create(&root).Error; err != nil {
		t.Fatalf("create root comment: %v", err)
	}
	duplicateRoot := model.CommentEntry{
		TargetID:    target.ID,
		AuthorID:    uuid.New(),
		FloorNumber: &floor,
		Content:     "duplicate floor",
		ContentHash: "duplicate-floor",
	}
	if err := db.Create(&duplicateRoot).Error; err == nil {
		t.Fatal("expected duplicate active root floor to be rejected")
	}
	if err := db.Delete(&root).Error; err != nil {
		t.Fatalf("soft delete root comment: %v", err)
	}
	reusedFloor := model.CommentEntry{
		TargetID:    target.ID,
		AuthorID:    uuid.New(),
		FloorNumber: &floor,
		Content:     "reused floor",
		ContentHash: "reused-floor",
	}
	if err := db.Create(&reusedFloor).Error; err != nil {
		t.Fatalf("reuse root floor after soft delete: %v", err)
	}
}

func migrateUnifiedCommentSchema(t *testing.T) *gorm.DB {
	t.Helper()
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
	return db
}
