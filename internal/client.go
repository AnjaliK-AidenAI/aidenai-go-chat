package internal

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4096
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// Client wraps a WebSocket connection for one user in one room.
type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	send   chan []byte
	roomID string
	userID string
	name   string
}

// ServeWS upgrades the HTTP connection to WebSocket and registers the client with the hub.
func ServeWS(hub *Hub, roomID string, w http.ResponseWriter, r *http.Request, log *zap.Logger) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("upgrade failed", zap.Error(err))
		return
	}
	userID := r.URL.Query().Get("user_id")
	name   := r.URL.Query().Get("username")
	if userID == "" {
		userID = uuid.New().String()
	}
	if name == "" {
		name = "Anonymous"
	}

	c := &Client{hub: hub, conn: conn, send: make(chan []byte, 256), roomID: roomID, userID: userID, name: name}
	hub.Register(c)
	hub.Broadcast(&Message{
		ID: uuid.New().String(), RoomID: roomID, SenderID: "system", Username: "System",
		Body: name + " joined the room", Type: "system", Timestamp: time.Now().UTC(),
	})

	go c.writePump()
	go c.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.Unregister(c)
		c.hub.Broadcast(&Message{
			ID: uuid.New().String(), RoomID: c.roomID, SenderID: "system", Username: "System",
			Body: c.name + " left the room", Type: "system", Timestamp: time.Now().UTC(),
		})
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	type inbound struct {
		Body string `json:"body"`
		Type string `json:"type"`
	}
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		var in inbound
		if json.Unmarshal(raw, &in) != nil {
			continue
		}
		if in.Type == "" {
			in.Type = "text"
		}
		c.hub.Broadcast(&Message{
			ID: uuid.New().String(), RoomID: c.roomID, SenderID: c.userID,
			Username: c.name, Body: in.Body, Type: in.Type, Timestamp: time.Now().UTC(),
		})
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{}) //nolint:errcheck
				return
			}
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(msg) //nolint:errcheck
			for n := len(c.send); n > 0; n-- {
				w.Write([]byte("\n")) //nolint:errcheck
				w.Write(<-c.send)     //nolint:errcheck
			}
			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
