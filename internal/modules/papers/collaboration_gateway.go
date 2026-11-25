package papers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"atoman/internal/collab"
	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/platform/httpx"
	"atoman/internal/platform/paperscrdt"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

const (
	collabProtocol        = "papers-crdt-v1"
	collabMaxConnections  = 12
	collabMaxPerAccount   = 3
	collabMaxPerRoom      = 6
	collabFrameLimit      = 384 << 10
	collabPollInterval    = 2 * time.Second
	collabWriteTimeout    = 5 * time.Second
	collabSessionLifetime = 30 * time.Minute
)

type collabSessionKey struct{}
type roomKey struct{ project, file uuid.UUID }

func (k roomKey) String() string { return "papers:" + k.project.String() + ":" + k.file.String() }

type collabMeta struct {
	epoch                                   uuid.UUID
	sequence, fileRevision, projectRevision int64
	role                                    string
}
type collaborationGateway struct {
	store      *collaborationStore
	signals    *collab.RoomSignals
	mu         sync.Mutex
	count      int
	accounts   map[uuid.UUID]int
	rooms      map[roomKey]int
	engineGate chan struct{}
	roomGates  map[roomKey]*collabRoomGate
	presence   *presenceRegistry
}

// configureCollaboration is startup-only dependency injection. Production
// reaches it only through the approved isolated Unix worker startup probe.
func (s *Service) configureCollaboration(engine collabEngine) error {
	if s == nil || s.db == nil || engine == nil || s.collaboration != nil {
		return paperscrdt.ErrUnavailable
	}
	s.collaboration = &collaborationGateway{store: &collaborationStore{service: s, engine: engine}, signals: collab.NewRoomSignals(), accounts: make(map[uuid.UUID]int), rooms: make(map[roomKey]int), engineGate: make(chan struct{}, 2), roomGates: make(map[roomKey]*collabRoomGate), presence: newPresenceRegistry()}
	return nil
}

func (g *collaborationGateway) admit(uid uuid.UUID, key roomKey) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.count >= collabMaxConnections || g.accounts[uid] >= collabMaxPerAccount || g.rooms[key] >= collabMaxPerRoom {
		return nil, false
	}
	g.count++
	g.accounts[uid]++
	g.rooms[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.count--
			g.accounts[uid]--
			g.rooms[key]--
			if g.accounts[uid] == 0 {
				delete(g.accounts, uid)
			}
			if g.rooms[key] == 0 {
				delete(g.rooms, key)
			}
		})
	}, true
}

// Room gates serialize merges of the same checkpoint before acquiring a
// global decoder slot. Waiters are bounded by admitted sockets; no extra
// request workers or unbounded queue are created. Different files can use
// the two global slots independently.
type collabRoomGate struct {
	slot chan struct{}
	refs int
}

func (g *collaborationGateway) reserveEngine(ctx context.Context, key roomKey) (func(), bool) {
	wait, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	g.mu.Lock()
	gate := g.roomGates[key]
	if gate == nil {
		gate = &collabRoomGate{slot: make(chan struct{}, 1)}
		g.roomGates[key] = gate
	}
	gate.refs++
	g.mu.Unlock()
	unref := func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		gate.refs--
		if gate.refs == 0 {
			delete(g.roomGates, key)
		}
	}
	select {
	case gate.slot <- struct{}{}:
	case <-wait.Done():
		unref()
		return nil, false
	}
	select {
	case g.engineGate <- struct{}{}:
	case <-wait.Done():
		<-gate.slot
		unref()
		return nil, false
	}
	if wait.Err() != nil {
		<-g.engineGate
		<-gate.slot
		unref()
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(func() { <-g.engineGate; <-gate.slot; unref() }) }, true
}

