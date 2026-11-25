package papers

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestProjectMemberAuthorizationAndRevisionFencing(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	root := f.base()
	member := root + "/members/" + f.other.UUID.String()
	file := root + "/files/" + f.project.MainFileID.String()
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	other := authctx.CurrentUser{ID: f.other.UUID, Role: authctx.RoleUser}
	req := func(method, path, body, token string, status int) {
		t.Helper()
		res := f.request(method, path, body, token)
		require.Equal(t, status, res.Code, res.Body.String())
	}
	req("GET", root, "", f.otherToken, 404)
	req("GET", root+"/files", "", f.otherToken, 404)
	req("GET", file, "", f.otherToken, 404)
	req("GET", root+"/members", "", f.otherToken, 404)
	req("PUT", member, `{"role":"viewer"}`, f.otherToken, 404)
	req("PUT", member, `{"role":"owner"}`, f.ownerToken, 400)
	req("PUT", root+"/members/"+f.owner.UUID.String(), `{"role":"editor"}`, f.ownerToken, 400)
	req("PUT", root+"/members/"+uuid.NewString(), `{"role":"editor"}`, f.ownerToken, 400)
	req("PUT", member, `{"role":"viewer"}`, f.ownerToken, 200)
	res := f.request("GET", root, "", f.otherToken)
	require.Equal(t, 200, res.Code, res.Body.String())
	require.Contains(t, res.Body.String(), `"access_role":"viewer"`)
	require.Equal(t, "private, no-store", res.Header().Get("Cache-Control"))
	req("GET", root+"/files", "", f.otherToken, 200)
	req("GET", file, "", f.otherToken, 200)
	projects, err := f.svc.ListProjects(other)
	require.NoError(t, err)
	require.Len(t, projects, 1)
	require.Equal(t, RoleProjectViewer, projects[0].AccessRole)
	projects, err = f.svc.ListProjects(owner)
	require.NoError(t, err)
	require.Len(t, projects, 1)
	require.Equal(t, RoleProjectOwner, projects[0].AccessRole)
	req("PUT", file, `{"content":"Read-only overwrite","base_revision":1}`, f.otherToken, 403)
	req("PUT", member, `{"role":"editor"}`, f.ownerToken, 200)
	req("PUT", file, `{"content":"Editor version","base_revision":1}`, f.otherToken, 200)
	req("PUT", file, `{"content":"Stale owner write","base_revision":1}`, f.ownerToken, 409)
	current, err := f.svc.GetFile(owner, f.project.ID, f.project.MainFileID)
	require.NoError(t, err)
	require.Equal(t, "Editor version", current.Content)
	require.Equal(t, int64(2), current.Revision)
	var version model.PaperFileVersion
	require.NoError(t, f.db.Where("file_id=? AND revision=?", current.ID, 2).First(&version).Error)
	require.Equal(t, f.other.UUID, version.AuthorID)
	// Explicitly retain the owner boundary on every existing privileged route.
	req("GET", root+"/members", "", f.otherToken, 404)
	req("PUT", member, `{"role":"viewer"}`, f.otherToken, 404)
	req("DELETE", member, "", f.otherToken, 404)
	req("PATCH", root, `{"name":"Changed","base_revision":2}`, f.otherToken, 404)
	req("DELETE", root, "", f.otherToken, 404)
	req("GET", root+"/export", "", f.otherToken, 404)
	req("POST", root+"/files", `{"path":"new.tex","content":"x"}`, f.otherToken, 404)
	req("POST", root+"/files/manage", `{"action":"upload","base_revision":2,"uploads":[{"path":"new.tex","kind":"text","content":"x"}]}`, f.otherToken, 404)
	req("GET", file+"/versions", "", f.otherToken, 404)
	req("GET", file+"/versions/1", "", f.otherToken, 404)
	req("GET", root+"/snapshots", "", f.otherToken, 404)
	req("POST", root+"/snapshots", `{"name":"private","base_revision":2}`, f.otherToken, 404)
	req("GET", root+"/compile-capability", "", f.otherToken, 404)
	req("GET", root+"/compiles", "", f.otherToken, 404)
	req("POST", root+"/compiles", `{"base_revision":2,"idempotency_key":"no-access"}`, f.otherToken, 404)
	require.Zero(t, f.publisher.calls)
	req("PUT", member, `{"role":"viewer"}`, f.ownerToken, 200)
	req("PUT", file, `{"content":"Downgraded","base_revision":2}`, f.otherToken, 403)
	req("DELETE", member, "", f.ownerToken, 200)
	req("GET", file, "", f.otherToken, 404)
	req("PUT", file, `{"content":"Revoked","base_revision":2}`, f.otherToken, 404)
	projects, err = f.svc.ListProjects(other)
	require.NoError(t, err)
	require.Empty(t, projects)
	req("PUT", member, `{"role":"editor"}`, f.ownerToken, 200)
	require.NoError(t, f.db.Model(&model.PaperAccessGrant{}).Where("user_id=?", f.other.UUID).Update("expires_at", time.Now().Add(-time.Hour)).Error)
	req("GET", file, "", f.otherToken, 403)
	req("PUT", file, `{"content":"Expired","base_revision":2}`, f.otherToken, 403)
	req("PUT", member, `{"role":"editor"}`, f.ownerToken, 400)
	require.NoError(t, f.db.Model(&model.PaperAccessGrant{}).Where("user_id=?", f.other.UUID).Update("expires_at", nil).Error)
	req("GET", file, "", f.otherToken, 200)
	req("DELETE", root, "", f.ownerToken, 204)
	req("POST", root+"/restore", "", f.ownerToken, 200)
	req("GET", file, "", f.otherToken, 404)
	members, err := f.svc.ListMembers(owner, f.project.ID)
	require.NoError(t, err)
	require.Empty(t, members)
}

