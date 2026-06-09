package collab

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type UserMessage struct {
	Event string      `json:"event"`
	Data  interface{} `json:"data"`
}

type userClient struct {
	conn   *websocket.Conn
	send   chan []byte
	userID uuid.UUID
	hub    *UserHub
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

func (h *UserHub) ServeWS(c *gin.Context, jwtSecret string) {
	userID, err := extractUserIDFromRequest(c, jwtSecret)
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
		c.hub.leave <- c
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
		c.hub.leave <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(512)
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func extractUserIDFromRequest(c *gin.Context, jwtSecret string) (uuid.UUID, error) {
	tokenStr := ""
	authHeader := c.GetHeader("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
	} else if q := c.Query("token"); q != "" {
		tokenStr = q
	}
	if tokenStr == "" {
		return uuid.Nil, jwt.ErrTokenMalformed
	}

	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrTokenSignatureInvalid
		}
		return []byte(jwtSecret), nil
	})
	if err != nil || !token.Valid {
		return uuid.Nil, err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil, jwt.ErrTokenMalformed
	}
	userIDStr, ok := claims["user_id"].(string)
	if !ok {
		return uuid.Nil, jwt.ErrTokenMalformed
	}
	return uuid.Parse(userIDStr)
}
