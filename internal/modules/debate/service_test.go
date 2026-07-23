package debate

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type debateTestContext struct {
	db      *gorm.DB
	service *Service
	owner   authctx.CurrentUser
	editor  authctx.CurrentUser
	admin   authctx.CurrentUser
}

func newDebateTestContext(t *testing.T) debateTestContext {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Debate{}, &model.Revision{}, &model.ContentProtection{},
		&model.DebateConclusionEvent{}, &model.DebateRevisionReference{}, &model.DebateRelation{}, &model.DebateVote{}, &model.ContentReference{},
		&model.Post{}, &model.ForumCategory{}, &model.ForumTopic{}, &model.FeedSource{}, &model.FeedItem{},
		&model.Artist{}, &model.Album{}, &model.Song{}, &model.Playlist{}, &model.PodcastEpisode{},
		&model.Video{}, &model.TimelinePerson{}, &model.TimelineEvent{}, &model.Channel{}, &model.Collection{},
		&model.DiscussionTarget{}, &model.CommentEntry{},
	)
	users := []model.User{
		{UUID: uuid.New(), Username: "owner", Email: "owner@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true},
		{UUID: uuid.New(), Username: "editor", Email: "editor@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true},
		{UUID: uuid.New(), Username: "admin", Email: "admin@example.com", Password: "hash", Role: authctx.RoleAdmin, IsActive: true},
	}
	require.NoError(t, db.Create(&users).Error)
	return debateTestContext{
		db: db, service: NewService(db),
		owner:  authctx.CurrentUser{ID: users[0].UUID, Username: users[0].Username, Role: users[0].Role},
		editor: authctx.CurrentUser{ID: users[1].UUID, Username: users[1].Username, Role: users[1].Role},
		admin:  authctx.CurrentUser{ID: users[2].UUID, Username: users[2].Username, Role: users[2].Role},
	}
}

func createDebateForTest(t *testing.T, ctx debateTestContext, title, content string) DebateDTO {
	t.Helper()
	created, err := ctx.service.CreateDebate(ctx.owner, CreateDebateRequest{Title: title, Description: "summary", Content: content, Tags: []string{"tag"}})
	require.NoError(t, err)
	return created
}

func TestCreateDebateCreatesCurrentVersionOneInOneTransaction(t *testing.T) {
	ctx := newDebateTestContext(t)
	created := createDebateForTest(t, ctx, "Topic", "Body")

	require.Equal(t, model.DebateStatusActive, created.Status)
	require.NotNil(t, created.CurrentRevisionID)
	var revision model.Revision
	require.NoError(t, ctx.db.First(&revision, "id = ?", *created.CurrentRevisionID).Error)
	require.Equal(t, 1, revision.VersionNumber)
	require.Equal(t, "creation", revision.EditType)
	require.Equal(t, "approved", revision.Status)
	require.True(t, revision.IsCurrent)
	require.Equal(t, ctx.owner.ID, revision.EditorID)
}

func TestSaveWikiStoresConcludedDebateReferenceInUnifiedReferences(t *testing.T) {
	ctx := newDebateTestContext(t)
	source := createDebateForTest(t, ctx, "Source", "body")
	target := createDebateForTest(t, ctx, "Target", "body")
	setConclusionForTest(t, ctx.db, source.ID, model.DebateVoteYes)

	content := "See @debate:" + source.ID.String() + ":support"
	saved, err := ctx.service.SaveWiki(ctx.editor, target.ID, SaveWikiRequest{
		Title: "Target", Content: content, EditSummary: "cite conclusion", BaseRevisionID: *target.CurrentRevisionID,
	})
	require.NoError(t, err)
	require.Len(t, saved.References, 1)
	require.Equal(t, "debate", saved.References[0].Target.Type)
	require.Equal(t, source.ID, saved.References[0].Target.ID)
	require.NotNil(t, saved.References[0].Relation)
	require.Equal(t, "support", saved.References[0].Relation.Stance)

	var stored model.ContentReference
	require.NoError(t, ctx.db.First(&stored, "source_type = ? AND source_id = ?", "debate_revision", *saved.CurrentRevisionID).Error)
	require.Equal(t, "content", stored.SourceField)
	require.Equal(t, source.ID, stored.TargetID)

	var extension model.DebateRevisionReference
	require.NoError(t, ctx.db.First(&extension, "revision_id = ?", *saved.CurrentRevisionID).Error)
	require.Equal(t, stored.ID, extension.ContentReferenceID)
}

func TestCreateAndSaveWikiValidateTitleSummaryAndOptimisticLock(t *testing.T) {
	ctx := newDebateTestContext(t)
	_, err := ctx.service.CreateDebate(ctx.owner, CreateDebateRequest{Title: "@post:" + uuid.NewString(), Content: "Body"})
	requireAppError(t, err, "debate.title_reference", 400)

	created := createDebateForTest(t, ctx, "Topic", "Body")
	_, err = ctx.service.SaveWiki(ctx.editor, created.ID, SaveWikiRequest{Title: "Edited", Description: "New", Content: "Body 2", Tags: []string{"new"}, EditSummary: "", BaseRevisionID: *created.CurrentRevisionID})
	requireAppError(t, err, "validation.invalid_request", 400)

	saved, err := ctx.service.SaveWiki(ctx.editor, created.ID, SaveWikiRequest{Title: "Edited", Description: "New", Content: "Body 2", Tags: []string{"new"}, EditSummary: "Improve", BaseRevisionID: *created.CurrentRevisionID})
	require.NoError(t, err)
	require.Equal(t, "Edited", saved.Title)
	require.NotEqual(t, created.CurrentRevisionID, saved.CurrentRevisionID)

	var revisions []model.Revision
	require.NoError(t, ctx.db.Where("content_type = ? AND content_id = ?", debateContentType, created.ID).Order("version_number").Find(&revisions).Error)
	require.Len(t, revisions, 2)
	require.False(t, revisions[0].IsCurrent)
	require.True(t, revisions[1].IsCurrent)
	require.Equal(t, revisions[0].ID, *revisions[1].PreviousRevisionID)
	require.Equal(t, 2, revisions[1].VersionNumber)

	_, err = ctx.service.SaveWiki(ctx.owner, created.ID, SaveWikiRequest{Title: "Conflict", EditSummary: "Old base", BaseRevisionID: *created.CurrentRevisionID})
	var appErr *apperr.AppError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, "debate.edit_conflict", appErr.Code)
	require.Equal(t, created.CurrentRevisionID.String(), fmt.Sprint(appErr.Details["base_revision_id"]))
	require.Equal(t, saved.CurrentRevisionID.String(), fmt.Sprint(appErr.Details["current_revision_id"]))
}