// Metadata polling never loads or hashes a manuscript or large Yjs state.
// The full state is reread under the same authorization only on changes.
func (c *collaborationStore) inspect(ctx context.Context, user authctx.CurrentUser, key roomKey, epoch uuid.UUID) (collabMeta, error) {
	var m collabMeta
	err := c.database(ctx).Transaction(func(tx *gorm.DB) error {
		p, err := lockCollabAccess(tx, c.service, user, key.project, false)
		if err != nil {
			return err
		}
		var f model.PaperFile
		err = tx.Select("id", "path", "kind", "revision", "sha256").Where("id=? AND project_id=?", key.file, key.project).First(&f).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("papers.file_not_found", "File not found")
		}
		if err != nil {
			return err
		}
		if f.Kind != "text" {
			return apperr.BadRequest("papers.binary_read_only", "Only text files support collaboration")
		}
		m.role = p.AccessRole
		m.fileRevision = f.Revision
		m.projectRevision = p.HeadRevision
		if epoch == uuid.Nil {
			return nil
		}
		var row model.PaperCollaborativeDocument
		err = tx.Select("epoch", "sequence", "file_revision", "file_sha256", "path").Where("file_id=? AND project_id=?", key.file, key.project).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return staleEpoch()
		}
		if err != nil {
			return err
		}
		if row.Epoch != epoch || row.FileRevision != f.Revision || row.FileSHA256 != f.SHA256 || row.Path != f.Path || row.Sequence < 1 {
			return staleEpoch()
		}
		m.epoch = row.Epoch
		m.sequence = row.Sequence
		return nil
	})
	return m, safeCollabStorageError(err)
}

func (g *collaborationGateway) join(ctx context.Context, user authctx.CurrentUser, key roomKey, epoch uuid.UUID) (collaborationState, collabMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out collaborationState
	meta, err := g.store.inspect(ctx, user, key, epoch)
	if err != nil {
		return out, meta, err
	}
	if epoch != uuid.Nil {
		out, err = g.store.Read(ctx, user, key.project, key.file, epoch)
	} else if meta.role == RoleProjectViewer {
		base, loadErr := g.store.load(ctx, user, key.project, key.file, false)
		if loadErr != nil {
			return out, meta, loadErr
		}
		out, err = base.checkpoint() // viewer cannot create an epoch
	} else {
		release, ok := g.reserveEngine(ctx, key)
		if !ok {
			return out, meta, collabRetry()
		}
		out, err = g.store.Open(ctx, user, key.project, key.file)
		release()
	}
	if err != nil {
		return out, meta, err
	}
	next, err := g.store.inspect(ctx, user, key, out.Epoch)
	if err != nil {
		return out, next, err
	}
	if next.role != meta.role {
		return out, next, apperr.Forbidden("papers.collab_role_changed", "Project role changed")
	}
	// A concurrent writer may commit between Read and inspect. The next
	// notification/poll will catch up using the monotonic sequence.
	return out, next, nil
}

type collabClientFrame struct {
	Type      string           `json:"type"`
	RequestID uuid.UUID        `json:"request_id"`
	Epoch     uuid.UUID        `json:"epoch"`
	Update    []byte           `json:"update,omitempty"`
	Selection *collabSelection `json:"selection,omitempty"`
}
type collabServerFrame struct {
	Type            string       `json:"type"`
	Protocol        string       `json:"protocol,omitempty"`
	Epoch           uuid.UUID    `json:"epoch"`
	RequestID       *uuid.UUID   `json:"request_id,omitempty"`
	Sequence        int64        `json:"sequence,omitempty"`
	FileRevision    int64        `json:"file_revision,omitempty"`
	ProjectRevision int64        `json:"project_revision,omitempty"`
	Role            string       `json:"role,omitempty"`
	State           []byte       `json:"state,omitempty"`
	Vector          []byte       `json:"vector,omitempty"`
	Code            string       `json:"code,omitempty"`
	Retryable       bool         `json:"retryable,omitempty"`
	ConnectionID    *uuid.UUID   `json:"connection_id,omitempty"`
	PresenceVersion uint64       `json:"presence_version,omitempty"`
	Peers           []collabPeer `json:"peers,omitempty"`
}

