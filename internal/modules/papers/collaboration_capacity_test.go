package papers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type capacityPeer struct {
	conn    *websocket.Conn
	initial collabServerFrame
	file    int
	token   string
}
type capacityResult struct {
	index   int
	retries int
	elapsed time.Duration
	err     error
}

// Bounded test of the real gateway with the production admission limits.
// Uses synthetic manuscript content, isolated DB and the actual Yjs runtime.
// No production HTTP route, account, project, credential or database is used.
func TestCollabGatewayCapacityAndConcurrentCommit(t *testing.T) {
	engine := testCollabEngine(t)
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	pid := f.project.ID
	tokens := []string{f.ownerToken, f.otherToken}
	require.NoError(t, f.svc.configureCollaboration(engine))
	_, err := f.svc.SetMember(owner, pid, f.other.UUID, RoleProjectEditor)
	require.NoError(t, err)
	// Four accounts fill two rooms; fifth account probes room/global limits.
	for i := 0; i < 3; i++ {
		u := model.User{Username: "capacity-" + uuid.NewString(), Email: uuid.NewString() + "@example.net", Password: "fake", Role: authctx.RoleUser, IsActive: true}
		require.NoError(t, f.db.Create(&u).Error)
		require.NoError(t, f.db.Create(&model.PaperAccessGrant{UserID: u.UUID, Status: "active"}).Error)
		_, err = f.svc.SetMember(owner, pid, u.UUID, RoleProjectEditor)
		require.NoError(t, err)
		token, err := authsession.New(f.db).Create(u.UUID, authsession.KindAPI)
		require.NoError(t, err)
		tokens = append(tokens, token.Token)
	}
	files := []uuid.UUID{f.project.MainFileID}
	for i := 1; i <= 2; i++ {
		file, err := f.svc.CreateFile(owner, pid, fmt.Sprintf("chapter-%d.tex", i), "capacity fixture")
		require.NoError(t, err)
		files = append(files, file.ID)
	}
	server := httptest.NewServer(f.api)
	defer server.Close()
	path := func(i int) string { return f.base() + "/files/" + files[i].String() + "/collaboration" }
	var peers []capacityPeer
	defer func() {
		for _, p := range peers {
			_ = p.conn.Close()
		}
	}()
	connect := func(file, account int) capacityPeer {
		c, _, err := dialGateway(t, server.URL, path(file), tokens[account], uuid.Nil)
		require.NoError(t, err)
		state := readGateway(t, c)
		require.Equal(t, "state", state.Type)
		return capacityPeer{conn: c, initial: state, file: file, token: tokens[account]}
	}
	denied := func(file, account int) {
		c, res, err := dialGateway(t, server.URL, path(file), tokens[account], uuid.Nil)
		if c != nil {
			_ = c.Close()
		}
		require.Error(t, err)
		require.NotNil(t, res)
		require.Equal(t, http.StatusTooManyRequests, res.StatusCode)
	}
	for i := 0; i < collabMaxPerAccount; i++ {
		peers = append(peers, connect(0, 0))
	}
	denied(1, 0) // account limit, independent of room/global limits
	for i := 0; i < collabMaxPerAccount; i++ {
		peers = append(peers, connect(0, 1))
	}
	denied(0, 4) // room limit, independent of account/global limits
	for _, account := range []int{2, 3} {
		for i := 0; i < collabMaxPerAccount; i++ {
			peers = append(peers, connect(1, account))
		}
	}
	require.Len(t, peers, collabMaxConnections)
	denied(2, 4) // global limit, independent of account/room limits
	t.Logf("Admission: %d accepted, account/room/global overflows rejected", len(peers))
	// Independent updates from the same original checkpoint, including
	// simultaneous writers in different files. Prepare synthetic deltas first.
	deltas := make([][]byte, len(peers))
	for i, p := range peers {
		deltas[i] = peerUpdate(t, p.initial.State, 0, 0, fmt.Sprintf("CAP%02d ", i))
	}
	// Two waves of six, followed by all twelve. This probes sustained
	// concurrency with the current browser's retry budget (400/800/1600 ms).
	for wave := 0; wave < 3; wave++ {
		count := 6
		if wave == 2 {
			count = len(peers)
			for i, p := range peers {
				deltas[i] = peerUpdate(t, p.initial.State, 0, 0, fmt.Sprintf("FULL%02d ", i))
			}
		}
		start := make(chan struct{})
		results := make(chan capacityResult, count)
		for j := 0; j < count; j++ {
			i := j
			if wave < 2 {
				i = wave*3 + j%3 + (j/3)*6
			}
			p := peers[i]
			go func(i int, p capacityPeer) {
				<-start
				begun := time.Now()
				result := capacityResult{index: i}
				defer func() { result.elapsed = time.Since(begun); results <- result }()
				request := collabClientFrame{Type: "update", Epoch: p.initial.Epoch, RequestID: uuid.New(), Update: deltas[i]}
				for attempt := 0; attempt <= 3; attempt++ {
					if err := p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
						result.err = err
						return
					}
					if err := p.conn.WriteJSON(request); err != nil {
						result.err = err
						return
					}
					for {
						var frame collabServerFrame
						if err := p.conn.SetReadDeadline(time.Now().Add(18 * time.Second)); err != nil {
							result.err = err
							return
						}
						if err := p.conn.ReadJSON(&frame); err != nil {
							result.err = err
							return
						}
						if frame.RequestID == nil || *frame.RequestID != request.RequestID {
							continue
						}
						if frame.Type == "state" {
							return
						}
						if frame.Type != "error" || !frame.Retryable || attempt == 3 {
							result.err = fmt.Errorf("update not acknowledged: %s, retryable=%t, attempt=%d", frame.Code, frame.Retryable, attempt+1)
							return
						}
						result.retries++
						time.Sleep(time.Duration(400*(1<<attempt)) * time.Millisecond)
						break
					}
				}
			}(i, p)
		}
		close(start)
		var failures []string
		totalRetries := 0
		var longest time.Duration
		for j := 0; j < count; j++ {
			r := <-results
			totalRetries += r.retries
			if r.elapsed > longest {
				longest = r.elapsed
			}
			if r.err != nil {
				failures = append(failures, fmt.Sprintf("peer %d: %v", r.index, r.err))
			}
		}
		t.Logf("Wave %d: %d simultaneous writers, retries=%d, max acknowledgement=%s", wave+1, count, totalRetries, longest)
		require.Empty(t, failures)
	}
	// Every accepted delta is present exactly once, with a canonical
	// revision per accepted update. Observe latest checkpoint on each socket.
	for i, file := range files[:2] {
		row, err := f.svc.GetFile(owner, pid, file)
		require.NoError(t, err)
		require.Equal(t, peers[i*6].initial.FileRevision+12, row.Revision)
		for j := 0; j < 6; j++ {
			require.Equal(t, 1, strings.Count(row.Content, fmt.Sprintf("CAP%02d ", i*6+j)))
			require.Equal(t, 1, strings.Count(row.Content, fmt.Sprintf("FULL%02d ", i*6+j)))
		}
		time.Sleep(200 * time.Millisecond)
		for _, p := range peers[i*6 : i*6+6] {
			id := uuid.New()
			require.NoError(t, p.conn.SetWriteDeadline(time.Now().Add(5*time.Second)))
			require.NoError(t, p.conn.WriteJSON(collabClientFrame{Type: "sync", Epoch: p.initial.Epoch, RequestID: id}))
			var state collabServerFrame
			for n := 0; n < 64; n++ {
				state = readGateway(t, p.conn)
				if state.RequestID != nil && *state.RequestID == id {
					break
				}
			}
			require.NotNil(t, state.RequestID)
			require.Equal(t, id, *state.RequestID)
			require.Equal(t, row.Revision, state.FileRevision)
			require.Equal(t, p.initial.Sequence+12, state.Sequence)
		}
	}
	// Disconnects free all quotas; the rejected fifth account can join.
	for _, p := range peers {
		_ = p.conn.Close()
	}
	g := f.svc.collaboration
	require.Eventually(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.count == 0 && len(g.accounts) == 0 && len(g.rooms) == 0 && len(g.roomGates) == 0 && len(g.engineGate) == 0
	}, 5*time.Second, 20*time.Millisecond)
	fresh := connect(2, 4)
	_ = fresh.conn.Close()
	t.Log("All accepted deltas committed exactly once; all admission quotas recovered")
}

// Verify cancellation and reclamation without creating an unbounded pool.
func TestCollabGatewayRoomGateCancellation(t *testing.T) {
	g := &collaborationGateway{engineGate: make(chan struct{}, 2), roomGates: make(map[roomKey]*collabRoomGate)}
	a, b, c := roomKey{uuid.New(), uuid.New()}, roomKey{uuid.New(), uuid.New()}, roomKey{uuid.New(), uuid.New()}
	one, ok := g.reserveEngine(context.Background(), a)
	require.True(t, ok)
	defer one()
	two, ok := g.reserveEngine(context.Background(), b)
	require.True(t, ok)
	defer two()
	for _, key := range []roomKey{a, c} {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		_, ok := g.reserveEngine(ctx, key)
		cancel()
		require.False(t, ok)
	}
	require.Len(t, g.engineGate, 2)
	require.Len(t, g.roomGates, 2)
	one()
	one()
	three, ok := g.reserveEngine(context.Background(), c)
	require.True(t, ok)
	three()
	two()
	require.Empty(t, g.roomGates)
	require.Empty(t, g.engineGate)
}
