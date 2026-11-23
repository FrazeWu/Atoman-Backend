package debate

import (
	"net/http"
	"testing"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newStructuredDebateContext(t *testing.T) debateTestContext {
	t.Helper()
	ctx := newDebateTestContext(t)
	require.NoError(t, ctx.db.AutoMigrate(
		&model.DebateEntry{},
		&model.DebateEvidenceVote{},
		&model.DebateChallenge{},
		&model.DebateChallengeVote{},
		&model.SiteSetting{},
	))
	require.NoError(t, migrations.RunDebateStructuredMigration(ctx.db))
	return ctx
}

func seedReferenceChannel(t *testing.T, ctx debateTestContext, name string) model.Channel {
	t.Helper()
	ch := model.Channel{
		Name: name,
		Slug: "ch-" + uuid.NewString(),
	}
	require.NoError(t, ctx.db.Create(&ch).Error)
	return ch
}

func seedReferencePerson(t *testing.T, ctx debateTestContext, name string) model.TimelinePerson {
	t.Helper()
	person := model.TimelinePerson{
		UserID:   ctx.owner.ID,
		Name:     name,
		IsPublic: true,
	}
	require.NoError(t, ctx.db.Create(&person).Error)
	return person
}

func TestStructuredEntriesThreeLevelValidationAndReferences(t *testing.T) {
	ctx := newStructuredDebateContext(t)
	topic := createDebateForTest(t, ctx, "Main Topic", "Background overview")
	otherDebate := createDebateForTest(t, ctx, "Related Debate", "Background source")
	person := seedReferencePerson(t, ctx, "Karl Popper")

	// Claim requires title, valid support/oppose stance, and no parent_id.
	otherID := uuid.New()
	_, err := ctx.service.CreateEntry(ctx.owner, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeClaim,
		Stance:    model.DebateEntryStanceSupport,
		Content:   "Missing title",
	})
	requireAppError(t, err, "debate.entry_invalid", 400)

	_, err = ctx.service.CreateEntry(ctx.owner, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeClaim,
		Stance:    model.DebateEntryStanceSupport,
		Title:     "Bad parent",
		Content:   "Claim with parent",
		ParentID:  &otherID,
	})
	requireAppError(t, err, "debate.entry_invalid", 400)

	claim, err := ctx.service.CreateEntry(ctx.owner, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeClaim,
		Stance:    model.DebateEntryStanceSupport,
		Title:     "Core falsifiability argument",
		Content:   "Detailed reasoning",
	})
	require.NoError(t, err)
	require.Equal(t, model.DebateEntryTypeClaim, claim.EntryType)
	require.Equal(t, model.DebateEntryStanceSupport, claim.Stance)
	require.Equal(t, model.DebateEntryStatusActive, claim.Status)

	// Evidence requires >= 1 internal reference and forbids debate relation qualifiers.
	_, err = ctx.service.CreateEntry(ctx.editor, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeEvidence,
		ParentID:  &claim.ID,
		Content:   "Unsupported claim text with https://example.com only",
	})
	requireAppError(t, err, "debate.evidence_reference_required", 400)

	_, err = ctx.service.CreateEntry(ctx.editor, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeEvidence,
		ParentID:  &claim.ID,
		Content:   "@debate:" + otherDebate.ID.String() + ":support",
	})
	requireAppError(t, err, "debate.reference_invalid", 400)

	// Valid evidence under claim with inherited stance & resolved internal references.
	evidence, err := ctx.service.CreateEntry(ctx.editor, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeEvidence,
		ParentID:  &claim.ID,
		Content:   "See @person:" + person.ID.String() + " and @debate:" + otherDebate.ID.String(),
	})
	require.NoError(t, err)
	require.Equal(t, model.DebateEntryStanceSupport, evidence.Stance)
	require.Len(t, evidence.References, 2)

	// Rebuttal against claim or evidence automatically flips default stance; targeting rebuttal hits depth limit.
	rebuttal, err := ctx.service.CreateEntry(ctx.owner, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeRebuttal,
		ParentID:  &evidence.ID,
		Content:   "Alternative interpretation",
	})
	require.NoError(t, err)
	require.Equal(t, model.DebateEntryStanceOppose, rebuttal.Stance)

	_, err = ctx.service.CreateEntry(ctx.editor, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeRebuttal,
		ParentID:  &rebuttal.ID,
		Content:   "Level 4 reply",
	})
	requireAppError(t, err, "debate.entry_depth_exceeded", 400)

	// Rejected entries cannot be edited or targeted.
	require.NoError(t, ctx.db.Model(&model.DebateEntry{}).Where("id = ?", evidence.ID).Update("status", model.DebateEntryStatusRejected).Error)
	_, err = ctx.service.UpdateEntry(ctx.editor, topic.ID, evidence.ID, UpdateDebateEntryRequest{
		Content: "@person:" + person.ID.String() + " edited",
	})
	requireAppError(t, err, "debate.entry_rejected", 409)
	_, err = ctx.service.CreateEntry(ctx.owner, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeRebuttal,
		ParentID:  &evidence.ID,
		Content:   "Reply to rejected evidence",
	})
	requireAppError(t, err, "debate.entry_rejected", 409)
}