func parseCollabFrame(raw []byte) (collabClientFrame, error) {
	var f collabClientFrame
	if len(raw) == 0 || len(raw) > collabFrameLimit {
		return f, paperscrdt.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&f) != nil {
		return f, paperscrdt.ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF || f.Epoch == uuid.Nil || f.RequestID == uuid.Nil {
		return f, paperscrdt.ErrInvalid
	}
	switch f.Type {
	case "update":
		if f.Selection != nil || len(f.Update) == 0 || len(f.Update) > paperscrdt.MaxUpdate {
			return f, paperscrdt.ErrInvalid
		}
	case "sync":
		if f.Selection != nil || len(f.Update) != 0 {
			return f, paperscrdt.ErrInvalid
		}
	case "presence":
		if len(raw) > 2048 || len(f.Update) != 0 || !f.Selection.valid() {
			return f, paperscrdt.ErrInvalid
		}
	default:
		return f, paperscrdt.ErrInvalid
	}
	return f, nil
}
func stateFrame(state collaborationState, role string, requestID *uuid.UUID) collabServerFrame {
	return collabServerFrame{Type: "state", Protocol: collabProtocol, Epoch: state.Epoch, RequestID: requestID, Sequence: state.Sequence, FileRevision: state.FileRevision, ProjectRevision: state.ProjectRevision, Role: role, State: state.Checkpoint.State, Vector: state.Checkpoint.Vector}
}
func sendCollab(conn *websocket.Conn, frame collabServerFrame) error {
	if conn.SetWriteDeadline(time.Now().Add(collabWriteTimeout)) != nil {
		return paperscrdt.ErrUnavailable
	}
	return conn.WriteJSON(frame)
}
func collabErrorCode(err error) string {
	switch code := apperr.FromError(err).Code; code {
	case "auth.unauthorized", "papers.project_not_found", "papers.file_not_found", "papers.invite_required", "papers.read_only", "papers.collab_role_changed", "papers.collab_epoch_stale", "papers.collab_checkpoint_invalid", "papers.collab_retry", "papers.collab_invalid_update", "papers.collab_quota", "papers.collab_unavailable", "papers.collab_storage_failed", "papers.compile_source_limit":
		return code
	default:
		return "papers.collab_unavailable"
	}
}
func terminalCollabError(code string) bool {
	switch code {
	case "papers.collab_retry", "papers.collab_invalid_update", "papers.collab_quota", "papers.collab_unavailable", "papers.collab_storage_failed", "papers.compile_source_limit":
		return false
	default:
		return true
	}
}
func closeCollab(conn *websocket.Conn, code string) {
	// Close reason is a fixed machine code, never source, SQL or user data.
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, code), time.Now().Add(time.Second))
}