func TestWikiProtectionAndArchiveRules(t *testing.T) {
	ctx := newDebateTestContext(t)
	created := createDebateForTest(t, ctx, "Topic", "Body")
	expires := time.Now().Add(time.Hour)
	require.NoError(t, ctx.service.SetProtection(ctx.admin, created.ID, ProtectionRequest{ProtectionLevel: "full", Reason: "locked", ExpiresAt: &expires}))

	_, err := ctx.service.SaveWiki(ctx.editor, created.ID, SaveWikiRequest{Title: "Blocked", EditSummary: "edit", BaseRevisionID: *created.CurrentRevisionID})
	requireAppError(t, err, "debate.protected", 403)
	adminSaved, err := ctx.service.SaveWiki(ctx.admin, created.ID, SaveWikiRequest{Title: "Admin edit", Description: "summary", Content: "body", EditSummary: "admin", BaseRevisionID: *created.CurrentRevisionID})
	require.NoError(t, err)

	archived, err := ctx.service.ArchiveDebate(ctx.admin, created.ID)
	require.NoError(t, err)
	require.Equal(t, model.DebateStatusArchived, archived.Status)
	_, err = ctx.service.SaveWiki(ctx.admin, created.ID, SaveWikiRequest{Title: "No", EditSummary: "archived", BaseRevisionID: *adminSaved.CurrentRevisionID})
	requireAppError(t, err, "debate.archived", 409)
	_, err = ctx.service.ArchiveDebate(ctx.owner, created.ID)
	requireAppError(t, err, "debate.admin_required", 403)
}

