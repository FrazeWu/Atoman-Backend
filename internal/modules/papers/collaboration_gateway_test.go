package papers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/platform/paperscrdt"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func collabPath(f *compileHTTPFixture) string {
	return f.base() + "/files/" + f.project.MainFileID.String() + "/collaboration"
}
func TestCollabGatewayDisabledAndAdmission(t *testing.T) {
	f := newCompileHTTPFixture(t, false)
	path := collabPath(f)
	require.Equal(t, 401, f.request("GET", path, "", "").Code)
	require.Equal(t, 404, f.request("GET", path, "", f.otherToken).Code)
	r := f.request("GET", path, "", f.ownerToken)
	require.Equal(t, 200, r.Code)
	require.Contains(t, r.Body.String(), `"enabled":false`)
	require.Equal(t, 503, f.request("GET", path+"/ws", "", f.ownerToken).Code)
	require.NoError(t, f.svc.configureCollaboration(testStubEngine{}))
	g := f.svc.collaboration
	key := roomKey{project: f.project.ID, file: f.project.MainFileID}
	var release []func()
	for i := 0; i < collabMaxPerAccount; i++ {
		off, ok := g.admit(f.owner.UUID, key)
		require.True(t, ok)
		release = append(release, off)
	}
	_, ok := g.admit(f.owner.UUID, key)
	require.False(t, ok)
	for _, off := range release {
		off()
		off()
	}
	require.Zero(t, g.count)
	require.Empty(t, g.accounts)
	require.Empty(t, g.rooms)
	for i := 0; i < collabMaxPerRoom; i++ {
		off, ok := g.admit(uuid.New(), key)
		require.True(t, ok)
		defer off()
	}
	_, ok = g.admit(uuid.New(), key)
	require.False(t, ok)
	require.Equal(t, 400, f.request("GET", path+"/ws", "", f.ownerToken).Code)
}

type testStubEngine struct{}

func (testStubEngine) Run(context.Context, paperscrdt.Request) (paperscrdt.Checkpoint, error) {
	return paperscrdt.Checkpoint{}, paperscrdt.ErrUnavailable
}

func TestCollabFrameBoundsAndSchema(t *testing.T) {
	good := collabClientFrame{Type: "update", Epoch: uuid.New(), RequestID: uuid.New(), Update: []byte{1, 2}}
	raw, err := json.Marshal(good)
	require.NoError(t, err)
	got, err := parseCollabFrame(raw)
	require.NoError(t, err)
	require.Equal(t, good, got)
	for _, bad := range [][]byte{nil, bytes.Repeat([]byte("x"), collabFrameLimit+1), append(append([]byte{}, raw...), []byte(" {}")...), []byte(strings.Replace(string(raw), `"update":`, `"role":"owner","update":`, 1))} {
		_, err = parseCollabFrame(bad)
		require.Error(t, err)
	}
	for _, bad := range []collabClientFrame{
		{Type: "update", Epoch: good.Epoch, RequestID: good.RequestID},
		{Type: "update", Epoch: good.Epoch, RequestID: good.RequestID, Update: make([]byte, paperscrdt.MaxUpdate+1)},
		{Type: "sync", Epoch: good.Epoch, RequestID: good.RequestID, Update: []byte{1}},
		{Type: "sync", Epoch: uuid.Nil, RequestID: good.RequestID},
		{Type: "presence", Epoch: good.Epoch, RequestID: good.RequestID, Selection: &collabSelection{Anchor: make([]byte, maxRelativePositionBytes+1), Head: []byte{1}}},
	} {
		raw, _ = json.Marshal(bad)
		_, err = parseCollabFrame(raw)
		require.Error(t, err)
	}
}