// serve is single-writer: only its main loop sends application messages.
// One reader and one in-flight request are bounded per admitted socket.
func (g *collaborationGateway) serve(ctx context.Context, conn *websocket.Conn, user authctx.CurrentUser, key roomKey, state collaborationState, meta collabMeta, enablePresence bool) {
	ctx, cancel := context.WithTimeout(ctx, collabSessionLifetime)
	defer cancel()
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	notify, unsubscribe := g.signals.Subscribe(key.String())
	defer unsubscribe()
	if sendCollab(conn, stateFrame(state, meta.role, nil)) != nil {
		return
	}
	sentSeq, sentProject := state.Sequence, state.ProjectRevision
	epoch, role := state.Epoch, meta.role
	pk := presenceKey{room: key, epoch: epoch}
	var connectionID uuid.UUID
	var presenceVersion uint64
	if enablePresence {
		var ok bool
		connectionID, ok = g.presence.join(pk, user, role)
		if !ok {
			closeCollab(conn, "papers.collab_limit")
			return
		}
		defer func() {
			if g.presence.leave(pk, connectionID) {
				g.signals.Notify(key.String())
			}
		}()
		g.signals.Notify(key.String())
	}
	sendPresence := func() bool {
		if !enablePresence {
			return true
		}
		version, peers := g.presence.snapshot(pk)
		if version == presenceVersion {
			return true
		}
		if sendCollab(conn, collabServerFrame{Type: "presence", Protocol: collabProtocol, Epoch: epoch, ConnectionID: &connectionID, PresenceVersion: version, Peers: peers}) != nil {
			return false
		}
		presenceVersion = version
		return true
	}
	conn.SetReadLimit(collabFrameLimit)
	if conn.SetReadDeadline(time.Now().Add(45*time.Second)) != nil {
		return
	}
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	frames := make(chan collabClientFrame)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			typ, raw, err := conn.ReadMessage()
			if err != nil || typ != websocket.TextMessage {
				return
			}
			f, err := parseCollabFrame(raw)
			if err != nil {
				return
			}
			select {
			case frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	tick := time.NewTicker(collabPollInterval)
	defer tick.Stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	// Every notification is checked against the current membership, grant,
	// session, canonical revision and epoch before any state is sent.
	refresh := func(requestID *uuid.UUID, force bool) bool {
		opCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		m, err := g.store.inspect(opCtx, user, key, epoch)
		if err != nil {
			closeCollab(conn, collabErrorCode(err))
			return false
		}
		if m.role != role {
			closeCollab(conn, "papers.collab_role_changed")
			return false
		}
		if !force && m.sequence == sentSeq && m.projectRevision == sentProject {
			return sendPresence()
		}
		fresh, err := g.store.Read(opCtx, user, key.project, key.file, epoch)
		if err != nil {
			closeCollab(conn, collabErrorCode(err))
			return false
		}
		if fresh.Sequence < sentSeq {
			closeCollab(conn, "papers.collab_epoch_stale")
			return false
		}
		if sendCollab(conn, stateFrame(fresh, role, requestID)) != nil {
			return false
		}
		sentSeq, sentProject = fresh.Sequence, fresh.ProjectRevision
		return sendPresence()
	}
	var lastMessage, lastPresence time.Time
	var presenceDrops int
	if enablePresence && !refresh(nil, false) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-readerDone:
			return
		case <-tick.C:
			if !refresh(nil, false) {
				return
			}
		case <-notify:
			if !refresh(nil, false) {
				return
			}
		case <-ping.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second)) != nil {
				return
			}
		case f := <-frames:
			if f.Epoch != epoch {
				closeCollab(conn, "papers.collab_epoch_stale")
				return
			}
			if f.Type == "presence" {
				if !enablePresence {
					closeCollab(conn, "papers.collab_invalid_update")
					return
				}
				// Rate-limit ephemeral messages independently of accepted source
				// updates. Excess cursor motion is dropped, not written to storage.
				if !lastPresence.IsZero() && time.Since(lastPresence) < 350*time.Millisecond {
					presenceDrops++
					if presenceDrops > 8 {
						closeCollab(conn, "papers.collab_rate_limit")
						return
					}
					continue
				}
				presenceDrops = 0
				lastPresence = time.Now()
				opCtx, stopOp := context.WithTimeout(ctx, 5*time.Second)
				current, err := g.store.inspect(opCtx, user, key, epoch)
				stopOp()
				if err != nil {
					closeCollab(conn, collabErrorCode(err))
					return
				}
				if current.role != role {
					closeCollab(conn, "papers.collab_role_changed")
					return
				}
				if g.presence.update(pk, connectionID, f.Selection) {
					g.signals.Notify(key.String())
				}
				continue
			}
			// Presence and durable document writes have independent pacing.
			if !lastMessage.IsZero() && time.Since(lastMessage) < 150*time.Millisecond {
				closeCollab(conn, "papers.collab_rate_limit")
				return
			}
			lastMessage = time.Now()
			if f.Type == "sync" {
				if !refresh(&f.RequestID, true) {
					return
				}
				continue
			}
			if role == RoleProjectViewer {
				closeCollab(conn, "papers.read_only")
				return
			}
			opCtx, stopOp := context.WithTimeout(ctx, 10*time.Second)
			release, ok := g.reserveEngine(opCtx, key)
			if !ok {
				stopOp()
				if ctx.Err() != nil {
					return
				}
				if sendCollab(conn, collabServerFrame{Type: "error", Epoch: epoch, RequestID: &f.RequestID, Code: "papers.collab_retry", Retryable: true}) != nil {
					return
				}
				continue
			}
			accepted, err := g.store.Apply(opCtx, user, key.project, key.file, epoch, f.Update)
			stopOp()
			release()
			if err != nil {
				code := collabErrorCode(err)
				if terminalCollabError(code) {
					closeCollab(conn, code)
					return
				}
				if sendCollab(conn, collabServerFrame{Type: "error", Epoch: epoch, RequestID: &f.RequestID, Code: code, Retryable: code == "papers.collab_retry" || code == "papers.collab_unavailable" || code == "papers.collab_storage_failed"}) != nil {
					return
				}
				continue
			}
			// Notify only AFTER the authoritative transaction has returned success.
			// Even if the sender disconnects before its ack, peers can catch up.
			if accepted.Sequence > sentSeq {
				g.signals.Notify(key.String())
			}
			if !refresh(&f.RequestID, true) {
				return
			}
		}
	}
}

