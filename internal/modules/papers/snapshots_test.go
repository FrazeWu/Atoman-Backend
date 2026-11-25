package papers

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProjectSnapshotLifecycleAndAtomicRestore(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	pid := f.project.ID
	// Exercise real production's partial unique index, including swapped paths.
	require.NoError(t, f.db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_paper_files_project_active_path ON paper_files (project_id,path) WHERE deleted_at IS NULL").Error)
	original, err := f.svc.GetFile(owner, pid, f.project.MainFileID)
	require.NoError(t, err)
	png := base64.StdEncoding.EncodeToString([]byte{137, 80, 78, 71, 13, 10, 26, 10})
	r, err := f.svc.ManageFiles(owner, pid, FileChange{Action: "upload", BaseRevision: 1, Uploads: []FileUpload{
		{Path: "a.tex", Kind: "text", Content: "A private original"},
		{Path: "b.tex", Kind: "text", Content: "B original"},
		{Path: "figures/a.png", Kind: "binary", Content: png},
	}})
	require.NoError(t, err)
	var a, b, img uuid.UUID
	for _, file := range r.Files {
		switch file.Path {
		case "a.tex":
			a = file.ID
		case "b.tex":
			b = file.ID
		case "figures/a.png":
			img = file.ID
		}
	}
	root := f.base() + "/snapshots"
	require.Equal(t, 401, f.request("GET", root, "", "").Code)
	require.Equal(t, 404, f.request("GET", root, "", f.otherToken).Code)
	require.Equal(t, 404, f.request("POST", root, `{"name":"private","base_revision":2}`, f.otherToken).Code)
	require.Equal(t, 409, f.request("POST", root, `{"name":"stale","base_revision":1}`, f.ownerToken).Code)
	res := f.request("POST", root, `{"name":"Submission","base_revision":2}`, f.ownerToken)
	require.Equal(t, 201, res.Code, res.Body.String())
	require.Equal(t, "private, no-store", res.Header().Get("Cache-Control"))
	var envelope struct{ Data model.PaperProjectSnapshot }
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
	snap := envelope.Data
	require.Equal(t, 4, snap.FileCount)
	require.Equal(t, int64(2), snap.SourceRevision)
	require.NotContains(t, res.Body.String(), "A private original")
	detailURL := root + "/" + snap.ID.String()
	detail := f.request("GET", detailURL, "", f.ownerToken)
	require.Equal(t, 200, detail.Code, detail.Body.String())
	require.Contains(t, detail.Body.String(), "figures/a.png")
	require.NotContains(t, detail.Body.String(), "A private original")
	require.NotContains(t, detail.Body.String(), png)
	for _, method := range []string{"GET", "DELETE"} {
		require.Equal(t, 404, f.request(method, detailURL, "", f.otherToken).Code)
	}
	require.Equal(t, 404, f.request("POST", detailURL+"/restore", `{"base_revision":2}`, f.otherToken).Code)
	// Mutate source, swap two existing filenames, change main, delete an asset,
	// and add a replacement with the same path.
	saved, err := f.svc.SaveFile(owner, pid, original.ID, "changed main", 1)
	require.NoError(t, err)
	rev := saved.ProjectHeadRevision
	change := func(c FileChange) {
		c.BaseRevision = rev
		result, err := f.svc.ManageFiles(owner, pid, c)
		require.NoError(t, err)
		rev = result.Project.HeadRevision
	}
	change(FileChange{Action: "rename", FileID: a, Path: "tmp.tex"})
	change(FileChange{Action: "rename", FileID: b, Path: "a.tex"})
	change(FileChange{Action: "rename", FileID: a, Path: "b.tex"})
	// main.tex is reserved when switching to a different main file.
	change(FileChange{Action: "rename", FileID: original.ID, Path: "old-main.tex"})
	change(FileChange{Action: "set_main", FileID: b})
	change(FileChange{Action: "delete", FileID: img})
	change(FileChange{Action: "upload", Uploads: []FileUpload{{Path: "figures/a.png", Kind: "binary", Content: "AQ=="}, {Path: "new.tex", Kind: "text", Content: "new file"}}})
	require.Equal(t, 409, f.request("POST", detailURL+"/restore", `{"base_revision":2}`, f.ownerToken).Code)
	input, _ := json.Marshal(snapshotRestoreInput{BaseRevision: rev})
	res = f.request("POST", detailURL+"/restore", string(input), f.ownerToken)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	var restored struct{ Data ProjectSnapshotRestore }
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &restored))
	require.Equal(t, rev+1, restored.Data.Project.HeadRevision)
	require.Equal(t, original.ID, restored.Data.Project.MainFileID)
	require.True(t, restored.Data.Backup.Automatic)
	require.Equal(t, rev, restored.Data.Backup.SourceRevision)
	require.Len(t, restored.Data.Files, 4)
	for _, row := range restored.Data.Files {
		require.Empty(t, row.Content)
	}
	for fid, want := range map[uuid.UUID]string{original.ID: original.Content, a: "A private original", b: "B original", img: png} {
		row, err := f.svc.GetFile(owner, pid, fid)
		require.NoError(t, err)
		require.Equal(t, want, row.Content)
		require.Greater(t, row.Revision, int64(1))
	}
	ra, err := f.svc.GetFile(owner, pid, a)
	require.NoError(t, err)
	require.Equal(t, "a.tex", ra.Path)
	rb, err := f.svc.GetFile(owner, pid, b)
	require.NoError(t, err)
	require.Equal(t, "b.tex", rb.Path)
	_, err = f.svc.SaveFile(owner, pid, original.ID, "stale edit", 1)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	rows, err := f.svc.ListProjectSnapshots(owner, pid)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	// Restore the automatic checkpoint and get the pre-restore manuscript back.
	undo, err := f.svc.RestoreProjectSnapshot(owner, pid, restored.Data.Backup.ID, rev+1)
	require.NoError(t, err)
	require.Equal(t, b, undo.Project.MainFileID)
	require.Len(t, undo.Files, 5)
	require.Equal(t, rev+2, undo.Project.HeadRevision)
	require.Equal(t, 200, f.request("DELETE", detailURL, "", f.ownerToken).Code)
	require.Equal(t, 404, f.request("GET", detailURL, "", f.ownerToken).Code)
	deletedInput, _ := json.Marshal(snapshotRestoreInput{BaseRevision: rev + 2})
	require.Equal(t, 404, f.request("POST", detailURL+"/restore", string(deletedInput), f.ownerToken).Code)
}

