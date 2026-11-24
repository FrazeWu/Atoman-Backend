package papers

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestManagedFileLifecycle(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	base := f.base() + "/files/manage"
	send := func(in FileChange, token string, expected int) ManagedProject {
		data, err := json.Marshal(in)
		require.NoError(t, err)
		res := f.request("POST", base, string(data), token)
		require.Equal(t, expected, res.Code, res.Body.String())
		var v struct {
			Data ManagedProject `json:"data"`
		}
		if expected == 200 {
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &v))
		}
		return v.Data
	}
	upload := FileChange{Action: "upload", BaseRevision: 1, Uploads: []FileUpload{
		{Path: "sections/intro.tex", Kind: "text", Content: "A section"},
		{Path: "figures/chart.png", Kind: "binary", Content: base64.StdEncoding.EncodeToString([]byte{137, 80, 78, 71, 13, 10, 26, 10})},
	}}
	send(upload, f.otherToken, http.StatusNotFound)
	r := send(upload, f.ownerToken, 200)
	require.Equal(t, int64(2), r.Project.HeadRevision)
	require.Len(t, r.Files, 3)
	var chapter, figure uuid.UUID
	for _, a := range r.Files {
		require.Empty(t, a.Content)
		if a.Path == "sections/intro.tex" {
			chapter = a.ID
		}
		if a.Path == "figures/chart.png" {
			figure = a.ID
		}
	}
	require.NotEqual(t, uuid.Nil, chapter)
	require.NotEqual(t, uuid.Nil, figure)
	send(upload, f.ownerToken, 409)
	send(FileChange{Action: "upload", BaseRevision: 2, Uploads: []FileUpload{{Path: "valid.tex", Kind: "text", Content: "ok"}, {Path: "FIGURES/chart.PNG", Kind: "binary", Content: "AQ=="}}}, f.ownerToken, 409)
	send(FileChange{Action: "rename", BaseRevision: 2, FileID: chapter, Path: "../escape.tex"}, f.ownerToken, 400)
	send(FileChange{Action: "delete", BaseRevision: 2, FileID: f.project.MainFileID}, f.ownerToken, 409)
	r = send(FileChange{Action: "rename", BaseRevision: 2, FileID: chapter, Path: "sections/method.tex"}, f.ownerToken, 200)
	require.Equal(t, int64(3), r.Project.HeadRevision)
	var moved model.PaperFile
	require.NoError(t, f.db.First(&moved, "id=?", chapter).Error)
	require.Equal(t, "sections/method.tex", moved.Path)
	require.Equal(t, int64(2), moved.Revision)
	var count int64
	require.NoError(t, f.db.Model(&model.PaperFileVersion{}).Where("file_id=?", chapter).Count(&count).Error)
	require.Equal(t, int64(2), count)
	r = send(FileChange{Action: "rename", BaseRevision: 3, FileID: f.project.MainFileID, Path: "paper.tex"}, f.ownerToken, 200)
	require.Equal(t, int64(4), r.Project.HeadRevision)
	r = send(FileChange{Action: "upload", BaseRevision: 4, Uploads: []FileUpload{{Path: "supplement.tex", Kind: "text", Content: "Supplement"}}}, f.ownerToken, 200)
	var supplement uuid.UUID
	for _, a := range r.Files {
		if a.Path == "supplement.tex" {
			supplement = a.ID
		}
	}
	r = send(FileChange{Action: "set_main", BaseRevision: 5, FileID: supplement}, f.ownerToken, 200)
	require.Equal(t, supplement, r.Project.MainFileID)
	r = send(FileChange{Action: "delete", BaseRevision: 6, FileID: figure}, f.ownerToken, 200)
	require.Len(t, r.Files, 3)
	require.Equal(t, 404, f.request("GET", f.base()+"/files/"+figure.String(), "", f.ownerToken).Code)
	snap, err := f.svc.ownedSnapshot(t.Context(), authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}, f.project.ID, 7)
	require.NoError(t, err)
	require.NoError(t, snap.Validate())
}
func TestManagedProjectTrashRestore(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	require.Equal(t, 204, f.request("DELETE", f.base(), "", f.ownerToken).Code)
	require.Equal(t, 404, f.request("GET", f.base(), "", f.ownerToken).Code)
	require.Equal(t, 404, f.request("POST", f.base()+"/restore", "", f.otherToken).Code)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	rows, err := f.svc.ListTrashedProjects(owner)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, f.project.ID, rows[0].ID)
	require.Equal(t, 200, f.request("POST", f.base()+"/restore", "", f.ownerToken).Code)
	require.Equal(t, 200, f.request("GET", f.base(), "", f.ownerToken).Code)
	rows, err = f.svc.ListTrashedProjects(owner)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Equal(t, 404, f.request("POST", f.base()+"/restore", "", f.ownerToken).Code)
}
