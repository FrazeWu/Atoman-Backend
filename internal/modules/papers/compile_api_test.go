package papers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testEnvDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type localTestPublisher struct {
	store       *papersassets.Store
	before      func()
	failure     error
	forceDigest string
	calls       int
}

func (p *localTestPublisher) PublishSnapshot(ctx context.Context, snapshot papersassets.Snapshot) (string, error) {
	p.calls++
	if p.before != nil {
		p.before()
	}
	if p.failure != nil {
		return "", p.failure
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.forceDigest != "" {
		return p.forceDigest, nil
	}
	return p.store.PublishSnapshot(snapshot)
}

type testRelease struct {
	envDigest string
	failure   error
	academic  bool
	cjk       bool
}

func (r testRelease) LoadRelease(_ context.Context, _ string) (papersassets.SignedArchiveRelease, error) {
	if r.failure != nil {
		return papersassets.SignedArchiveRelease{}, r.failure
	}
	env := papersassets.Environment{
		Version: 1, Name: "texlive-2026-p0",
		ImageDigest:   "sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4",
		ArchiveDigest: "sha256:" + strings.Repeat("a", 64),
		RunscRelease:  "20261019.0",
	}
	if r.academic {
		env = papersassets.AcademicEnvironment()
	}
	if r.cjk {
		env = papersassets.AcademicCJKEnvironment()
	}
	return papersassets.SignedArchiveRelease{Claim: papersassets.ArchiveClaim{
		Version: 1, EnvironmentDigest: r.envDigest, Environment: env,
	}, Signature: "unit-test-only-mock-trust"}, nil
}

type compileHTTPFixture struct {
	db         *gorm.DB
	api        *gin.Engine
	svc        *Service
	queue      *papersqueue.Queue
	publisher  *localTestPublisher
	owner      model.User
	other      model.User
	project    model.PaperProject
	ownerToken string
	otherToken string
}

func newCompileHTTPFixture(t *testing.T, enabled bool) *compileHTTPFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.AuthSession{}, &model.AuditLog{}, &model.PaperAccessGrant{},
		&model.PaperProject{}, &model.PaperFile{}, &model.PaperFileVersion{},
		&model.PaperCompileJob{}, &model.PaperCompileSlot{})
	require.NoError(t, db.Create(&model.PaperCompileSlot{ID: 1}).Error)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	owner := model.User{Username: "owner-" + uuid.NewString(), Email: uuid.NewString() + "@example.net", Password: "fake", Role: authctx.RoleUser, IsActive: true}
	other := model.User{Username: "other-" + uuid.NewString(), Email: uuid.NewString() + "@example.net", Password: "fake", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&other).Error)
	require.NoError(t, db.Create(&model.PaperAccessGrant{UserID: owner.UUID, Status: "active"}).Error)
	require.NoError(t, db.Create(&model.PaperAccessGrant{UserID: other.UUID, Status: "active"}).Error)
	tok := func(u model.User) string {
		token, err := authsession.New(db).Create(u.UUID, authsession.KindAPI)
		require.NoError(t, err)
		return token.Token
	}
	ownerToken := tok(owner)
	otherToken := tok(other)
	queue, err := papersqueue.New(db, 15*time.Second)
	require.NoError(t, err)
	root := filepath.Join(t.TempDir(), "snapshots")
	require.NoError(t, os.Mkdir(root, 0700))
	store, err := papersassets.OpenStore(root)
	require.NoError(t, err)
	pub := &localTestPublisher{store: store}
	var svc *Service
	if enabled {
		svc, err = NewServiceWithCompile(db, CompileRuntime{
			Publisher: pub, Release: testRelease{envDigest: testEnvDigest},
			Jobs: queue, EnvironmentDigest: testEnvDigest,
		})
		require.NoError(t, err)
	} else {
		svc = NewService(db)
	}
	project, err := svc.CreateProject(authctx.CurrentUser{ID: owner.UUID, Role: authctx.RoleUser}, "Secure Paper")
	require.NoError(t, err)
	api := gin.New()
	RegisterRoutes(api.Group("/api/v1/papers"), svc)
	return &compileHTTPFixture{db: db, api: api, svc: svc, queue: queue, publisher: pub,
		owner: owner, other: other, project: project, ownerToken: ownerToken, otherToken: otherToken}
}
func (f *compileHTTPFixture) request(method, path, body, token string) *httptest.ResponseRecorder {
	out := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	f.api.ServeHTTP(out, req)
	return out
}
func (f *compileHTTPFixture) base() string {
	return "/api/v1/papers/projects/" + f.project.ID.String()
}
func parseJob(t *testing.T, out *httptest.ResponseRecorder) CompileJobView {
	t.Helper()
	var result struct{ Data CompileJobView }
	require.NoError(t, json.Unmarshal(out.Body.Bytes(), &result), out.Body.String())
	return result.Data
}
func TestCompileAPIDisabledInNormalProductionComposition(t *testing.T) {
	f := newCompileHTTPFixture(t, false)
	base := f.base()
	require.Equal(t, http.StatusUnauthorized, f.request("GET", base+"/compile-capability", "", "").Code)
	require.Equal(t, http.StatusNotFound, f.request("GET", base+"/compile-capability", "", f.otherToken).Code)
	capability := f.request("GET", base+"/compile-capability", "", f.ownerToken)
	require.Equal(t, http.StatusOK, capability.Code)
	require.Contains(t, capability.Body.String(), `"enabled":false`)
	require.Contains(t, capability.Body.String(), `"pdf_available":false`)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/compiles", `{"base_revision":1,"idempotency_key":"no-execute"}`},
		{"GET", "/compiles", ""},
		{"GET", "/compiles/" + uuid.NewString(), ""},
		{"DELETE", "/compiles/" + uuid.NewString(), ""},
		{"GET", "/compiles/" + uuid.NewString() + "/pdf", ""},
	} {
		out := f.request(tc.method, base+tc.path, tc.body, f.ownerToken)
		require.Equal(t, http.StatusServiceUnavailable, out.Code, out.Body.String())
	}
	require.Zero(t, f.publisher.calls)
	var count int64
	require.NoError(t, f.db.Model(&model.PaperCompileJob{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestCompileAPISnapshotQueuedIdempotentOwnedAndCancellable(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	base := f.base()
	ctx := context.Background()
	require.Equal(t, http.StatusNotFound, f.request("GET", base+"/compiles", "", f.otherToken).Code)
	require.Equal(t, http.StatusNotFound, f.request("POST", base+"/compiles", `{"base_revision":1,"idempotency_key":"no-owner"}`, f.otherToken).Code)
	require.Equal(t, http.StatusBadRequest, f.request("POST", base+"/compiles", `{"base_revision":0,"idempotency_key":"bad"}`, f.ownerToken).Code)
	requestBody := `{"base_revision":1,"idempotency_key":"click-1"}`
	submitted := f.request("POST", base+"/compiles", requestBody, f.ownerToken)
	require.Equal(t, http.StatusAccepted, submitted.Code, submitted.Body.String())
	job := parseJob(t, submitted)
	require.NotEqual(t, uuid.Nil, job.ID)
	require.Equal(t, "queued", job.Status)
	require.Equal(t, int64(1), job.SourceRevision)
	require.NotContains(t, submitted.Body.String(), "lease_token")
	require.NotContains(t, submitted.Body.String(), "sandbox_id")
	require.NotContains(t, submitted.Body.String(), "worker_id")
	require.Equal(t, 1, f.publisher.calls)
	indexed, found, err := f.queue.FindByIdempotencyKey(ctx, f.owner.UUID, f.project.ID, "click-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, job.ID, indexed.ID)
	// A repeated click returns the exact same job without republishing.
	duplicate := f.request("POST", base+"/compiles", requestBody, f.ownerToken)
	require.Equal(t, http.StatusAccepted, duplicate.Code, duplicate.Body.String())
	require.Equal(t, job.ID, parseJob(t, duplicate).ID)
	require.Equal(t, 1, f.publisher.calls)
	jobPath := base + "/compiles/" + job.ID.String()
	require.Equal(t, http.StatusNotFound, f.request("GET", jobPath, "", f.otherToken).Code)
	require.Equal(t, http.StatusNotFound, f.request("DELETE", jobPath, "", f.otherToken).Code)
	require.Equal(t, http.StatusOK, f.request("GET", jobPath, "", f.ownerToken).Code)
	require.Equal(t, http.StatusOK, f.request("GET", base+"/compiles", "", f.ownerToken).Code)
	pdf := f.request("GET", jobPath+"/pdf", "", f.ownerToken)
	require.Equal(t, http.StatusServiceUnavailable, pdf.Code, pdf.Body.String())
	require.Contains(t, pdf.Body.String(), "papers.pdf_persistence_pending")
	require.Equal(t, "private, no-store", pdf.Header().Get("Cache-Control"))
	// Project edits after accepted submission do NOT mutate its frozen revision.
	file, err := f.svc.GetFile(authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}, f.project.ID, f.project.MainFileID)
	require.NoError(t, err)
	_, err = f.svc.SaveFile(authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}, f.project.ID, file.ID, "NEW SOURCE", file.Revision)
	require.NoError(t, err)
	duplicate = f.request("POST", base+"/compiles", requestBody, f.ownerToken)
	require.Equal(t, http.StatusAccepted, duplicate.Code, duplicate.Body.String())
	require.Equal(t, job.ID, parseJob(t, duplicate).ID)
	require.Equal(t, http.StatusConflict, f.request("POST", base+"/compiles",
		`{"base_revision":2,"idempotency_key":"click-1"}`, f.ownerToken).Code)
	cancel := f.request("DELETE", jobPath, "", f.ownerToken)
	require.Equal(t, http.StatusOK, cancel.Code, cancel.Body.String())
	require.Equal(t, "canceled", parseJob(t, cancel).Status)
	require.True(t, parseJob(t, cancel).CancelRequested)
	repeatedCancel := f.request("DELETE", jobPath, "", f.ownerToken)
	require.Equal(t, http.StatusOK, repeatedCancel.Code, repeatedCancel.Body.String())
	var count int64
	require.NoError(t, f.db.Model(&model.PaperCompileJob{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCompileAPIRejectsUnsupportedSourceAndRevisionRace(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	base := f.base()
	ownerCtx := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	_, err := f.svc.CreateFile(ownerCtx, f.project.ID, ".latexmkrc", "# unsupported configuration")
	require.Error(t, err)
	// A legacy editor-only file from a prior release must still fail closed.
	legacy := model.PaperFile{ProjectID: f.project.ID, Path: ".latexmkrc", Kind: "text", Content: "# legacy configuration", SHA256: hashText("# legacy configuration"), Revision: 1}
	require.NoError(t, f.db.Create(&legacy).Error)
	require.NoError(t, f.db.Model(&model.PaperProject{}).Where("id=?", f.project.ID).Update("head_revision", 2).Error)
	reject := f.request("POST", base+"/compiles", `{"base_revision":2,"idempotency_key":"config-rejected"}`, f.ownerToken)
	require.Equal(t, http.StatusUnprocessableEntity, reject.Code, reject.Body.String())
	require.Equal(t, 0, f.publisher.calls)
	// An out-of-date revision is rejected before any external S3 publication.
	require.Equal(t, http.StatusConflict, f.request("POST", base+"/compiles",
		`{"base_revision":1,"idempotency_key":"stale"}`, f.ownerToken).Code)
	// Confirm publishing cannot produce a stale queue entry: the project
	// advances after the DB snapshot but before queue.Submit.
	f2 := newCompileHTTPFixture(t, true)
	f2.publisher.before = func() {
		f2.publisher.before = nil
		file, err := f2.svc.GetFile(authctx.CurrentUser{ID: f2.owner.UUID, Role: authctx.RoleUser}, f2.project.ID, f2.project.MainFileID)
		require.NoError(t, err)
		_, err = f2.svc.SaveFile(authctx.CurrentUser{ID: f2.owner.UUID, Role: authctx.RoleUser}, f2.project.ID, file.ID, "changed during publication", file.Revision)
		require.NoError(t, err)
	}
	out := f2.request("POST", f2.base()+"/compiles",
		`{"base_revision":1,"idempotency_key":"race"}`, f2.ownerToken)
	require.Equal(t, http.StatusConflict, out.Code, out.Body.String())
	var count int64
	require.NoError(t, f2.db.Model(&model.PaperCompileJob{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestCompileAPITrustedEnvironmentMustBeVerified(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	f.svc.compile.Release = testRelease{envDigest: testEnvDigest, failure: errors.New("invalid Ed25519 signature")}
	out := f.request("POST", f.base()+"/compiles",
		`{"base_revision":1,"idempotency_key":"bad-release"}`, f.ownerToken)
	require.Equal(t, http.StatusServiceUnavailable, out.Code, out.Body.String())
	require.Zero(t, f.publisher.calls)
}

func TestCompileAPIRejectsPublisherDigestMismatch(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	f.publisher.forceDigest = "sha256:" + strings.Repeat("d", 64)
	out := f.request("POST", f.base()+"/compiles",
		`{"base_revision":1,"idempotency_key":"mismatch"}`, f.ownerToken)
	require.Equal(t, http.StatusInternalServerError, out.Code, out.Body.String())
	var count int64
	require.NoError(t, f.db.Model(&model.PaperCompileJob{}).Count(&count).Error)
	require.Zero(t, count, "a mismatched immutable source digest must never enqueue")
}

func TestCompileEngineAllowlistAndImmutableBinding(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	base := f.base()
	lua := `{"base_revision":1,"idempotency_key":"lua-test","compiler":"lualatex"}`
	unknown := `{"base_revision":1,"idempotency_key":"unknown","compiler":"/bin/sh"}`
	require.Equal(t, http.StatusBadRequest, f.request("POST", base+"/compiles", unknown, f.ownerToken).Code)
	require.Equal(t, http.StatusBadRequest, f.request("POST", base+"/compiles", lua, f.ownerToken).Code)
	require.Zero(t, f.publisher.calls)
	require.NotContains(t, f.request("GET", base+"/compile-capability", "", f.ownerToken).Body.String(), "lualatex")
	// The exact signed pin alone is insufficient without explicit opt-in.
	digest := papersassets.AcademicEnvironmentDigest()
	f.svc.compile.EnvironmentDigest = digest
	f.svc.compile.Release = testRelease{envDigest: digest, academic: true}
	require.Equal(t, http.StatusBadRequest, f.request("POST", base+"/compiles", lua, f.ownerToken).Code)
	f.svc.compile.EnableLuaLaTeX = true
	require.Contains(t, f.request("GET", base+"/compile-capability", "", f.ownerToken).Body.String(), "lualatex")
	out := f.request("POST", base+"/compiles", lua, f.ownerToken)
	require.Equal(t, http.StatusAccepted, out.Code, out.Body.String())
	view := parseJob(t, out)
	require.Equal(t, papersassets.CompilerLuaLaTeX, view.Compiler)
	job, found, err := f.queue.FindByIdempotencyKey(context.Background(), f.owner.UUID, f.project.ID, "lua-test")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, papersassets.CompilerLuaLaTeX, job.Compiler)
	snapshot, err := f.publisher.store.LoadSnapshot(job.SnapshotDigest)
	require.NoError(t, err)
	require.Equal(t, papersassets.CompilerLuaLaTeX, snapshot.Compiler)
	require.Equal(t, view.ID, parseJob(t, f.request("POST", base+"/compiles", lua, f.ownerToken)).ID)
	require.Equal(t, http.StatusConflict, f.request("POST", base+"/compiles", `{"base_revision":1,"idempotency_key":"lua-test","compiler":"pdflatex"}`, f.ownerToken).Code)
	require.Equal(t, 1, f.publisher.calls)
}

func TestXeLaTeXRequiresExactCJKPinAndExplicitOptIn(t *testing.T) {
	f := newCompileHTTPFixture(t, true)
	base := f.base()
	req := `{"base_revision":1,"idempotency_key":"xe","compiler":"xelatex"}`
	f.svc.compile.EnableXeLaTeX = true
	f.svc.compile.EnvironmentDigest = papersassets.AcademicEnvironmentDigest()
	f.svc.compile.Release = testRelease{envDigest: f.svc.compile.EnvironmentDigest, academic: true}
	require.Equal(t, http.StatusBadRequest, f.request("POST", base+"/compiles", req, f.ownerToken).Code)
	digest := papersassets.AcademicCJKEnvironmentDigest()
	f.svc.compile.EnvironmentDigest = digest
	f.svc.compile.Release = testRelease{envDigest: digest, cjk: true}
	f.svc.compile.EnableXeLaTeX = false
	require.Equal(t, http.StatusBadRequest, f.request("POST", base+"/compiles", req, f.ownerToken).Code)
	require.NotContains(t, f.request("GET", base+"/compile-capability", "", f.ownerToken).Body.String(), "xelatex")
	f.svc.compile.EnableXeLaTeX = true
	require.Contains(t, f.request("GET", base+"/compile-capability", "", f.ownerToken).Body.String(), "xelatex")
	out := f.request("POST", base+"/compiles", req, f.ownerToken)
	require.Equal(t, http.StatusAccepted, out.Code, out.Body.String())
	require.Equal(t, papersassets.CompilerXeLaTeX, parseJob(t, out).Compiler)
	job, found, err := f.queue.FindByIdempotencyKey(context.Background(), f.owner.UUID, f.project.ID, "xe")
	require.NoError(t, err)
	require.True(t, found)
	snap, err := f.publisher.store.LoadSnapshot(job.SnapshotDigest)
	require.NoError(t, err)
	require.Equal(t, papersassets.CompilerXeLaTeX, snap.Compiler)
	require.Equal(t, http.StatusConflict, f.request("POST", base+"/compiles", `{"base_revision":1,"idempotency_key":"xe","compiler":"pdflatex"}`, f.ownerToken).Code)
}