func dialGateway(t *testing.T, server, path, token string, epoch uuid.UUID) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server, "http") + path + "/ws"
	if epoch != uuid.Nil {
		url += "?epoch=" + epoch.String()
	}
	header := http.Header{"Authorization": []string{"Bearer " + token}}
	d := websocket.Dialer{HandshakeTimeout: 12 * time.Second, Subprotocols: []string{"atoman"}}
	conn, res, err := d.Dial(url, header)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	if res != nil && err != nil {
		_ = res.Body.Close()
	}
	return conn, res, err
}
func readGateway(t *testing.T, c *websocket.Conn) collabServerFrame {
	t.Helper()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(15*time.Second)))
	var f collabServerFrame
	require.NoError(t, c.ReadJSON(&f))
	return f
}
func waitAck(t *testing.T, c *websocket.Conn, id uuid.UUID) collabServerFrame {
	t.Helper()
	for i := 0; i < 10; i++ {
		f := readGateway(t, c)
		if f.RequestID != nil && *f.RequestID == id {
			return f
		}
	}
	t.Fatal("missing committed acknowledgement")
	return collabServerFrame{}
}
func awaitSequence(t *testing.T, c *websocket.Conn, seq int64) collabServerFrame {
	t.Helper()
	for i := 0; i < 10; i++ {
		f := readGateway(t, c)
		if f.Type == "state" && f.Sequence >= seq {
			return f
		}
	}
	t.Fatal("missing committed state")
	return collabServerFrame{}
}
func expectClose(t *testing.T, c *websocket.Conn, code string) {
	t.Helper()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(15*time.Second)))
	for i := 0; i < 10; i++ {
		_, _, err := c.ReadMessage()
		if err == nil {
			continue
		}
		var closed *websocket.CloseError
		require.True(t, errors.As(err, &closed), "expected policy close: %v", err)
		require.Equal(t, websocket.ClosePolicyViolation, closed.Code)
		require.Equal(t, code, closed.Text)
		return
	}
	t.Fatal("connection not closed")
}
func TestCollabGatewayDurableSyncAndRevocation(t *testing.T) {
	engine := testCollabEngine(t)
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	other := authctx.CurrentUser{ID: f.other.UUID, Role: authctx.RoleUser}
	pid, fid := f.project.ID, f.project.MainFileID
	require.NoError(t, f.svc.configureCollaboration(engine))
	server := httptest.NewServer(f.api)
	defer server.Close()
	path := collabPath(f)
	_, resp, err := dialGateway(t, server.URL, path, f.otherToken, uuid.Nil)
	require.Error(t, err)
	require.Equal(t, 404, resp.StatusCode)
	_, err = f.svc.SetMember(owner, pid, other.ID, RoleProjectEditor)
	require.NoError(t, err)
	a, _, err := dialGateway(t, server.URL, path, f.ownerToken, uuid.Nil)
	require.NoError(t, err)
	defer a.Close()
	first := readGateway(t, a)
	require.Equal(t, "state", first.Type)
	require.Equal(t, collabProtocol, first.Protocol)
	require.Equal(t, RoleProjectOwner, first.Role)
	b, _, err := dialGateway(t, server.URL, path, f.otherToken, first.Epoch)
	require.NoError(t, err)
	defer b.Close()
	second := readGateway(t, b)
	require.Equal(t, RoleProjectEditor, second.Role)
	require.Equal(t, first.State, second.State)
	require.Equal(t, first.Epoch, second.Epoch)
	deltaA := peerUpdate(t, first.State, 0, 0, "A "+string(rune(0x4e2d))+" ")
	deltaB := peerUpdate(t, second.State, 0, 0, "B "+string(rune(0x1f600))+" ")
	reqA := uuid.New()
	require.NoError(t, a.WriteJSON(collabClientFrame{Type: "update", RequestID: reqA, Epoch: first.Epoch, Update: deltaA}))
	ackA := waitAck(t, a, reqA)
	require.Equal(t, "state", ackA.Type)
	require.Greater(t, ackA.Sequence, first.Sequence)
	receivedA := awaitSequence(t, b, ackA.Sequence)
	require.Equal(t, ackA.State, receivedA.State)
	reqB := uuid.New()
	require.NoError(t, b.WriteJSON(collabClientFrame{Type: "update", RequestID: reqB, Epoch: first.Epoch, Update: deltaB}))
	ackB := waitAck(t, b, reqB)
	require.Equal(t, "state", ackB.Type)
	require.Greater(t, ackB.Sequence, ackA.Sequence)
	receivedB := awaitSequence(t, a, ackB.Sequence)
	require.Equal(t, ackB.State, receivedB.State)
	stored, err := f.svc.GetFile(owner, pid, fid)
	require.NoError(t, err)
	require.Contains(t, stored.Content, "A "+string(rune(0x4e2d))+" ")
	require.Contains(t, stored.Content, "B "+string(rune(0x1f600))+" ")
	require.Equal(t, ackB.FileRevision, stored.Revision)
	// Session revocation and role downgrade are detected without client input.
	_, err = f.svc.SetMember(owner, pid, other.ID, RoleProjectViewer)
	require.NoError(t, err)
	expectClose(t, b, "papers.collab_role_changed")
	viewer, _, err := dialGateway(t, server.URL, path, f.otherToken, first.Epoch)
	require.NoError(t, err)
	defer viewer.Close()
	v := readGateway(t, viewer)
	require.Equal(t, RoleProjectViewer, v.Role)
	require.Equal(t, ackB.State, v.State)
	require.NoError(t, viewer.WriteJSON(collabClientFrame{Type: "update", RequestID: uuid.New(), Epoch: first.Epoch, Update: deltaB}))
	expectClose(t, viewer, "papers.read_only")
	_, err = f.svc.SetMember(owner, pid, other.ID, RoleProjectEditor)
	require.NoError(t, err)
	revoked, _, err := dialGateway(t, server.URL, path, f.otherToken, first.Epoch)
	require.NoError(t, err)
	defer revoked.Close()
	_ = readGateway(t, revoked)
	resolved, err := authsession.New(f.db).Authenticate(f.otherToken, authsession.KindAPI)
	require.NoError(t, err)
	require.NoError(t, authsession.New(f.db).RevokeUserSession(other.ID, resolved.Session.ID))
	expectClose(t, revoked, "auth.unauthorized")
	_, err = f.svc.SaveFile(owner, pid, fid, "ordinary save", stored.Revision)
	require.NoError(t, err)
	expectClose(t, a, "papers.collab_epoch_stale")
	_, resp, err = dialGateway(t, server.URL, path, f.ownerToken, first.Epoch)
	require.Error(t, err)
	require.Equal(t, 409, resp.StatusCode)
	fresh, _, err := dialGateway(t, server.URL, path, f.ownerToken, uuid.Nil)
	require.NoError(t, err)
	defer fresh.Close()
	newEpoch := readGateway(t, fresh)
	require.NotEqual(t, first.Epoch, newEpoch.Epoch)
	current, err := f.svc.GetFile(owner, pid, fid)
	require.NoError(t, err)
	require.Equal(t, "ordinary save", current.Content)
}

