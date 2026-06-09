// Package collab implements a y-websocket–compatible message relay hub.
// Each "room" corresponds to a blog post UUID. The hub simply broadcasts
// every message received from one client to all other clients in the same
// room, which is the minimal behaviour required by the y-websocket protocol
// (the Go side does NOT run a Y.Doc; awareness and CRDT merging happen
// entirely in the browser via yjs).
package collab

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Allow all origins in dev; in prod this should be restricted.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// client represents a single WebSocket connection inside a room.
type client struct {
	conn   *websocket.Conn
	send   chan []byte
	userID string // JWT sub, for presence info
	room   string
	hub    *Hub
}

// Hub manages all rooms and the clients inside them.
type Hub struct {
	mu      sync.RWMutex
	rooms   map[string]map[*client]struct{} // room → set of clients
	join    chan *client
	leave   chan *client
	message chan roomMessage
}

type roomMessage struct {
	room   string
	sender *client
	data   []byte
}

// NewHub creates and starts a Hub.
func NewHub() *Hub {
	h := &Hub{
		rooms:   make(map[string]map[*client]struct{}),
		join:    make(chan *client, 64),
		leave:   make(chan *client, 64),
		message: make(chan roomMessage, 512),
	}
	go h.run()
	return h
}

func (h *Hub) run() {
	for {
		select {
		case c := <-h.join:
			h.mu.Lock()
			if h.rooms[c.room] == nil {
				h.rooms[c.room] = make(map[*client]struct{})
			}
			h.rooms[c.room][c] = struct{}{}
			h.mu.Unlock()

		case c := <-h.leave:
			h.mu.Lock()
			if room, ok := h.rooms[c.room]; ok {
				delete(room, c)
				if len(room) == 0 {
					delete(h.rooms, c.room)
				}
			}
			h.mu.Unlock()
			close(c.send)

		case msg := <-h.message:
			h.mu.RLock()
			peers := h.rooms[msg.room]
			h.mu.RUnlock()
			for peer := range peers {
				if peer == msg.sender {
					continue
				}
				select {
				case peer.send <- msg.data:
				default:
					// slow consumer – drop the message; Yjs will re-sync
				}
			}
		}
	}
}

// ServeWS is the gin handler for GET /api/collab/ws/:roomID.
// The roomID should be the blog post UUID.
func (h *Hub) ServeWS(ctx *gin.Context) {
	roomID := ctx.Param("roomID")
	if roomID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "missing roomID"})
		return
	}

	userID, _ := ctx.Get("userID") // set by AuthMiddleware; empty for anon
	uid, _ := userID.(string)

	conn, err := upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
	if err != nil {
		return
	}

	c := &client{
		conn:   conn,
		send:   make(chan []byte, 128),
		userID: uid,
		room:   roomID,
		hub:    h,
	}

	h.join <- c

	go c.writePump()
	c.readPump() // blocks until the connection closes
}

func (c *client) readPump() {
	defer func() {
		c.hub.leave <- c
		c.conn.Close()
	}()

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		c.hub.message <- roomMessage{room: c.room, sender: c, data: data}
	}
}

func (c *client) writePump() {
	defer c.conn.Close()

	for data := range c.send {
		if err := c.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
			break
		}
	}
}