func TestProjectMembersDoNotExposePrivateIdentityOrContent(t *testing.T) {
	f := newCompileHTTPFixture(t, false)
	res := f.request("PUT", f.base()+"/members/"+f.other.UUID.String(), `{"role":"editor"}`, f.ownerToken)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	var envelope struct{ Data model.PaperProjectMember }
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
	require.Equal(t, f.other.UUID, envelope.Data.UserID)
	require.Equal(t, RoleProjectEditor, envelope.Data.Role)
	require.NotContains(t, res.Body.String(), f.other.Email)
	require.NotContains(t, res.Body.String(), "owner_id")
	require.NotContains(t, res.Body.String(), "content")
}

func TestConcurrentCollaboratorsCannotLoseAnAcceptedRevision(t *testing.T) {
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	editor := authctx.CurrentUser{ID: f.other.UUID, Role: authctx.RoleUser}
	_, err := f.svc.SetMember(owner, f.project.ID, editor.ID, RoleProjectEditor)
	require.NoError(t, err)
	type result struct {
		content string
		err     error
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, writer := range []struct {
		user    authctx.CurrentUser
		content string
	}{{owner, "Owner draft"}, {editor, "Editor draft"}} {
		go func() {
			ready <- struct{}{}
			<-start
			_, err := f.svc.SaveFile(writer.user, f.project.ID, f.project.MainFileID, writer.content, 1)
			results <- result{writer.content, err}
		}()
	}
	<-ready
	<-ready
	close(start)
	accepted := ""
	conflicts := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err == nil {
			require.Empty(t, accepted)
			accepted = r.content
		} else {
			require.Equal(t, 409, apperr.FromError(r.err).HTTPStatus)
			conflicts++
		}
	}
	require.NotEmpty(t, accepted)
	require.Equal(t, 1, conflicts)
	current, err := f.svc.GetFile(owner, f.project.ID, f.project.MainFileID)
	require.NoError(t, err)
	require.Equal(t, accepted, current.Content)
	require.Equal(t, int64(2), current.Revision)
	versions, err := f.svc.ListFileVersions(owner, f.project.ID, current.ID, 0)
	require.NoError(t, err)
	require.Len(t, versions, 2)
}
