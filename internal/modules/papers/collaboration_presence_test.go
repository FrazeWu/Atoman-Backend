package papers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestCollabPresenceRegistryIsolationAndCleanup(t *testing.T) {
	r := newPresenceRegistry()
	u := authctx.CurrentUser{ID: uuid.New(), Username: "member" + string(rune(0x202e))}
	key := presenceKey{room: roomKey{uuid.New(), uuid.New()}, epoch: uuid.New()}
	other := presenceKey{room: key.room, epoch: uuid.New()}
	id, ok := r.join(key, u, RoleProjectEditor)
	require.True(t, ok)
	_, peers := r.snapshot(key)
	require.Len(t, peers, 1)
	require.Equal(t, "member", peers[0].Name)
	require.Equal(t, u.ID, peers[0].UserID)
	_, peers = r.snapshot(other)
	require.Empty(t, peers)
	pos := &collabSelection{Anchor: []byte{1}, Head: []byte{2}}
	require.True(t, r.update(key, id, pos))
	require.False(t, r.update(key, id, pos))
	require.False(t, r.update(other, id, pos))
	require.False(t, r.update(key, uuid.New(), pos))
	require.False(t, r.update(key, id, &collabSelection{Anchor: []byte{1}}))
	pos.Anchor[0] = 3
	version, peers := r.snapshot(key)
	require.Equal(t, []byte{1}, peers[0].Selection.Anchor)
	peers[0].Selection.Anchor[0] = 9
	v2, peers := r.snapshot(key)
	require.Equal(t, version, v2)
	require.Equal(t, []byte{1}, peers[0].Selection.Anchor)
	require.True(t, r.update(key, id, nil))
	_, peers = r.snapshot(key)
	require.Nil(t, peers[0].Selection)
	require.True(t, r.leave(key, id))
	require.False(t, r.leave(key, id))
	require.Empty(t, r.rooms)
}
func TestCollabPresenceFramesCannotForgeIdentity(t *testing.T) {
	f := collabClientFrame{Type: "presence", RequestID: uuid.New(), Epoch: uuid.New(), Selection: &collabSelection{Anchor: []byte{1}, Head: []byte{2}}}
	raw, err := json.Marshal(f)
	require.NoError(t, err)
	got, err := parseCollabFrame(raw)
	require.NoError(t, err)
	require.Equal(t, f, got)
	_, err = parseCollabFrame([]byte(strings.Replace(string(raw), `"selection":`, `"name":"forged","selection":`, 1)))
	require.Error(t, err)
	f.Update = []byte{1}
	raw, _ = json.Marshal(f)
	_, err = parseCollabFrame(raw)
	require.Error(t, err)
	f.Update = nil
	f.Selection = &collabSelection{Anchor: make([]byte, 129), Head: []byte{1}}
	raw, _ = json.Marshal(f)
	_, err = parseCollabFrame(raw)
	require.Error(t, err)
	f.Selection = nil
	raw, _ = json.Marshal(f)
	_, err = parseCollabFrame(raw)
	require.NoError(t, err)
	f.Type = "sync"
	f.Selection = &collabSelection{Anchor: []byte{1}, Head: []byte{1}}
	raw, _ = json.Marshal(f)
	_, err = parseCollabFrame(raw)
	require.Error(t, err)
}
func dialPresence(t *testing.T, server, path, token string, epoch uuid.UUID) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server, "http") + path + "/ws?presence=1"
	if epoch != uuid.Nil {
		url += "&epoch=" + epoch.String()
	}
	d := websocket.Dialer{HandshakeTimeout: 12 * time.Second, Subprotocols: []string{"atoman"}}
	conn, res, err := d.Dial(url, http.Header{"Authorization": []string{"Bearer " + token}})
	if err != nil && res != nil {
		res.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
func waitPresence(t *testing.T, conn *websocket.Conn, accept func(collabServerFrame) bool) collabServerFrame {
	t.Helper()
	for i := 0; i < 15; i++ {
		frame := readGateway(t, conn)
		if frame.Type == "presence" && accept(frame) {
			return frame
		}
	}
	t.Fatal("presence update not received")
	return collabServerFrame{}
}
func TestCollabPresenceLiveJoinUpdateAndRevocation(t *testing.T) {
	e := testCollabEngine(t)
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	other := authctx.CurrentUser{ID: f.other.UUID, Role: authctx.RoleUser}
	pid, fid := f.project.ID, f.project.MainFileID
	require.NoError(t, f.svc.configureCollaboration(e))
	_, err := f.svc.SetMember(owner, pid, other.ID, RoleProjectEditor)
	require.NoError(t, err)
	server := httptest.NewServer(f.api)
	defer server.Close()
	path := collabPath(f)
	a := dialPresence(t, server.URL, path, f.ownerToken, uuid.Nil)
	defer a.Close()
	initial := readGateway(t, a)
	require.Equal(t, "state", initial.Type)
	alone := waitPresence(t, a, func(f collabServerFrame) bool { return len(f.Peers) == 1 })
	require.Equal(t, owner.ID, alone.Peers[0].UserID)
	require.Equal(t, *alone.ConnectionID, alone.Peers[0].ConnectionID)
	b := dialPresence(t, server.URL, path, f.otherToken, initial.Epoch)
	defer b.Close()
	require.Equal(t, "state", readGateway(t, b).Type)
	joined := waitPresence(t, a, func(f collabServerFrame) bool { return len(f.Peers) == 2 })
	otherView := waitPresence(t, b, func(f collabServerFrame) bool { return len(f.Peers) == 2 })
	require.NotEqual(t, *joined.ConnectionID, *otherView.ConnectionID)
	require.Equal(t, joined.Peers, otherView.Peers)
	// Opaque Yjs positions are bounded, never treated as document content.
	sel := &collabSelection{Anchor: []byte{1, 2, 3}, Head: []byte{4, 5}}
	require.NoError(t, b.WriteJSON(collabClientFrame{Type: "presence", RequestID: uuid.New(), Epoch: initial.Epoch, Selection: sel}))
	moved := waitPresence(t, a, func(f collabServerFrame) bool {
		for _, p := range f.Peers {
			if p.UserID == other.ID && p.Selection != nil {
				return true
			}
		}
		return false
	})
	for _, peer := range moved.Peers {
		if peer.UserID == other.ID {
			require.Equal(t, RoleProjectEditor, peer.Role)
			require.Equal(t, sel, peer.Selection)
		}
	}
	file, err := f.svc.GetFile(owner, pid, fid)
	require.NoError(t, err)
	require.Equal(t, initial.FileRevision, file.Revision)
	time.Sleep(400 * time.Millisecond)
	require.NoError(t, b.WriteJSON(collabClientFrame{Type: "presence", RequestID: uuid.New(), Epoch: initial.Epoch}))
	_ = waitPresence(t, a, func(f collabServerFrame) bool {
		for _, p := range f.Peers {
			if p.UserID == other.ID {
				return p.Selection == nil
			}
		}
		return false
	})
	_, err = f.svc.SetMember(owner, pid, other.ID, RoleProjectViewer)
	require.NoError(t, err)
	expectClose(t, b, "papers.collab_role_changed")
	removed := waitPresence(t, a, func(f collabServerFrame) bool { return len(f.Peers) == 1 })
	require.Equal(t, owner.ID, removed.Peers[0].UserID)
	require.NoError(t, a.Close())
	require.Eventually(t, func() bool {
		_, peers := f.svc.collaboration.presence.snapshot(presenceKey{room: roomKey{pid, fid}, epoch: initial.Epoch})
		return len(peers) == 0
	}, 5*time.Second, 20*time.Millisecond)
}
