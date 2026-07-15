package service

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seededTimelineProposalService(t *testing.T) (*TimelineRevisionProposalService, *gorm.DB, authctx.CurrentUser, model.TimelineEvent, model.TimelinePerson) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.MediaAsset{}, &model.TimelineEvent{}, &model.TimelinePerson{}, &model.TimelineRevision{}, &model.Revision{},
		&model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentMention{}, &model.CommentAttachment{},
		&model.CommentLike{}, &model.CommentReport{}, &model.CommentTimeAnchor{}, &model.CommentPublishRecord{},
		&model.Notification{}, &model.AuditLog{}, &model.TimelineRevisionProposal{},
	)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_timeline_test_target ON discussion_targets (kind, resource_key)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_timeline_test_floor ON comment_entries (target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE INDEX idx_timeline_test_publish ON comment_publish_records (author_id, created_at)`).Error)
	ownerModel := model.User{Username: "timeline-owner", Email: "owner@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&ownerModel).Error)
	owner := authctx.CurrentUser{ID: ownerModel.UUID, Username: ownerModel.Username, Role: ownerModel.Role}
	event := model.TimelineEvent{UserID: owner.ID, Title: "Old title", Location: "Paris", Source: "old source", IsPublic: true}
	person := model.TimelinePerson{UserID: owner.ID, Name: "Old name", Bio: "Old bio", IsPublic: true}
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, db.Create(&person).Error)
	return NewTimelineRevisionProposalService(db), db, owner, event, person
}

func TestAcceptTimelineProposalAppliesPatchAndRecordsRevision(t *testing.T) {
	svc, db, owner, event, _ := seededTimelineProposalService(t)
	proposal, err := svc.CreateEventProposal(owner, event.ID, TimelineProposalInput{
		Content: "Change the location", Evidence: "https://example.com/source", Patch: map[string]any{"location": "Berlin"},
	})
	require.NoError(t, err)

	decided, err := svc.Decide(owner, proposal.Comment.ID, "accept")
	require.NoError(t, err)
	require.Equal(t, "accepted", decided.Status)
	require.Equal(t, owner.ID, decided.Comment.AuthorID)
	require.Equal(t, owner.Username, decided.Comment.Author.Username)
	require.Equal(t, "Change the location", decided.Comment.Content)
	require.NotNil(t, decided.Comment.Attachments)
	require.NotNil(t, decided.Comment.Mentions)
	require.NotNil(t, decided.Comment.Replies)
	var stored model.TimelineEvent
	require.NoError(t, db.First(&stored, "id = ?", event.ID).Error)
	require.Equal(t, "Berlin", stored.Location)
	var revisions, audits int64
	require.NoError(t, db.Model(&model.TimelineRevision{}).Where("event_id = ?", event.ID).Count(&revisions).Error)
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ? AND entity_id = ?", "timeline_proposal.accept", proposal.Comment.ID).Count(&audits).Error)
	require.Equal(t, int64(1), revisions)
	require.Equal(t, int64(1), audits)
}

func TestTimelineProposalValidatesEvidenceChangesAndFieldAllowlist(t *testing.T) {
	svc, _, owner, event, person := seededTimelineProposalService(t)
	_, err := svc.CreateEventProposal(owner, event.ID, TimelineProposalInput{Content: "change", Patch: map[string]any{"location": "Berlin"}})
	require.ErrorIs(t, err, ErrTimelineProposalInvalid)
	_, err = svc.CreateEventProposal(owner, event.ID, TimelineProposalInput{Content: "change", Evidence: "source", Patch: map[string]any{}})
	require.ErrorIs(t, err, ErrTimelineProposalInvalid)
	_, err = svc.CreateEventProposal(owner, event.ID, TimelineProposalInput{Content: "change", Evidence: "source", Patch: map[string]any{"user_id": owner.ID.String()}})
	require.ErrorIs(t, err, ErrTimelineProposalInvalid)
	_, err = svc.CreatePersonProposal(owner, person.ID, TimelineProposalInput{Content: "change", Evidence: "source", Patch: map[string]any{"location": "Berlin"}})
	require.ErrorIs(t, err, ErrTimelineProposalInvalid)
}

func TestAcceptPersonProposalRecordsJSONRevisionAndRejectIsAudited(t *testing.T) {
	svc, db, owner, _, person := seededTimelineProposalService(t)
	accepted, err := svc.CreatePersonProposal(owner, person.ID, TimelineProposalInput{Content: "name source", Evidence: "archive", Patch: map[string]any{"name": "New name"}})
	require.NoError(t, err)
	_, err = svc.Decide(owner, accepted.Comment.ID, "accept")
	require.NoError(t, err)
	var revision model.Revision
	require.NoError(t, db.Where("content_type = ? AND content_id = ?", "timeline_person", person.ID).First(&revision).Error)
	require.Contains(t, string(revision.ContentSnapshot), "New name")

	rejected, err := svc.CreatePersonProposal(owner, person.ID, TimelineProposalInput{Content: "bio source", Evidence: "book", Patch: map[string]any{"bio": "Updated"}})
	require.NoError(t, err)
	decided, err := svc.Decide(owner, rejected.Comment.ID, "reject")
	require.NoError(t, err)
	require.Equal(t, "rejected", decided.Status)
	var audits int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ? AND entity_id = ?", "timeline_proposal.reject", rejected.Comment.ID).Count(&audits).Error)
	require.Equal(t, int64(1), audits)
}

func TestTimelineProposalDecisionRequiresTargetOwnerOrModeratorAndPending(t *testing.T) {
	svc, db, owner, event, _ := seededTimelineProposalService(t)
	proposal, err := svc.CreateEventProposal(owner, event.ID, TimelineProposalInput{Content: "change", Evidence: "source", Patch: map[string]any{"location": "Berlin"}})
	require.NoError(t, err)
	otherModel := model.User{Username: "other", Email: "other@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	moderatorModel := model.User{Username: "moderator", Email: "moderator@example.com", Password: "hash", Role: authctx.RoleModerator, IsActive: true}
	require.NoError(t, db.Create(&otherModel).Error)
	require.NoError(t, db.Create(&moderatorModel).Error)
	_, err = svc.Decide(authctx.CurrentUser{ID: otherModel.UUID, Role: otherModel.Role}, proposal.Comment.ID, "accept")
	require.ErrorIs(t, err, ErrTimelineProposalForbidden)
	_, err = svc.Decide(authctx.CurrentUser{ID: moderatorModel.UUID, Role: moderatorModel.Role}, proposal.Comment.ID, "reject")
	require.NoError(t, err)
	_, err = svc.Decide(owner, proposal.Comment.ID, "accept")
	require.ErrorIs(t, err, ErrTimelineProposalNotPending)
}

func TestTimelineProposalChildrenUseCommentCore(t *testing.T) {
	svc, db, owner, event, _ := seededTimelineProposalService(t)
	_, err := comment.NewService(db, comment.NewTargetRegistry(db)).Create(owner,
		comment.TargetRef{Kind: comment.TargetKindTimelineEvent, ResourceID: event.ID},
		comment.CreateCommentInput{Content: "ordinary root"},
	)
	require.ErrorIs(t, err, comment.ErrInvalidContent)
	root, err := svc.CreateEventProposal(owner, event.ID, TimelineProposalInput{Content: "change", Evidence: "source", Patch: map[string]any{"location": "Berlin"}})
	require.NoError(t, err)
	child, err := comment.NewService(db, comment.NewTargetRegistry(db)).Create(owner,
		comment.TargetRef{Kind: comment.TargetKindTimelineEvent, ResourceID: event.ID},
		comment.CreateCommentInput{Content: "discussion", ReplyToID: &root.Comment.ID},
	)
	require.NoError(t, err)
	require.Equal(t, root.Comment.ID, *child.RootID)
}

func TestListTimelineProposalsIncludesTypedStateAndComments(t *testing.T) {
	svc, _, owner, event, _ := seededTimelineProposalService(t)
	created, err := svc.CreateEventProposal(owner, event.ID, TimelineProposalInput{Content: "change", Evidence: "source", Patch: map[string]any{"location": "Berlin"}})
	require.NoError(t, err)
	listed, err := svc.List(owner, comment.TargetKindTimelineEvent, event.ID, 1, 1)
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	require.Equal(t, created.Comment.ID, listed.Items[0].Comment.ID)
	require.Equal(t, "pending", listed.Items[0].Status)
	require.Equal(t, "source", listed.Items[0].Evidence)
	require.Equal(t, 1, listed.PerPage)
	require.False(t, listed.HasMore)
}

func TestTimelineProposalAcceptsBCEDateWithTimelinePrecision(t *testing.T) {
	svc, _, owner, event, _ := seededTimelineProposalService(t)
	proposal, err := svc.CreateEventProposal(owner, event.ID, TimelineProposalInput{Content: "date", Evidence: "archive", Patch: map[string]any{"event_date": "-0044-03-15"}})
	require.NoError(t, err)
	require.Equal(t, "-0044-03-15T00:00:00Z", proposal.Patch["event_date"])
}
