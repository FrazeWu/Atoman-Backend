package papers

import (
	"context"
	"encoding/json"
	"testing"

	"atoman/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPaperAIAuditAndRollback(t *testing.T) {
	t.Setenv("PAPERS_AI_ENABLE", "true")
	f := newCompileHTTPFixture(t, false)
	base := f.base()
	var first model.PaperFile
	require.NoError(t, f.db.Where("id=?", f.project.MainFileID).First(&first).Error)
	const secret = "synthetic-audit-key"
	const instruction = "private instruction must not persist"
	const summary = "private model answer must not persist"
	const replacement = "Private accepted replacement"
	fail := false
	f.svc.aiCall = func(_ context.Context, _ string, _ string, _ string, prompt paperAIPrompt) (paperAISuggestion, error) {
		if fail {
			return paperAISuggestion{}, paperAIProviderError("papers.ai_unavailable")
		}
		body, err := json.Marshal(map[string]any{"summary": summary, "edits": []map[string]string{{"path": first.Path, "before": prompt.Source.Text, "after": replacement}}})
		require.NoError(t, err)
		return paperAISuggestion{Text: string(body), InputTokens: 12, OutputTokens: 4}, nil
	}
	req := paperAIInput{BaseURL: paperAIDefaultBase, Model: "test-model", APIKey: secret, FileID: first.ID, BaseRevision: first.Revision, Source: first.Content, Task: "project", Instruction: instruction, Consent: true, ProjectRevision: f.project.HeadRevision}
	call := func(method, path string, input any, token string, status int) []byte {
		t.Helper()
		var raw []byte
		if input != nil {
			var err error
			raw, err = json.Marshal(input)
			require.NoError(t, err)
		}
		res := f.request(method, base+path, string(raw), token)
		require.Equal(t, status, res.Code, res.Body.String())
		require.NotContains(t, res.Body.String(), secret)
		return res.Body.Bytes()
	}
	activity := func() []paperAIActivity {
		var env struct{ Data []paperAIActivity }
		raw := call("GET", "/ai/activity", nil, f.ownerToken, 200)
		require.NotContains(t, string(raw), instruction)
		require.NotContains(t, string(raw), summary)
		require.NotContains(t, string(raw), replacement)
		require.NoError(t, json.Unmarshal(raw, &env))
		return env.Data
	}
	fail = true
	call("POST", "/ai/suggestions", req, f.ownerToken, 502)
	events := activity()
	require.Len(t, events, 2)
	require.Equal(t, aiFailed, events[0].Action)
	require.Equal(t, aiStarted, events[1].Action)
	require.Equal(t, events[0].AttemptID, events[1].AttemptID)
	fail = false
	var suggested struct{ Data paperAISuggestion }
	require.NoError(t, json.Unmarshal(call("POST", "/ai/suggestions", req, f.ownerToken, 200), &suggested))
	require.NotNil(t, suggested.Data.ProposalID)
	pid := suggested.Data.ProposalID
	events = activity()
	require.Len(t, events, 4)
	require.Equal(t, aiProposed, events[0].Action)
	require.Equal(t, *pid, events[0].ID)
	require.EqualValues(t, 12, events[0].InputTokens)
	require.Equal(t, events[0].AttemptID, events[1].AttemptID)
	require.Len(t, events[0].Files, 1)
	call("GET", "/ai/activity", nil, "", 401)
	call("GET", "/ai/activity", nil, f.otherToken, 404)
	for _, role := range []string{"viewer", "editor"} {
		call("PUT", "/members/"+f.other.UUID.String(), map[string]string{"role": role}, f.ownerToken, 200)
		call("GET", "/ai/activity", nil, f.otherToken, 404)
	}
	apply := FileChange{Action: "ai_apply", BaseRevision: req.ProjectRevision, AIEventID: pid, Replacements: []TextReplacement{{FileID: first.ID, BaseRevision: first.Revision, Content: replacement}}}
	tampered := apply
	tampered.Replacements = []TextReplacement{{FileID: first.ID, BaseRevision: first.Revision, Content: "forged"}}
	call("POST", "/files/manage", tampered, f.ownerToken, 409)
	random := uuid.New()
	tampered = apply
	tampered.AIEventID = &random
	call("POST", "/files/manage", tampered, f.ownerToken, 404)
	call("POST", "/files/manage", apply, f.otherToken, 404)
	require.Len(t, activity(), 4)
	var applied struct{ Data ManagedProject }
	require.NoError(t, json.Unmarshal(call("POST", "/files/manage", apply, f.ownerToken, 200), &applied))
	require.NotNil(t, applied.Data.AIEventID)
	require.Equal(t, req.ProjectRevision+1, applied.Data.Project.HeadRevision)
	var live model.PaperFile
	require.NoError(t, f.db.Where("id=?", first.ID).First(&live).Error)
	require.Equal(t, replacement, live.Content)
	require.Equal(t, first.Revision+1, live.Revision)
	events = activity()
	require.Len(t, events, 5)
	require.Equal(t, aiApplied, events[0].Action)
	require.Equal(t, *pid, events[0].RelatedID)
	require.Equal(t, *applied.Data.AIEventID, events[0].ID)
	require.Equal(t, paperAIIdentity(), events[0].Contributor)
	require.Equal(t, "owner_approved", events[0].Decision)
	historicalPath := func(event, file uuid.UUID) string {
		return "/ai/activity/" + event.String() + "/changes/" + file.String()
	}
	detailPath := historicalPath(*applied.Data.AIEventID, first.ID)
	var detail struct{ Data paperAIHistoricalChange }
	require.NoError(t, json.Unmarshal(call("GET", detailPath, nil, f.ownerToken, 200), &detail))
	require.Equal(t, first.Content, detail.Data.Before)
	require.Equal(t, replacement, detail.Data.After)
	require.Equal(t, first.Revision, detail.Data.BeforeRevision)
	require.Equal(t, first.Revision+1, detail.Data.AfterRevision)
	require.Equal(t, paperAIIdentity(), detail.Data.Contributor)
	require.Equal(t, "owner_approved", detail.Data.Decision)
	call("GET", detailPath, nil, "", 401)
	call("GET", detailPath, nil, f.otherToken, 404)
	call("GET", historicalPath(*pid, first.ID), nil, f.ownerToken, 404)
	call("GET", historicalPath(*applied.Data.AIEventID, uuid.New()), nil, f.ownerToken, 404)

	call("POST", "/files/manage", apply, f.ownerToken, 409)
	// Unrelated edits are retained by a targeted rollback.
	upload := FileChange{Action: "upload", BaseRevision: applied.Data.Project.HeadRevision, Uploads: []FileUpload{{Path: "unrelated.tex", Kind: "text", Content: "Unrelated user work"}}}
	var uploaded struct{ Data ManagedProject }
	require.NoError(t, json.Unmarshal(call("POST", "/files/manage", upload, f.ownerToken, 200), &uploaded))
	undo := FileChange{Action: "ai_rollback", BaseRevision: uploaded.Data.Project.HeadRevision, AIEventID: applied.Data.AIEventID}
	var reverted struct{ Data ManagedProject }
	require.NoError(t, json.Unmarshal(call("POST", "/files/manage", undo, f.ownerToken, 200), &reverted))
	require.NotNil(t, reverted.Data.AIEventID)
	require.NoError(t, f.db.Where("id=?", first.ID).First(&live).Error)
	require.Equal(t, first.Content, live.Content)
	require.Equal(t, first.Revision+2, live.Revision)
	var unrelated model.PaperFile
	require.NoError(t, f.db.Where("project_id=? AND path=?", f.project.ID, "unrelated.tex").First(&unrelated).Error)
	require.Equal(t, "Unrelated user work", unrelated.Content)
	undo.BaseRevision = reverted.Data.Project.HeadRevision
	call("POST", "/files/manage", undo, f.ownerToken, 409)
	events = activity()
	require.Len(t, events, 6)
	require.Equal(t, aiRolledBack, events[0].Action)
	require.Equal(t, *applied.Data.AIEventID, events[0].RelatedID)
	// Apply a second operation and prove a later edit cannot be rolled back.
	req.BaseRevision = live.Revision
	req.ProjectRevision = reverted.Data.Project.HeadRevision
	require.NoError(t, json.Unmarshal(call("POST", "/ai/suggestions", req, f.ownerToken, 200), &suggested))
	apply.BaseRevision = req.ProjectRevision
	apply.AIEventID = suggested.Data.ProposalID
	apply.Replacements[0].BaseRevision = req.BaseRevision
	require.NoError(t, json.Unmarshal(call("POST", "/files/manage", apply, f.ownerToken, 200), &applied))
	manual := FileChange{Action: "replace_text", BaseRevision: applied.Data.Project.HeadRevision, Replacements: []TextReplacement{{FileID: first.ID, BaseRevision: req.BaseRevision + 1, Content: "Later human correction"}}}
	var manually struct{ Data ManagedProject }
	require.NoError(t, json.Unmarshal(call("POST", "/files/manage", manual, f.ownerToken, 200), &manually))
	undo.BaseRevision = manually.Data.Project.HeadRevision
	undo.AIEventID = applied.Data.AIEventID
	call("POST", "/files/manage", undo, f.ownerToken, 409)
	require.NoError(t, f.db.Where("id=?", first.ID).First(&live).Error)
	require.Equal(t, "Later human correction", live.Content)
	// Historical display stays tied to immutable accepted versions.
	require.NoError(t, json.Unmarshal(call("GET", detailPath, nil, f.ownerToken, 200), &detail))
	require.Equal(t, first.Content, detail.Data.Before)
	require.Equal(t, replacement, detail.Data.After)
	require.NoError(t, f.db.Model(&model.PaperFileVersion{}).Where("file_id=? AND revision=?", first.ID, first.Revision).Update("content", "Synthetic corruption").Error)
	call("GET", detailPath, nil, f.ownerToken, 409)

	var logs []model.AuditLog
	require.NoError(t, paperAIEventQuery(f.db, f.owner.UUID, f.project.ID).Find(&logs).Error)
	for _, row := range logs {
		require.NotContains(t, row.Metadata, secret)
		require.NotContains(t, row.Metadata, instruction)
		require.NotContains(t, row.Metadata, summary)
		require.NotContains(t, row.Metadata, replacement)
		require.Empty(t, row.Reason)
	}
}
