package papers

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPapersPrivateAccessAndRevisionCAS(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.AuditLog{}, &model.PaperAccessGrant{},
		&model.PaperProject{}, &model.PaperFile{}, &model.PaperFileVersion{},
	)
	admin := model.User{Username: "papers-admin-" + uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	owner := model.User{Username: "papers-owner-" + uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	other := model.User{Username: "papers-other-" + uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&admin).Error)
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&other).Error)
	service := NewService(db)
	adminCtx := authctx.CurrentUser{ID: admin.UUID, Role: authctx.RoleAdmin}
	ownerCtx := authctx.CurrentUser{ID: owner.UUID, Role: authctx.RoleUser}
	otherCtx := authctx.CurrentUser{ID: other.UUID, Role: authctx.RoleUser}

	_, err := service.CreateProject(ownerCtx, "Unauthorized")
	require.Equal(t, 403, apperr.FromError(err).HTTPStatus)
	_, err = service.CreateProject(adminCtx, "Admin not invited")
	require.Equal(t, 403, apperr.FromError(err).HTTPStatus)
	require.Error(t, service.GrantAccess(otherCtx, owner.UUID, nil))
	require.NoError(t, service.GrantAccess(adminCtx, owner.UUID, nil))
	require.NoError(t, service.GrantAccess(adminCtx, other.UUID, nil))

	project, err := service.CreateProject(ownerCtx, "My Paper")
	require.NoError(t, err)
	require.Equal(t, int64(1), project.HeadRevision)
	require.NotEqual(t, uuid.Nil, project.MainFileID)
	_, err = service.GetProject(otherCtx, project.ID)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = service.GetFile(otherCtx, project.ID, project.MainFileID)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)

	initial, err := service.GetFile(ownerCtx, project.ID, project.MainFileID)
	require.NoError(t, err)
	require.Contains(t, initial.Content, "documentclass")
	require.Equal(t, int64(1), initial.Revision)
	saved, err := service.SaveFile(ownerCtx, project.ID, initial.ID, "\\section{Experiments}\n", 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), saved.Revision)
	require.Equal(t, int64(2), saved.ProjectHeadRevision)
	retry, err := service.SaveFile(ownerCtx, project.ID, initial.ID, "\\section{Experiments}\n", 1)
	require.NoError(t, err)
	require.Equal(t, saved.Revision, retry.Revision)
	require.Equal(t, int64(2), retry.ProjectHeadRevision)

	_, err = service.SaveFile(ownerCtx, project.ID, initial.ID, "stale replacement", 1)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	current, err := service.GetFile(ownerCtx, project.ID, initial.ID)
	require.NoError(t, err)
	require.Equal(t, "\\section{Experiments}\n", current.Content)
	var versions []model.PaperFileVersion
	require.NoError(t, db.Where("file_id = ?", initial.ID).Order("revision").Find(&versions).Error)
	require.Len(t, versions, 2)
	require.Equal(t, initial.Content, versions[0].Content)
	require.Equal(t, current.Content, versions[1].Content)
	history, err := service.ListFileVersions(ownerCtx, project.ID, initial.ID, 0)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, int64(2), history[0].Revision)
	history, err = service.ListFileVersions(ownerCtx, project.ID, initial.ID, 2)
	require.NoError(t, err)
	require.Len(t, history, 1)
	historical, err := service.GetFileVersion(ownerCtx, project.ID, initial.ID, 1)
	require.NoError(t, err)
	require.Equal(t, initial.Content, historical.Content)
	_, err = service.GetFileVersion(otherCtx, project.ID, initial.ID, 1)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = service.ListFileVersions(otherCtx, project.ID, initial.ID, 0)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = service.GetFileVersion(ownerCtx, project.ID, initial.ID, 99)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)

	created, err := service.CreateFile(ownerCtx, project.ID, "chapters/introduction.tex", "Hello")
	require.NoError(t, err)
	require.Equal(t, int64(1), created.Revision)
	_, err = service.CreateFile(ownerCtx, project.ID, "chapters/introduction.tex", "duplicate")
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	for _, collision := range []string{"CHAPTERS/Introduction.TEX", "main.tex/chapter.tex", "chapters/introduction.tex/child.tex"} {
		_, err = service.CreateFile(ownerCtx, project.ID, collision, "")
		require.Equal(t, 409, apperr.FromError(err).HTTPStatus, collision)
	}
	_, err = service.CreateFile(ownerCtx, project.ID, ".latexmkrc", "unsupported")
	require.Equal(t, 400, apperr.FromError(err).HTTPStatus)
	require.Equal(t, int64(3), created.ProjectHeadRevision)

	for _, badPath := range []string{"../escape.tex", "/etc/passwd.tex", "./main.tex", "folder\\file.tex", "a//b.tex", "unsafe.sh"} {
		_, err = service.CreateFile(ownerCtx, project.ID, badPath, "")
		require.Equal(t, 400, apperr.FromError(err).HTTPStatus, badPath)
	}
	_, err = service.SaveFile(ownerCtx, project.ID, initial.ID, string(make([]byte, maxTextBytes+1)), 2)
	require.Equal(t, 400, apperr.FromError(err).HTTPStatus)
	updated, err := service.GetProject(ownerCtx, project.ID)
	require.NoError(t, err)
	require.Equal(t, int64(3), updated.HeadRevision)
	replacements := FileChange{Action: "replace_text", BaseRevision: updated.HeadRevision, Replacements: []TextReplacement{
		{FileID: initial.ID, BaseRevision: 2, Content: "new main"},
		{FileID: created.ID, BaseRevision: 999, Content: "new intro"},
	}}
	_, err = service.ManageFiles(ownerCtx, project.ID, replacements)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	unchanged, err := service.GetFile(ownerCtx, project.ID, initial.ID)
	require.NoError(t, err)
	require.Equal(t, current.Content, unchanged.Content)
	replacements.Replacements[1].BaseRevision = 1
	result, err := service.ManageFiles(ownerCtx, project.ID, replacements)
	require.NoError(t, err)
	require.Equal(t, int64(4), result.Project.HeadRevision)
	edited, err := service.GetFile(ownerCtx, project.ID, created.ID)
	require.NoError(t, err)
	require.Equal(t, "new intro", edited.Content)
	require.Equal(t, int64(2), edited.Revision)
	historical, err = service.GetFileVersion(ownerCtx, project.ID, initial.ID, 2)
	require.NoError(t, err)
	require.Equal(t, current.Content, historical.Content)
	_, err = service.ManageFiles(ownerCtx, project.ID, replacements)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)

	require.NoError(t, service.RevokeAccess(adminCtx, owner.UUID))
	_, err = service.GetProject(ownerCtx, project.ID)
	require.Equal(t, 403, apperr.FromError(err).HTTPStatus)
	require.NoError(t, service.GrantAccess(adminCtx, owner.UUID, nil))
	recovered, err := service.GetProject(ownerCtx, project.ID)
	require.NoError(t, err)
	require.Equal(t, project.ID, recovered.ID)
}

