package collab

import "sync"

// RoomSignals is a bounded, content-free room notification registry.
// Subscribers reread authorized durable state; these notifications are
// deliberately coalesced, so slow clients never queue manuscript copies.
type RoomSignals struct {
	mu    sync.Mutex
	rooms map[string]map[chan struct{}]struct{}
}

func NewRoomSignals() *RoomSignals {
	return &RoomSignals{rooms: make(map[string]map[chan struct{}]struct{})}
}
func (h *RoomSignals) Subscribe(room string) (<-chan struct{}, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan struct{}, 1)
	if h.rooms[room] == nil {
		h.rooms[room] = make(map[chan struct{}]struct{})
	}
	h.rooms[room][ch] = struct{}{}
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.rooms[room], ch)
		if len(h.rooms[room]) == 0 {
			delete(h.rooms, room)
		}
	}
}
func (h *RoomSignals) Notify(room string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.rooms[room] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