func TestEntryEditPermissionsInheritedRefsCascadeDeleteAndArchive(t *testing.T) {
	ctx := newStructuredDebateContext(t)
	topic := createDebateForTest(t, ctx, "Editable Topic", "Body")
	person := seedReferencePerson(t, ctx, "Ada Lovelace")
	channel := seedReferenceChannel(t, ctx, "Primary Archive")

	claim, err := ctx.service.CreateEntry(ctx.owner, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeClaim,
		Stance:    model.DebateEntryStanceSupport,
		Title:     "Primary claim",
		Content:   "Claim details",
	})
	require.NoError(t, err)

	ev, err := ctx.service.CreateEntry(ctx.editor, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeEvidence,
		ParentID:  &claim.ID,
		Content:   "Source @person:" + person.ID.String(),
	})
	require.NoError(t, err)

	rebuttal, err := ctx.service.CreateEntry(ctx.owner, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeRebuttal,
		ParentID:  &ev.ID,
		Content:   "Rebuttal text",
	})
	require.NoError(t, err)

	// Open challenge on evidence will be dismissed when deleting parent claim.
	challenge, err := ctx.service.CreateChallenge(ctx.owner, topic.ID, CreateChallengeRequest{
		TargetType: model.DebateChallengeTargetEntry,
		TargetID:   ev.ID,
		ReasonCode: model.DebateChallengeReasonMismatch,
		Rationale:  "Check citation accuracy",
	})
	require.NoError(t, err)

	// Other regular user cannot edit editor's entry.
	nonOwner := authctx.CurrentUser{ID: uuid.New(), Username: "outsider", Role: authctx.RoleUser}
	require.NoError(t, ctx.db.Create(&model.User{UUID: nonOwner.ID, Username: nonOwner.Username, Email: "outsider@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}).Error)
	_, err = ctx.service.UpdateEntry(nonOwner, topic.ID, ev.ID, UpdateDebateEntryRequest{
		Content: "Source @channel:" + channel.ID.String(),
	})
	requireAppError(t, err, "debate.entry_forbidden", 403)

	// If person later becomes non-public, owner keeping existing reference can still edit, while new unavailable ref fails.
	require.NoError(t, ctx.db.Model(&model.TimelinePerson{}).Where("id = ?", person.ID).Update("is_public", false).Error)
	updated, err := ctx.service.UpdateEntry(ctx.editor, topic.ID, ev.ID, UpdateDebateEntryRequest{
		Content: "Updated wording keeping @person:" + person.ID.String(),
	})
	require.NoError(t, err)
	require.Contains(t, updated.Content, "Updated wording")

	unavailablePerson := model.TimelinePerson{UserID: ctx.owner.ID, Name: "Hidden"}
	require.NoError(t, ctx.db.Create(&unavailablePerson).Error)
	require.NoError(t, ctx.db.Model(&model.TimelinePerson{}).Where("id = ?", unavailablePerson.ID).Update("is_public", false).Error)
	_, err = ctx.service.UpdateEntry(ctx.editor, topic.ID, ev.ID, UpdateDebateEntryRequest{
		Content: "Switch to unavailable @person:" + unavailablePerson.ID.String(),
	})
	requireAppError(t, err, "debate.reference_unavailable", 400)

	// Admin deletes top-level claim -> soft-deletes child evidence & grandchild rebuttal and dismisses open challenge.
	require.NoError(t, ctx.service.DeleteEntry(ctx.admin, topic.ID, claim.ID))
	entries, err := ctx.service.ListEntries(topic.ID)
	require.NoError(t, err)
	require.Empty(t, entries)

	var remainingRebuttalCount int64
	require.NoError(t, ctx.db.Model(&model.DebateEntry{}).Where("id = ?", rebuttal.ID).Count(&remainingRebuttalCount).Error)
	require.Zero(t, remainingRebuttalCount)

	var ch model.DebateChallenge
	require.NoError(t, ctx.db.First(&ch, "id = ?", challenge.ID).Error)
	require.Equal(t, model.DebateChallengeStatusDismissed, ch.Status)

	// Archived debate blocks entry creation.
	require.NoError(t, ctx.db.Model(&model.Debate{}).Where("id = ?", topic.ID).Update("status", model.DebateStatusArchived).Error)
	_, err = ctx.service.CreateEntry(ctx.owner, topic.ID, CreateDebateEntryRequest{
		EntryType: model.DebateEntryTypeClaim,
		Stance:    model.DebateEntryStanceSupport,
		Title:     "Late claim",
		Content:   "Should fail",
	})
	requireAppError(t, err, "debate.archived", 409)
}

func TestStructuredEntriesHTTPFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := newStructuredDebateContext(t)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		switch c.GetHeader("X-Test-User") {
		case "owner":
			authctx.SetCurrentUser(c, ctx.owner)
		case "admin":
			authctx.SetCurrentUser(c, ctx.admin)
		}
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1"), ctx.service)

	topic := createDebateForTest(t, ctx, "HTTP Debate", "Body")
	res := performDebateRequest(t, router, http.MethodPost, "/api/v1/debate/topics/"+topic.ID.String()+"/entries", map[string]any{
		"entry_type": "claim",
		"stance":     "support",
		"title":      "HTTP Claim",
		"content":    "HTTP Body",
	}, "owner")
	require.Equal(t, http.StatusCreated, res.Code, res.Body.String())
	assertDebateDataEnvelope(t, res)

	listRes := performDebateRequest(t, router, http.MethodGet, "/api/v1/debate/topics/"+topic.ID.String()+"/entries", nil, "")
	require.Equal(t, http.StatusOK, listRes.Code, listRes.Body.String())
	assertDebateDataEnvelope(t, listRes)
}