func TestSaveWikiLocksDebatesInStableOrder(t *testing.T) {
	ctx := newDebateTestContext(t)
	low := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	middle := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	high := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	seedDebateWithID(t, ctx, low, "Low")
	seedDebateWithID(t, ctx, middle, "Middle")
	target := seedDebateWithID(t, ctx, high, "Target")
	setConclusionForTest(t, ctx.db, low, model.DebateVoteYes)
	setConclusionForTest(t, ctx.db, middle, model.DebateVoteYes)
	locked := captureLockedDebateIDs(t, ctx.db)

	_, err := ctx.service.SaveWiki(ctx.editor, high, SaveWikiRequest{
		Title: "Target", Content: "@debate:" + middle.String() + ":support @debate:" + low.String() + ":support",
		EditSummary: "ordered locks", BaseRevisionID: *target.CurrentRevisionID,
	})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{low, middle, high}, *locked)
}

func TestReconfirmLocksDebatesBeforeRelationInStableOrder(t *testing.T) {
	ctx := newDebateTestContext(t)
	sourceID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	targetID := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	seedDebateWithID(t, ctx, sourceID, "Source")
	target := seedDebateWithID(t, ctx, targetID, "Target")
	setConclusionForTest(t, ctx.db, sourceID, model.DebateVoteYes)
	relation := insertRelationForTest(t, ctx.db, sourceID, targetID, model.DebateRelationSupport, *target.CurrentRevisionID)
	require.NoError(t, ctx.db.Model(&relation).Update("status", model.DebateRelationStale).Error)
	locked := captureLockedDebateIDs(t, ctx.db)

	_, err := ctx.service.ReconfirmReference(ctx.editor, targetID, relation.ID, ReconfirmReferenceRequest{
		BaseRevisionID: *target.CurrentRevisionID, EditSummary: "reconfirm",
	})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{sourceID, targetID}, *locked)
}

func TestProtectionMutationsLockDebate(t *testing.T) {
	ctx := newDebateTestContext(t)
	created := createDebateForTest(t, ctx, "Protected", "body")

	locked := captureLockedDebateIDs(t, ctx.db)
	require.NoError(t, ctx.service.SetProtection(ctx.admin, created.ID, ProtectionRequest{ProtectionLevel: "full"}))
	require.Equal(t, []uuid.UUID{created.ID}, *locked)

	locked = captureLockedDebateIDs(t, ctx.db)
	require.NoError(t, ctx.service.DeleteProtection(ctx.admin, created.ID))
	require.Equal(t, []uuid.UUID{created.ID}, *locked)
}

func TestRevertAndReconfirmRejectMissingBaseRevision(t *testing.T) {
	ctx := newDebateTestContext(t)
	created := createDebateForTest(t, ctx, "Topic", "body")
	revision, err := ctx.service.GetRevision(created.ID, *created.CurrentRevisionID)
	require.NoError(t, err)

	_, err = ctx.service.RevertRevision(ctx.editor, created.ID, revision.ID, RevertRevisionRequest{EditSummary: "revert"})
	requireAppError(t, err, "validation.invalid_request", 400)
	_, err = ctx.service.ReconfirmReference(ctx.editor, created.ID, uuid.New(), ReconfirmReferenceRequest{EditSummary: "reconfirm"})
	requireAppError(t, err, "validation.invalid_request", 400)
}

func TestArchiveMakesActiveOutboundRelationsUnavailable(t *testing.T) {
	ctx := newDebateTestContext(t)
	source := createDebateForTest(t, ctx, "Source", "source")
	target := createDebateForTest(t, ctx, "Target", "target")
	setConclusionForTest(t, ctx.db, source.ID, model.DebateVoteYes)
	relation := insertRelationForTest(t, ctx.db, source.ID, target.ID, model.DebateRelationSupport, *target.CurrentRevisionID)

	_, err := ctx.service.ArchiveDebate(ctx.admin, source.ID)
	require.NoError(t, err)
	var stored model.DebateRelation
	require.NoError(t, ctx.db.First(&stored, "id = ?", relation.ID).Error)
	require.Equal(t, model.DebateRelationUnavailable, stored.Status)
}

