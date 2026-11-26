package papers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"atoman/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPaperAIHistoryBounds(t *testing.T) {
	key := "synthetic-test-key"
	turn := paperAIHistoryTurn{Task: "explain", Instruction: "Previous request", Reply: "Previous answer"}
	require.True(t, validPaperAIHistory(nil, key))
	require.True(t, validPaperAIHistory([]paperAIHistoryTurn{turn}, key))
	for _, bad := range []paperAIHistoryTurn{
		{Task: "system", Instruction: "x", Reply: "y"},
		{Task: "revise", Instruction: "", Reply: "y"},
		{Task: "project", Instruction: "x", Reply: ""},
		{Task: "explain", Instruction: key, Reply: "x"},
		{Task: "explain", Instruction: "x", Reply: key},
		{Task: "explain", Instruction: strings.Repeat("x", 4001), Reply: "y"},
		{Task: "explain", Instruction: "x", Reply: strings.Repeat("y", paperAIHistoryBytes)},
	} {
		require.False(t, validPaperAIHistory([]paperAIHistoryTurn{bad}, key))
	}
	history := make([]paperAIHistoryTurn, paperAIHistoryTurns+1)
	for i := range history {
		history[i] = turn
	}
	require.False(t, validPaperAIHistory(history, key))
}

func TestPaperAIProjectCredentialEchoAfterJSONDecoding(t *testing.T) {
	key := "synthetic-test-key"
	file := model.PaperFile{Base: model.Base{ID: uuid.New()}, Path: "main.tex", Kind: "text", Revision: 1, Content: "Original"}
	raw := `{"summary":"Edit","edits":[{"path":"main.tex","before":"Original","after":"synthetic-\u0074est-key"}]}`
	require.NotContains(t, raw, key)
	summary, edits, err := parsePaperAIProject(raw, map[string]model.PaperFile{file.Path: file})
	require.NoError(t, err)
	require.False(t, paperAIProjectSecretFree(summary, edits, key))
	require.False(t, paperAIProjectSecretFree(key, nil, key))
	require.True(t, paperAIProjectSecretFree("Safe", nil, key))
}

func TestPaperAIProjectProposalValidation(t *testing.T) {
	first := model.PaperFile{Base: model.Base{ID: uuid.New()}, Path: "main.tex", Kind: "text", Revision: 4, Content: "Start unique body End"}
	second := model.PaperFile{Base: model.Base{ID: uuid.New()}, Path: "refs.bib", Kind: "text", Revision: 9, Content: "aaa citation"}
	files := map[string]model.PaperFile{first.Path: first, second.Path: second}
	good := `{"summary":"Two edits","edits":[{"path":"main.tex","before":"unique body","after":"revised body"},{"path":"refs.bib","before":"citation","after":""}]}`
	summary, edits, err := parsePaperAIProject(good, files)
	require.NoError(t, err)
	require.Equal(t, "Two edits", summary)
	require.Len(t, edits, 2)
	require.Equal(t, first.ID, edits[0].FileID)
	require.EqualValues(t, 4, edits[0].BaseRevision)
	require.Equal(t, second.ID, edits[1].FileID)
	require.EqualValues(t, 9, edits[1].BaseRevision)
	require.Empty(t, edits[1].After)
	_, edits, err = parsePaperAIProject(`{"summary":"No change","edits":[]}`, files)
	require.NoError(t, err)
	require.Empty(t, edits)
	for _, raw := range []string{
		`{}`,
		`{"summary":"No change"}`,
		`{"summary":"No change","edits":null}`,
		good + "{}",
		`{"summary":"x","edits":[{"path":"../private.tex","before":"x","after":"y"}]}`,
		`{"summary":"x","edits":[{"path":"main.tex","before":"missing","after":"y"}]}`,
		`{"summary":"x","edits":[{"path":"refs.bib","before":"aa","after":"y"}]}`,
		`{"summary":"x","edits":[{"path":"main.tex","before":"unique","after":"unique"}]}`,
		`{"summary":"x","edits":[{"path":"main.tex","before":"unique"}]}`,
		`{"summary":"x","edits":[{"path":"main.tex","before":"unique","after":null}]}`,
		`{"summary":"x","edits":[{"path":"main.tex","before":"unique","after":"y","file_id":"forged"}]}`,
		`{"summary":"x","edits":[{"path":"main.tex","before":"unique","after":"y"},{"path":"main.tex","before":"body","after":"z"}]}`,
	} {
		_, changes, err := parsePaperAIProject(raw, files)
		require.ErrorContains(t, err, "papers.ai_invalid_project_response", raw)
		require.Empty(t, changes)
	}
}