func TestCollabGatewaySessionRevocationBeforeCommit(t *testing.T) {
	e := testCollabEngine(t)
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	pid, fid := f.project.ID, f.project.MainFileID
	resolved, err := authsession.New(f.db).Authenticate(f.ownerToken, authsession.KindAPI)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), collabSessionKey{}, resolved.Session.ID), 20*time.Second)
	defer cancel()
	store := &collaborationStore{service: f.svc, engine: e}
	seed, err := store.Open(ctx, owner, pid, fid)
	require.NoError(t, err)
	update := peerUpdate(t, seed.Checkpoint.State, 0, 0, "not accepted ")
	barrier := mergeBarrier{e: e, ready: make(chan struct{}, 1), release: make(chan struct{})}
	racing := &collaborationStore{service: f.svc, engine: barrier}
	result := make(chan error, 1)
	go func() { _, err := racing.Apply(ctx, owner, pid, fid, seed.Epoch, update); result <- err }()
	select {
	case <-barrier.ready:
	case <-ctx.Done():
		close(barrier.release)
		t.Fatal("merge not reached")
	}
	require.NoError(t, authsession.New(f.db).RevokeUserSession(owner.ID, resolved.Session.ID))
	close(barrier.release)
	collabCode(t, <-result, "auth.unauthorized")
	row, err := f.svc.GetFile(owner, pid, fid)
	require.NoError(t, err)
	require.Equal(t, seed.Checkpoint.Text, row.Content)
	require.Equal(t, seed.FileRevision, row.Revision)
	var cp model.PaperCollaborativeDocument
	require.NoError(t, f.db.Where("file_id=?", fid).First(&cp).Error)
	require.Equal(t, seed.Sequence, cp.Sequence)
}

func TestCollabGatewayCookieOriginAndCrossFile(t *testing.T) {
	f := newCompileHTTPFixture(t, false)
	require.NoError(t, f.svc.configureCollaboration(testStubEngine{}))
	server := httptest.NewServer(f.api)
	defer server.Close()
	web, err := authsession.New(f.db).Create(f.owner.UUID, authsession.KindWeb)
	require.NoError(t, err)
	path := collabPath(f)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + path + "/ws"
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Subprotocols: []string{"atoman"}}
	for _, origin := range []string{"", "https://untrusted.example.invalid"} {
		header := http.Header{"Cookie": []string{"atoman_session=" + web.Token}}
		if origin != "" {
			header.Set("Origin", origin)
		}
		conn, response, err := d.Dial(wsURL, header)
		if conn != nil {
			conn.Close()
		}
		require.Error(t, err)
		require.NotNil(t, response)
		response.Body.Close()
		require.Equal(t, 403, response.StatusCode)
	}
	// An authenticated member of one project cannot join an arbitrary file.
	invalidPath := f.base() + "/files/" + uuid.NewString() + "/collaboration"
	_, response, err := dialGateway(t, server.URL, invalidPath, f.ownerToken, uuid.Nil)
	require.Error(t, err)
	require.NotNil(t, response)
	require.Equal(t, 404, response.StatusCode)
	require.Zero(t, f.svc.collaboration.count)
}
