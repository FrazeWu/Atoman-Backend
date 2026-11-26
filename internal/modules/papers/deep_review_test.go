package papers

import (
	"context"
	"encoding/json"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A request may already have passed RequireAccess when an administrator
// revokes the invitation. The project transaction must not inherit that
// obsolete authorization.
func TestPaperLockedOperationsRecheckEligibility(t *testing.T) {
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	require.NoError(t, f.svc.RequireAccess(owner))
	require.NoError(t, f.db.Model(&model.PaperAccessGrant{}).Where("user_id=?", owner.ID).Update("status", "revoked").Error)
	for _, check := range []func(*gorm.DB) error{
		func(tx *gorm.DB) error { _, err := f.svc.lockOwned(tx, owner, f.project.ID); return err },
		func(tx *gorm.DB) error { _, err := f.svc.lockAccessible(tx, owner, f.project.ID, false); return err },
		func(tx *gorm.DB) error { _, err := f.svc.lockAccessible(tx, owner, f.project.ID, true); return err },
	} {
		err := f.db.Transaction(check)
		require.Equal(t, 403, apperr.FromError(err).HTTPStatus)
	}
	require.NoError(t, f.db.Model(&model.PaperAccessGrant{}).Where("user_id=?", owner.ID).Update("status", "active").Error)
	require.NoError(t, f.db.Model(&model.User{}).Where("uuid=?", owner.ID).Update("is_active", false).Error)
	_, err := f.svc.GetFile(owner, f.project.ID, f.project.MainFileID)
	require.Equal(t, 403, apperr.FromError(err).HTTPStatus)
	_, err = f.svc.ListProjects(owner)
	require.Equal(t, 403, apperr.FromError(err).HTTPStatus)
	_, err = f.svc.ListTrashedProjects(owner)
	require.Equal(t, 403, apperr.FromError(err).HTTPStatus)
	_, err = f.svc.CreateProject(owner, "Disabled account")
	require.Equal(t, 403, apperr.FromError(err).HTTPStatus)
}

func TestPaperFileReadCarriesConsistentProjectRevision(t *testing.T) {
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	before, err := f.svc.GetFile(owner, f.project.ID, f.project.MainFileID)
	require.NoError(t, err)
	require.Equal(t, f.project.HeadRevision, before.ProjectHeadRevision)
	_, err = f.svc.CreateFile(owner, f.project.ID, "other.tex", "Another file")
	require.NoError(t, err)
	after, err := f.svc.GetFile(owner, f.project.ID, f.project.MainFileID)
	require.NoError(t, err)
	require.Equal(t, before.Revision, after.Revision)
	require.Equal(t, before.ProjectHeadRevision+1, after.ProjectHeadRevision)
	rows, err := f.svc.ListFiles(owner, f.project.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.Equal(t, after.ProjectHeadRevision, row.ProjectHeadRevision)
		require.Empty(t, row.Content)
	}
}

func TestPaperAISingleFileAndContextChangesInvalidateResponse(t *testing.T) {
	t.Setenv("PAPERS_AI_ENABLE", "true")
	for _, mode := range []string{"revise", "explain"} {
		for _, change := range []string{"target", "context", "unrelated"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				f := newCompileHTTPFixture(t, false)
				owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
				active, err := f.svc.GetFile(owner, f.project.ID, f.project.MainFileID)
				require.NoError(t, err)
				extra, err := f.svc.CreateFile(owner, f.project.ID, "refs.bib", "Approved reference")
				require.NoError(t, err)
				unrelated, err := f.svc.CreateFile(owner, f.project.ID, "unrelated.tex", "Other source")
				require.NoError(t, err)
				target := map[string]model.PaperFile{"target": active, "context": extra, "unrelated": unrelated}[change]
				calls := 0
				f.svc.aiCall = func(_ context.Context, _, _, _ string, _ paperAIPrompt) (paperAISuggestion, error) {
					calls++
					_, err := f.svc.SaveFile(owner, f.project.ID, target.ID, "New version", target.Revision)
					require.NoError(t, err)
					return paperAISuggestion{Text: "Synthetic suggestion"}, nil
				}
				in := paperAIInput{BaseURL: paperAIDefaultBase, Model: "test-model", APIKey: "synthetic-fixture-key", FileID: active.ID, BaseRevision: active.Revision, Source: "Local selected text", Task: mode, Instruction: "Review", Context: []paperAIContextRef{{FileID: extra.ID, Revision: extra.Revision}}, Consent: true}
				raw, err := json.Marshal(in)
				require.NoError(t, err)
				result := f.request("POST", f.base()+"/ai/suggestions", string(raw), f.ownerToken)
				if change == "unrelated" {
					require.Equal(t, 200, result.Code, result.Body.String())
				} else {
					require.Equal(t, 409, result.Code, result.Body.String())
					require.NotContains(t, result.Body.String(), "Synthetic suggestion")
					var count int64
					require.NoError(t, f.db.Model(&model.AuditLog{}).Where("entity_id=? AND action=?", f.project.ID, aiProposed).Count(&count).Error)
					require.Zero(t, count)
				}
				require.Equal(t, 1, calls)
			})
		}
	}
}

func TestPaperAINeverSendsCredentialInFilePath(t *testing.T) {
	t.Setenv("PAPERS_AI_ENABLE", "true")
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	const key = "synthetic-fixture-key"
	file, err := f.svc.CreateFile(owner, f.project.ID, key+".tex", "Source")
	require.NoError(t, err)
	active, err := f.svc.GetFile(owner, f.project.ID, f.project.MainFileID)
	require.NoError(t, err)
	calls := 0
	f.svc.aiCall = func(_ context.Context, _, _, _ string, _ paperAIPrompt) (paperAISuggestion, error) {
		calls++
		return paperAISuggestion{Text: "unexpected"}, nil
	}
	in := paperAIInput{BaseURL: paperAIDefaultBase, Model: "test-model", APIKey: key, FileID: file.ID, BaseRevision: file.Revision, Source: file.Content, Task: "revise", Instruction: "Review", Consent: true}
	for _, input := range []paperAIInput{in, {BaseURL: paperAIDefaultBase, Model: "test-model", APIKey: key, FileID: active.ID, BaseRevision: active.Revision, Source: active.Content, Task: "explain", Instruction: "Review", Consent: true, Context: []paperAIContextRef{{FileID: file.ID, Revision: file.Revision}}}} {
		raw, err := json.Marshal(input)
		require.NoError(t, err)
		result := f.request("POST", f.base()+"/ai/suggestions", string(raw), f.ownerToken)
		require.Equal(t, 400, result.Code, result.Body.String())
		require.NotContains(t, result.Body.String(), key)
	}
	require.Zero(t, calls)
}