func TestRevisionListReadDiffAndRevert(t *testing.T) {
	ctx := newDebateTestContext(t)
	created := createDebateForTest(t, ctx, "One", "Body 1")
	second, err := ctx.service.SaveWiki(ctx.editor, created.ID, SaveWikiRequest{Title: "Two", Description: "summary 2", Content: "Body 2", Tags: []string{"two"}, EditSummary: "second", BaseRevisionID: *created.CurrentRevisionID})
	require.NoError(t, err)

	revisions, err := ctx.service.ListRevisions(created.ID)
	require.NoError(t, err)
	require.Len(t, revisions, 2)
	one, err := ctx.service.GetRevision(created.ID, revisions[1].ID)
	require.NoError(t, err)
	require.Equal(t, "One", one.Snapshot.Title)
	diff, err := ctx.service.DiffRevisions(created.ID, revisions[0].ID, revisions[1].ID)
	require.NoError(t, err)
	require.True(t, diff.Changes["title"].Changed)
	require.True(t, diff.Changes["description"].Changed)
	require.True(t, diff.Changes["content"].Changed)
	require.True(t, diff.Changes["tags"].Changed)

	reverted, err := ctx.service.RevertRevision(ctx.editor, created.ID, revisions[1].ID, RevertRevisionRequest{BaseRevisionID: *second.CurrentRevisionID, EditSummary: "restore"})
	require.NoError(t, err)
	require.Equal(t, "One", reverted.Title)
	current, err := ctx.service.GetRevision(created.ID, *reverted.CurrentRevisionID)
	require.NoError(t, err)
	require.Equal(t, 3, current.VersionNumber)
	require.Equal(t, "revert", current.EditType)
}

func TestDebateReferencesProjectRelationsRejectConflictAndCycle(t *testing.T) {
	ctx := newDebateTestContext(t)
	a := createDebateForTest(t, ctx, "A", "A")
	b := createDebateForTest(t, ctx, "B", "B")
	setConclusionForTest(t, ctx.db, a.ID, model.DebateVoteYes)
	setConclusionForTest(t, ctx.db, b.ID, model.DebateVoteNo)

	refB := "@debate:" + b.ID.String() + ":support"
	a2, err := ctx.service.SaveWiki(ctx.editor, a.ID, SaveWikiRequest{Title: "A", Description: "summary", Content: refB + " and " + refB, EditSummary: "cite B twice", BaseRevisionID: *a.CurrentRevisionID})
	require.NoError(t, err)
	require.Len(t, a2.References, 2)
	var relations []model.DebateRelation
	require.NoError(t, ctx.db.Find(&relations).Error)
	require.Len(t, relations, 1)
	require.Equal(t, b.ID, relations[0].SourceDebateID)
	require.Equal(t, a.ID, relations[0].TargetDebateID)
	revision, err := ctx.service.GetRevision(a.ID, *a2.CurrentRevisionID)
	require.NoError(t, err)
	require.Len(t, revision.References, 2)
	require.Equal(t, "B", revision.References[0].Title)

	_, err = ctx.service.SaveWiki(ctx.editor, a.ID, SaveWikiRequest{Title: "A", Content: refB + " @debate:" + b.ID.String() + ":oppose", EditSummary: "conflict", BaseRevisionID: *a2.CurrentRevisionID})
	requireAppError(t, err, "debate.reference_conflict", 409)

	_, err = ctx.service.SaveWiki(ctx.editor, b.ID, SaveWikiRequest{Title: "B", Content: "@debate:" + a.ID.String() + ":support", EditSummary: "cycle", BaseRevisionID: *b.CurrentRevisionID})
	var appErr *apperr.AppError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, "debate.reference_cycle", appErr.Code)
	require.NotEmpty(t, appErr.Details["path"])
}