func TestPaperAIProjectProposalAndAtomicApply(t *testing.T) {
	t.Setenv("PAPERS_AI_ENABLE", "true")
	f := newCompileHTTPFixture(t, false)
	base := f.base()
	var first model.PaperFile
	require.NoError(t, f.db.Where("id=?", f.project.MainFileID).First(&first).Error)
	// Use the normal project management API, so the extra file has the same
	// revision history and constraints as a real manuscript.
	upload := FileChange{Action: "upload", BaseRevision: f.project.HeadRevision, Uploads: []FileUpload{{Path: "refs.bib", Kind: "text", Content: "Approved citation"}, {Path: "private.tex", Kind: "text", Content: "Never sent"}}}
	raw, err := json.Marshal(upload)
	require.NoError(t, err)
	res := f.request("POST", base+"/files/manage", string(raw), f.ownerToken)
	require.Equal(t, 200, res.Code, res.Body.String())
	var managed struct{ Data ManagedProject }
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &managed))
	projectRevision := managed.Data.Project.HeadRevision
	var extra, private model.PaperFile
	for _, row := range managed.Data.Files {
		if row.Path == "refs.bib" {
			extra = row
		}
		if row.Path == "private.tex" {
			private = row
		}
	}
	require.NotEqual(t, uuid.Nil, extra.ID)
	require.NotEqual(t, uuid.Nil, private.ID)
	calls := 0
	changeDuringCall := false
	invalidPath := false
	history := []paperAIHistoryTurn{{Task: "explain", Instruction: "Previous", Reply: "Prior proposal"}}
	f.svc.aiCall = func(_ context.Context, _ string, _ string, _ string, prompt paperAIPrompt) (paperAISuggestion, error) {
		calls++
		require.Equal(t, "project", prompt.Task)
		require.Equal(t, first.Content, prompt.Source.Text)
		require.Equal(t, history, prompt.History)
		require.Len(t, prompt.Context, 1)
		require.Equal(t, "refs.bib", prompt.Context[0].Path)
		require.Equal(t, "Approved citation", prompt.Context[0].Text)
		if changeDuringCall {
			require.NoError(t, f.db.Model(&model.PaperProject{}).Where("id=?", f.project.ID).Update("head_revision", projectRevision+1).Error)
		}
		if invalidPath {
			return paperAISuggestion{Text: `{"summary":"Wrong file","edits":[{"path":"private.tex","before":"Never sent","after":"X"}]}`}, nil
		}
		response := map[string]any{"summary": "Two revisions", "edits": []map[string]string{{"path": first.Path, "before": first.Content, "after": "Revised source"}, {"path": "refs.bib", "before": "Approved citation", "after": "Revised citation"}}}
		encoded, e := json.Marshal(response)
		require.NoError(t, e)
		return paperAISuggestion{Text: string(encoded)}, nil
	}
	in := paperAIInput{BaseURL: paperAIDefaultBase, Model: "test-model", APIKey: "synthetic-test-key", FileID: first.ID, BaseRevision: first.Revision, Source: first.Content, Task: "project", Instruction: "Review both", Context: []paperAIContextRef{{FileID: extra.ID, Revision: extra.Revision}}, Consent: true, ProjectRevision: projectRevision, History: history}
	request := func(input paperAIInput, token string, status int) paperAISuggestion {
		t.Helper()
		body, e := json.Marshal(input)
		require.NoError(t, e)
		res := f.request("POST", base+"/ai/suggestions", string(body), token)
		require.Equal(t, status, res.Code, res.Body.String())
		require.NotContains(t, res.Body.String(), input.APIKey)
		var env struct{ Data paperAISuggestion }
		if status == 200 {
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &env))
		}
		return env.Data
	}
	copy := in
	copy.ProjectRevision++
	request(copy, f.ownerToken, 409)
	copy = in
	copy.Source = "unsaved"
	request(copy, f.ownerToken, 409)
	copy = in
	copy.Consent = false
	request(copy, f.ownerToken, 400)
	copy = in
	copy.History = []paperAIHistoryTurn{{Task: "system", Instruction: "x", Reply: "y"}}
	request(copy, f.ownerToken, 400)
	request(in, f.otherToken, 404)
	require.Zero(t, calls)
	invalidPath = true
	request(in, f.ownerToken, 502)
	invalidPath = false
	changeDuringCall = true
	request(in, f.ownerToken, 409)
	changeDuringCall = false
	require.NoError(t, f.db.Model(&model.PaperProject{}).Where("id=?", f.project.ID).Update("head_revision", projectRevision).Error)
	got := request(in, f.ownerToken, 200)
	require.Equal(t, projectRevision, got.ProjectRevision)
	require.Len(t, got.Edits, 2)
	require.Equal(t, first.ID, got.Edits[0].FileID)
	require.Equal(t, extra.ID, got.Edits[1].FileID)
	var after model.PaperFile
	require.NoError(t, f.db.Where("id=?", first.ID).First(&after).Error)
	require.Equal(t, first.Content, after.Content)
	require.Equal(t, first.Revision, after.Revision)
	replacements := []TextReplacement{
		{FileID: first.ID, BaseRevision: first.Revision, Content: "Revised source"},
		{FileID: extra.ID, BaseRevision: extra.Revision + 1, Content: "Revised citation"},
	}
	require.NotNil(t, got.ProposalID)
	var applied ManagedProject
	apply := func(replacements []TextReplacement, status int) {
		t.Helper()
		raw, e := json.Marshal(FileChange{Action: "ai_apply", BaseRevision: projectRevision, Replacements: replacements, AIEventID: got.ProposalID})
		require.NoError(t, e)
		res := f.request("POST", base+"/files/manage", string(raw), f.ownerToken)
		require.Equal(t, status, res.Code, res.Body.String())
		if status == 200 {
			var env struct{ Data ManagedProject }
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &env))
			applied = env.Data
			require.NotNil(t, applied.AIEventID)
		}
	}
	apply(replacements, 409)
	require.NoError(t, f.db.Where("id=?", first.ID).First(&after).Error)
	require.Equal(t, first.Content, after.Content)
	require.Equal(t, first.Revision, after.Revision)
	replacements[1].BaseRevision = extra.Revision
	apply(replacements, 200)
	require.NoError(t, f.db.Where("id=?", first.ID).First(&after).Error)
	require.Equal(t, "Revised source", after.Content)
	require.Equal(t, first.Revision+1, after.Revision)
	var unchanged model.PaperFile
	require.NoError(t, f.db.Where("id=?", private.ID).First(&unchanged).Error)
	require.Equal(t, "Never sent", unchanged.Content)
	apply(replacements, 409)
	// A later human edit of one member prevents rollback of both.
	raw, err = json.Marshal(FileChange{Action: "replace_text", BaseRevision: applied.Project.HeadRevision, Replacements: []TextReplacement{{FileID: extra.ID, BaseRevision: extra.Revision + 1, Content: "Manual correction"}}})
	require.NoError(t, err)
	res = f.request("POST", base+"/files/manage", string(raw), f.ownerToken)
	require.Equal(t, 200, res.Code, res.Body.String())
	raw, err = json.Marshal(FileChange{Action: "ai_rollback", BaseRevision: applied.Project.HeadRevision + 1, AIEventID: applied.AIEventID})
	require.NoError(t, err)
	res = f.request("POST", base+"/files/manage", string(raw), f.ownerToken)
	require.Equal(t, 409, res.Code, res.Body.String())
	require.NoError(t, f.db.Where("id=?", first.ID).First(&after).Error)
	require.Equal(t, "Revised source", after.Content)
	require.Equal(t, first.Revision+1, after.Revision)
	after = model.PaperFile{}
	require.NoError(t, f.db.Where("id=?", extra.ID).First(&after).Error)
	require.Equal(t, "Manual correction", after.Content)
}