func (h *handler) collabParams(c *gin.Context) (authctx.CurrentUser, model.AuthSession, roomKey, bool) {
	u, ok := current(c)
	if !ok {
		return u, model.AuthSession{}, roomKey{}, false
	}
	session, ok := middleware.CurrentAuthSession(c)
	if !ok || session.ID == uuid.Nil || session.UserID != u.ID {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return u, session, roomKey{}, false
	}
	pid, ok := projectID(c)
	if !ok {
		return u, session, roomKey{}, false
	}
	fid, ok := httpx.UUIDParam(c, "fileId")
	if !ok {
		return u, session, roomKey{}, false
	}
	return u, session, roomKey{project: pid, file: fid}, true
}

// @Summary Inspect file collaboration availability (disabled by default)
// @Tags papers
// @Security BearerAuth
// @Param id path string true "Project UUID"
// @Param fileId path string true "File UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/files/{fileId}/collaboration [get]
func (h *handler) collaborationCapability(c *gin.Context) {
	u, session, key, ok := h.collabParams(c)
	if !ok {
		return
	}
	g := h.service.collaboration
	if g == nil {
		if _, err := h.service.GetFile(u, key.project, key.file); err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.OK(c, http.StatusOK, gin.H{"enabled": false})
		return
	}
	ctx := context.WithValue(c.Request.Context(), collabSessionKey{}, session.ID)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	meta, err := g.store.inspect(ctx, u, key, uuid.Nil)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"enabled": true, "protocol": collabProtocol, "role": meta.role, "presence": true})
}

// @Summary Authenticated private collaboration WebSocket (disabled by default)
// @Tags papers
// @Security BearerAuth
// @Param id path string true "Project UUID"
// @Param fileId path string true "File UUID"
// @Param epoch query string false "Prior editing epoch; stale epochs are rejected"
// @Param presence query string false "Set to 1 to negotiate optional live presence frames"
// @Success 101
// @Router /api/v1/papers/projects/{id}/files/{fileId}/collaboration/ws [get]
func (h *handler) collaborationSocket(c *gin.Context) {
	u, session, key, ok := h.collabParams(c)
	if !ok {
		return
	}
	g := h.service.collaboration
	if g == nil {
		httpx.Error(c, apperr.New(http.StatusServiceUnavailable, "papers.collab_disabled", "Realtime collaboration is disabled", nil))
		return
	}
	if !websocket.IsWebSocketUpgrade(c.Request) {
		httpx.Error(c, apperr.BadRequest("papers.collab_upgrade_required", "WebSocket upgrade required"))
		return
	}
	// Cookie-authenticated upgrades require an explicit trusted web origin.
	// API-token clients use the existing shared WebSocket origin policy.
	if session.Kind == authsession.KindWeb && !middleware.IsTrustedWebOrigin(c.GetHeader("Origin")) {
		httpx.Error(c, apperr.Forbidden("auth.origin_invalid", "Request origin invalid"))
		return
	}
	var epoch uuid.UUID
	if raw := c.Query("epoch"); raw != "" {
		var err error
		epoch, err = uuid.Parse(raw)
		if err != nil || epoch == uuid.Nil {
			httpx.Error(c, apperr.BadRequest("papers.collab_invalid_epoch", "Invalid editing epoch"))
			return
		}
	}
	release, ok := g.admit(u.ID, key)
	if !ok {
		httpx.Error(c, apperr.New(http.StatusTooManyRequests, "papers.collab_limit", "Collaboration connection limit reached", nil))
		return
	}
	defer release()
	ctx := context.WithValue(c.Request.Context(), collabSessionKey{}, session.ID)
	initial, meta, err := g.join(ctx, u, key, epoch)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	conn, err := collab.UpgradeAuthorized(c)
	if err != nil {
		return
	}
	g.serve(ctx, conn, u, key, initial, meta, c.Query("presence") == "1")
}