func TestPapersValidation(t *testing.T) {
	for _, name := range []string{"", "   ", "draft\ninvalid", "draft\u202eexe", string([]byte{0xff})} {
		_, err := validatedName(name)
		require.Error(t, err)
	}
	for _, name := range []string{"main.tex", "chapters/intro.tex", "refs.bib", ".latexmkrc", "章节/绪论.tex"} {
		got, err := validatedPath(name)
		require.NoError(t, err)
		require.Equal(t, name, got)
	}
	for _, invalidPath := range []string{
		"line\nfeed.tex", "line\rreturn.tex", "line\ttab.tex",
		"foo\u202eexe.tex", "a/../escape.tex", string([]byte{0xff}) + ".tex",
	} {
		_, err := validatedPath(invalidPath)
		require.Equal(t, 400, apperr.FromError(err).HTTPStatus, invalidPath)
	}
	require.Error(t, validateText(string([]byte{0xff})))
	require.Error(t, validateText("ok\x00"))
}

func TestExpiredPapersInvitationIsDenied(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.PaperAccessGrant{})
	s := NewService(db)
	user := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	expired := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Create(&model.PaperAccessGrant{UserID: user.ID, Status: "active", ExpiresAt: &expired}).Error)
	allowed, err := s.AccessAllowed(user)
	require.NoError(t, err)
	require.False(t, allowed)
	_, err = s.CreateProject(user, "Must not create")
	require.Equal(t, 403, apperr.FromError(err).HTTPStatus)
}