func TestSaveWikiRollsBackRevisionDebateAndRelationsWhenReferenceSnapshotFails(t *testing.T) {
	ctx := newDebateTestContext(t)
	oldSource := createDebateForTest(t, ctx, "Old source", "old")
	newSource := createDebateForTest(t, ctx, "New source", "new")
	setConclusionForTest(t, ctx.db, oldSource.ID, model.DebateVoteYes)
	setConclusionForTest(t, ctx.db, newSource.ID, model.DebateVoteNo)
	target := createDebateForTest(t, ctx, "Target", "body")
	oldRaw := "@debate:" + oldSource.ID.String() + ":support"
	before, err := ctx.service.SaveWiki(ctx.editor, target.ID, SaveWikiRequest{Title: "Before", Content: oldRaw, EditSummary: "old ref", BaseRevisionID: *target.CurrentRevisionID})
	require.NoError(t, err)
	require.Len(t, before.References, 1)

	injected := errors.New("injected reference snapshot failure")
	callback := "test:fail-debate-reference-create"
	require.NoError(t, ctx.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "debate_revision_references" {
			tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = ctx.db.Callback().Create().Remove(callback) })
	newRaw := "@debate:" + newSource.ID.String() + ":oppose"
	_, err = ctx.service.SaveWiki(ctx.editor, target.ID, SaveWikiRequest{Title: "After", Content: newRaw, EditSummary: "new ref", BaseRevisionID: *before.CurrentRevisionID})
	require.ErrorIs(t, err, injected)
	require.NoError(t, ctx.db.Callback().Create().Remove(callback))

	var stored model.Debate
	require.NoError(t, ctx.db.First(&stored, "id = ?", target.ID).Error)
	require.Equal(t, "Before", stored.Title)
	require.Equal(t, oldRaw, stored.Content)
	require.Equal(t, before.CurrentRevisionID, stored.CurrentRevisionID)
	var revisions []model.Revision
	require.NoError(t, ctx.db.Where("content_type = ? AND content_id = ?", debateContentType, target.ID).Order("version_number").Find(&revisions).Error)
	require.Len(t, revisions, 2)
	require.True(t, revisions[1].IsCurrent)
	var refs []model.DebateRevisionReference
	require.NoError(t, ctx.db.Find(&refs).Error)
	require.Len(t, refs, 1)
	require.Equal(t, oldSource.ID, refs[0].ResourceID)
	var relations []model.DebateRelation
	require.NoError(t, ctx.db.Where("target_debate_id = ?", target.ID).Find(&relations).Error)
	require.Len(t, relations, 1)
	require.Equal(t, oldSource.ID, relations[0].SourceDebateID)
	require.Equal(t, *before.CurrentRevisionID, relations[0].TargetRevisionID)
}

func TestStaleReferencesCanBeInheritedAndReconfirmed(t *testing.T) {
	ctx := newDebateTestContext(t)
	source := createDebateForTest(t, ctx, "Source", "source")
	target := createDebateForTest(t, ctx, "Target", "target")
	firstEvent := setConclusionForTest(t, ctx.db, source.ID, model.DebateVoteYes)
	raw := "@debate:" + source.ID.String() + ":support"
	target2, err := ctx.service.SaveWiki(ctx.editor, target.ID, SaveWikiRequest{Title: "Target", Content: raw, EditSummary: "cite", BaseRevisionID: *target.CurrentRevisionID})
	require.NoError(t, err)
	require.Len(t, target2.References, 1)
	relationID := *target2.References[0].RelationID
	require.NoError(t, ctx.db.Model(&model.DebateRelation{}).Where("id = ?", relationID).Update("status", model.DebateRelationStale).Error)
	require.NoError(t, ctx.db.Model(&model.Debate{}).Where("id = ?", source.ID).Update("status", model.DebateStatusArchived).Error)

	inherited, err := ctx.service.SaveWiki(ctx.editor, target.ID, SaveWikiRequest{Title: "Target updated", Content: raw, EditSummary: "copy stale", BaseRevisionID: *target2.CurrentRevisionID})
	require.NoError(t, err)
	require.Equal(t, model.DebateRelationStale, inherited.References[0].State)
	_, err = ctx.service.SaveWiki(ctx.editor, target.ID, SaveWikiRequest{Title: "Target", Content: raw + " " + raw, EditSummary: "new invalid", BaseRevisionID: *inherited.CurrentRevisionID})
	requireAppError(t, err, "debate.reference_unavailable", 400)

	require.NoError(t, ctx.db.Model(&model.Debate{}).Where("id = ?", source.ID).Update("status", model.DebateStatusActive).Error)
	latestEvent := setConclusionForTest(t, ctx.db, source.ID, model.DebateVoteNo)
	reconfirmed, err := ctx.service.ReconfirmReference(ctx.editor, target.ID, relationID, ReconfirmReferenceRequest{BaseRevisionID: *inherited.CurrentRevisionID, EditSummary: "refresh source"})
	require.NoError(t, err)
	require.NotEqual(t, inherited.CurrentRevisionID, reconfirmed.CurrentRevisionID)
	var relation model.DebateRelation
	require.NoError(t, ctx.db.First(&relation, "id = ?", relationID).Error)
	require.Equal(t, model.DebateRelationActive, relation.Status)
	require.Equal(t, latestEvent.ID, relation.SourceConclusionEventID)
	require.NotEqual(t, firstEvent.ID, relation.SourceConclusionEventID)
}

