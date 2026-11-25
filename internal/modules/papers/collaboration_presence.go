package papers

import (
	"bytes"
	"sort"
	"strings"
	"sync"
	"unicode"

	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
)

const maxRelativePositionBytes = 128

// Presence is ephemeral and scoped to the project, file and editing epoch.
// A client can publish only its own bounded opaque relative positions.
// Connection identity, account identity, role and name come from the server.
type collabSelection struct {
	Anchor []byte `json:"anchor"`
	Head   []byte `json:"head"`
}

func (s *collabSelection) valid() bool {
	return s == nil || (len(s.Anchor) > 0 && len(s.Anchor) <= maxRelativePositionBytes && len(s.Head) > 0 && len(s.Head) <= maxRelativePositionBytes)
}
func copySelection(s *collabSelection) *collabSelection {
	if s == nil {
		return nil
	}
	return &collabSelection{Anchor: bytes.Clone(s.Anchor), Head: bytes.Clone(s.Head)}
}
func equalSelection(a, b *collabSelection) bool {
	if a == nil || b == nil {
		return a == b
	}
	return bytes.Equal(a.Anchor, b.Anchor) && bytes.Equal(a.Head, b.Head)
}

type collabPeer struct {
	ConnectionID uuid.UUID        `json:"connection_id"`
	UserID       uuid.UUID        `json:"user_id"`
	Name         string           `json:"name"`
	Role         string           `json:"role"`
	Selection    *collabSelection `json:"selection,omitempty"`
}
type presenceKey struct {
	room  roomKey
	epoch uuid.UUID
}
type presenceRoom struct {
	version uint64
	peers   map[uuid.UUID]collabPeer
}
type presenceRegistry struct {
	mu      sync.Mutex
	version uint64
	rooms   map[presenceKey]*presenceRoom
}

func newPresenceRegistry() *presenceRegistry {
	return &presenceRegistry{rooms: make(map[presenceKey]*presenceRoom)}
}
func presenceName(u authctx.CurrentUser) string {
	name := []rune(strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, u.Username)))
	if len(name) > 64 {
		name = name[:64]
	}
	if len(name) == 0 {
		return u.ID.String()[:8]
	}
	return string(name)
}

// Admission is performed by the gateway first. No unbounded peer registry
// is reachable independently of its connection limits.
func (r *presenceRegistry) join(key presenceKey, u authctx.CurrentUser, role string) (uuid.UUID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room := r.rooms[key]
	if room == nil {
		room = &presenceRoom{peers: make(map[uuid.UUID]collabPeer)}
		r.rooms[key] = room
	}
	if len(room.peers) >= collabMaxPerRoom {
		return uuid.Nil, false
	}
	id := uuid.New()
	room.peers[id] = collabPeer{ConnectionID: id, UserID: u.ID, Name: presenceName(u), Role: role}
	r.version++
	room.version = r.version
	return id, true
}
func (r *presenceRegistry) update(key presenceKey, id uuid.UUID, s *collabSelection) bool {
	if !s.valid() {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	room := r.rooms[key]
	if room == nil {
		return false
	}
	peer, ok := room.peers[id]
	if !ok || equalSelection(peer.Selection, s) {
		return false
	}
	peer.Selection = copySelection(s)
	room.peers[id] = peer
	r.version++
	room.version = r.version
	return true
}
func (r *presenceRegistry) leave(key presenceKey, id uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	room := r.rooms[key]
	if room == nil {
		return false
	}
	if _, ok := room.peers[id]; !ok {
		return false
	}
	delete(room.peers, id)
	r.version++
	room.version = r.version
	if len(room.peers) == 0 {
		delete(r.rooms, key)
	}
	return true
}
func (r *presenceRegistry) snapshot(key presenceKey) (uint64, []collabPeer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room := r.rooms[key]
	if room == nil {
		return 0, []collabPeer{}
	}
	peers := make([]collabPeer, 0, len(room.peers))
	for _, peer := range room.peers {
		peer.Selection = copySelection(peer.Selection)
		peers = append(peers, peer)
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].ConnectionID.String() < peers[j].ConnectionID.String() })
	return room.version, peers
}
