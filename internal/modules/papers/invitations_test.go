package papers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/stretchr/testify/require"
)

func TestProjectInvitationLinkAuthorization(t *testing.T) {
	f := newCompileHTTPFixture(t, false)
	root := f.base()
	links := root + "/invitations"
	accept := "/api/v1/papers/invitations/accept"
	req := func(method, path, body, token string, status int) []byte {
		t.Helper()
		res := f.request(method, path, body, token)
		require.Equal(t, status, res.Code, res.Body.String())
		require.Equal(t, "private, no-store", res.Header().Get("Cache-Control"))
		return res.Body.Bytes()
	}
	create := func(role string) CreatedInvitation {
		t.Helper()
		var env struct{ Data CreatedInvitation }
		require.NoError(t, json.Unmarshal(req("POST", links, `{"role":"`+role+`"}`, f.ownerToken, 201), &env))
		require.Len(t, env.Data.Token, 43)
		return env.Data
	}
	body := func(token string) string { return `{"token":"` + token + `"}` }
	req("POST", links, `{"role":"viewer"}`, f.otherToken, 404)
	req("POST", links, `{"role":"owner"}`, f.ownerToken, 400)
	view := create("viewer")
	var stored model.PaperProjectInvitation
	require.NoError(t, f.db.Where("id=?", view.ID).First(&stored).Error)
	require.NotEqual(t, view.Token, stored.TokenHash)
	require.Len(t, stored.TokenHash, 64)
	list := req("GET", links, "", f.ownerToken, 200)
	require.NotContains(t, string(list), view.Token)
	require.NotContains(t, string(list), stored.TokenHash)
	req("POST", accept, body(view.Token), "", 401)
	req("POST", accept, body(view.Token), f.ownerToken, 400)
	require.NoError(t, f.db.Model(&model.PaperAccessGrant{}).Where("user_id=?", f.other.UUID).Update("status", "revoked").Error)
	req("POST", accept, body(view.Token), f.otherToken, 403)
	require.NoError(t, f.db.Model(&model.PaperAccessGrant{}).Where("user_id=?", f.other.UUID).Update("status", "active").Error)
	req("POST", accept, body("invalid"), f.otherToken, 404)
	req("POST", accept, body(view.Token), f.otherToken, 200)
	file := root + "/files/" + f.project.MainFileID.String()
	req("GET", file, "", f.otherToken, 200)
	req("PUT", file, `{"content":"Forbidden","base_revision":1}`, f.otherToken, 403)
	upgrade := create("editor")
	req("POST", accept, body(upgrade.Token), f.otherToken, 200)
	req("PUT", file, `{"content":"Still forbidden","base_revision":1}`, f.otherToken, 403)
	req("POST", accept, body(upgrade.Token), f.otherToken, 404)
	req("POST", accept, body(view.Token), f.otherToken, 404)
	req("DELETE", root+"/members/"+f.other.UUID.String(), "", f.ownerToken, 200)
	req("POST", accept, body(view.Token), f.otherToken, 404)
	edit := create("editor")
	req("POST", accept, body(edit.Token), f.otherToken, 200)
	req("PUT", file, `{"content":"Allowed","base_revision":1}`, f.otherToken, 200)
	req("POST", links, `{"role":"editor"}`, f.otherToken, 404)
	req("DELETE", root+"/members/"+f.other.UUID.String(), "", f.ownerToken, 200)
	revoked := create("viewer")
	req("DELETE", links+"/"+revoked.ID.String(), "", f.ownerToken, 200)
	req("POST", accept, body(revoked.Token), f.otherToken, 404)
	expired := create("viewer")
	require.NoError(t, f.db.Model(&model.PaperProjectInvitation{}).Where("id=?", expired.ID).Update("expires_at", time.Now().Add(-time.Hour)).Error)
	req("POST", accept, body(expired.Token), f.otherToken, 404)
	trash := create("editor")
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	require.NoError(t, f.svc.DeleteProject(owner, f.project.ID))
	req("POST", root+"/restore", "", f.ownerToken, http.StatusOK)
	req("POST", accept, body(trash.Token), f.otherToken, 404)
}