func TestGetDebateGraphTreeAndGraphViews(t *testing.T) {
	ctx := newDebateTestContext(t)
	root := createDebateForTest(t, ctx, "Root", "root")
	support := createDebateForTest(t, ctx, "Support", "support")
	oppose := createDebateForTest(t, ctx, "Oppose", "oppose")
	far := createDebateForTest(t, ctx, "Far", "far")
	for _, item := range []DebateDTO{support, oppose, far} {
		setConclusionForTest(t, ctx.db, item.ID, model.DebateVoteYes)
	}
	insertRelationForTest(t, ctx.db, support.ID, root.ID, model.DebateRelationSupport, *root.CurrentRevisionID)
	insertRelationForTest(t, ctx.db, oppose.ID, root.ID, model.DebateRelationOppose, *root.CurrentRevisionID)
	insertRelationForTest(t, ctx.db, far.ID, support.ID, model.DebateRelationSupport, *support.CurrentRevisionID)

	tree, err := ctx.service.GetDebateGraph(root.ID, "tree", 1)
	require.NoError(t, err)
	require.Len(t, tree.Nodes, 2)
	require.Len(t, tree.Relations, 1)
	require.Equal(t, []uuid.UUID{support.ID}, tree.ExpandableNodeIDs)
	graph, err := ctx.service.GetDebateGraph(root.ID, "graph", 1)
	require.NoError(t, err)
	require.Len(t, graph.Nodes, 3)
	require.Len(t, graph.Relations, 2)
}

func setConclusionForTest(t *testing.T, db *gorm.DB, debateID uuid.UUID, direction string) model.DebateConclusionEvent {
	t.Helper()
	event := model.DebateConclusionEvent{DebateID: debateID, Direction: direction, YesVotes: 8, NoVotes: 2, TotalVotes: 10}
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, db.Model(&model.Debate{}).Where("id = ?", debateID).Updates(map[string]any{"conclusion_type": direction, "current_conclusion_event_id": event.ID}).Error)
	return event
}

func insertRelationForTest(t *testing.T, db *gorm.DB, sourceID, targetID uuid.UUID, stance string, revisionID uuid.UUID) model.DebateRelation {
	t.Helper()
	var source model.Debate
	require.NoError(t, db.First(&source, "id = ?", sourceID).Error)
	relation := model.DebateRelation{SourceDebateID: sourceID, TargetDebateID: targetID, Stance: stance, TargetRevisionID: revisionID, SourceConclusionEventID: *source.CurrentConclusionEventID, Status: model.DebateRelationActive}
	require.NoError(t, db.Create(&relation).Error)
	return relation
}

func requireAppError(t *testing.T, err error, code string, status int) {
	t.Helper()
	require.Error(t, err)
	var appErr *apperr.AppError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, code, appErr.Code)
	require.Equal(t, status, appErr.HTTPStatus)
}

func seedDebateWithID(t *testing.T, ctx debateTestContext, id uuid.UUID, title string) DebateDTO {
	t.Helper()
	debate := model.Debate{Base: model.Base{ID: id}, UserID: ctx.owner.ID, Title: title, Content: title, Status: model.DebateStatusActive}
	require.NoError(t, ctx.db.Create(&debate).Error)
	snapshot, err := json.Marshal(DebateSnapshot{Title: title, Content: title, Tags: []string{}})
	require.NoError(t, err)
	revision := model.Revision{ContentType: debateContentType, ContentID: debate.ID, VersionNumber: 1, ContentSnapshot: snapshot, EditorID: ctx.owner.ID, EditSummary: "Created", EditType: "creation", Status: "approved", IsCurrent: true}
	require.NoError(t, ctx.db.Create(&revision).Error)
	require.NoError(t, ctx.db.Model(&debate).Update("current_revision_id", revision.ID).Error)
	debate.CurrentRevisionID = &revision.ID
	return DebateDTO{Debate: debate}
}

func captureLockedDebateIDs(t *testing.T, db *gorm.DB) *[]uuid.UUID {
	t.Helper()
	locked := []uuid.UUID{}
	callback := "test:capture_locked_debates:" + uuid.NewString()
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		if debate, ok := tx.Statement.Dest.(*model.Debate); ok && debate.ID != uuid.Nil {
			locked = append(locked, debate.ID)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	return &locked
}