func TestSnapshotRestoreFailsClosedOnMissingHistoryAndQuota(t *testing.T) {
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	pid := f.project.ID
	snap, err := f.svc.CreateProjectSnapshot(owner, pid, "Baseline", 1)
	require.NoError(t, err)
	saved, err := f.svc.SaveFile(owner, pid, f.project.MainFileID, "Current contents", 1)
	require.NoError(t, err)
	require.NoError(t, f.db.Where("file_id=? AND revision=?", f.project.MainFileID, 1).Delete(&model.PaperFileVersion{}).Error)
	_, err = f.svc.RestoreProjectSnapshot(owner, pid, snap.ID, 2)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	rows, err := f.svc.ListProjectSnapshots(owner, pid)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	current, err := f.svc.GetFile(owner, pid, saved.ID)
	require.NoError(t, err)
	require.Equal(t, saved.Content, current.Content)
	require.Equal(t, saved.Revision, current.Revision)
	p, err := f.svc.GetProject(owner, pid)
	require.NoError(t, err)
	require.Equal(t, int64(2), p.HeadRevision)
	for i := 1; i < maxProjectSnapshots; i++ {
		_, err = f.svc.CreateProjectSnapshot(owner, pid, "Checkpoint", 2)
		require.NoError(t, err)
	}
	_, err = f.svc.CreateProjectSnapshot(owner, pid, "Overflow", 2)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	rows, err = f.svc.ListProjectSnapshots(owner, pid)
	require.NoError(t, err)
	require.Len(t, rows, maxProjectSnapshots)
	// Even restoring the current snapshot is prohibited without safety capacity.
	_, err = f.svc.RestoreProjectSnapshot(owner, pid, rows[0].ID, 2)
	require.Equal(t, "papers.snapshot_limit", apperr.FromError(err).Code)
	require.NoError(t, f.svc.DeleteProjectSnapshot(owner, pid, snap.ID))
	_, err = f.svc.RestoreProjectSnapshot(owner, pid, rows[0].ID, 2)
	require.NoError(t, err)
}
