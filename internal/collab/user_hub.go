package collab

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"atoman/internal/middleware"
	"atoman/internal/platform/authsession"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

type UserMessage struct {
	Event string      `json:"event"`
	Data  interface{} `json:"data"`
}

type userClient struct {
	conn      *websocket.Conn
	send      chan []byte
	userID    uuid.UUID
	hub       *UserHub
	leaveOnce sync.Once
}

type UserHub struct {
	mu      sync.RWMutex
	clients map[uuid.UUID]map[*userClient]struct{}
	join    chan *userClient
	leave   chan *userClient
}

func NewUserHub() *UserHub {
	h := &UserHub{
		clients: make(map[uuid.UUID]map[*userClient]struct{}),
		join:    make(chan *userClient, 64),
		leave:   make(chan *userClient, 64),
	}
	go h.run()
	return h
}

func (h *UserHub) run() {
	for {
		select {
		case client := <-h.join:
			h.mu.Lock()
			if h.clients[client.userID] == nil {
				h.clients[client.userID] = make(map[*userClient]struct{})
			}
			h.clients[client.userID][client] = struct{}{}
			h.mu.Unlock()
		case client := <-h.leave:
			h.mu.Lock()
			if clients, ok := h.clients[client.userID]; ok {
				delete(clients, client)
				if len(clients) == 0 {
					delete(h.clients, client.userID)
				}
			}
			h.mu.Unlock()
			close(client.send)
		}
	}
}

func (h *UserHub) Push(userID uuid.UUID, event string, data interface{}) {
	payload, err := json.Marshal(UserMessage{Event: event, Data: data})
	if err != nil {
		return
	}

	h.mu.RLock()
	clients := h.clients[userID]
	h.mu.RUnlock()

	for client := range clients {
		select {
		case client.send <- payload:
		default:
		}
	}
}

func (h *UserHub) ServeWS(c *gin.Context, db *gorm.DB) {
	userID, err := extractUserIDFromRequest(c, db)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	client := &userClient{
		conn:   conn,
		send:   make(chan []byte, 64),
		userID: userID,
		hub:    h,
	}
	h.join <- client

	go client.writePump()
	go client.readPump()
}

func (c *userClient) writePump() {
	defer func() {
		c.leaveOnce.Do(func() { c.hub.leave <- c })
		c.conn.Close()
	}()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

func (c *userClient) readPump() {
	defer func() {
		c.leaveOnce.Do(func() { c.hub.leave <- c })
		c.conn.Close()
	}()
	c.conn.SetReadLimit(512)
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func extractUserIDFromRequest(c *gin.Context, db *gorm.DB) (uuid.UUID, error) {
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if authorization != "" {
		if !strings.HasPrefix(authorization, "Bearer ") {
			return uuid.Nil, errors.New("invalid authorization")
		}
		resolved, err := authsession.New(db).Authenticate(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")), authsession.KindAPI)
		if err != nil {
			return uuid.Nil, err
		}
		return resolved.User.UUID, nil
	}
	cookie, err := c.Cookie(middleware.AuthSessionCookieName)
	if err != nil || !middleware.IsTrustedWebOrigin(c.GetHeader("Origin")) {
		return uuid.Nil, errors.New("invalid web session")
	}
	resolved, err := authsession.New(db).Authenticate(cookie, authsession.KindWeb)
	if err != nil {
		return uuid.Nil, err
	}
	return resolved.User.UUID, nil
}
