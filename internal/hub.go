package internal

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const historyTTL    = 24 * time.Hour
const historyMaxLen = 200

// Message is the canonical chat event broadcast between clients.
type Message struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	SenderID  string    `json:"sender_id"`
	Username  string    `json:"username"`
	Body      string    `json:"body"`
	Type      string    `json:"type"` // text | system | image_url
	Timestamp time.Time `json:"timestamp"`
}

// Hub maintains active clients grouped by room and fans-out broadcasts.
type Hub struct {
	mu     sync.RWMutex
	rooms  map[string]map[*Client]bool
	bcast  chan *Message
	reg    chan *Client
	unreg  chan *Client
	redis  *redis.Client
	logger *zap.Logger
}

func NewHub(rdb *redis.Client, log *zap.Logger) *Hub {
	return &Hub{
		rooms:  make(map[string]map[*Client]bool),
		bcast:  make(chan *Message, 256),
		reg:    make(chan *Client, 64),
		unreg:  make(chan *Client, 64),
		redis:  rdb,
		logger: log,
	}
}

func (h *Hub) Register(c *Client)   { h.reg <- c }
func (h *Hub) Unregister(c *Client) { h.unreg <- c }
func (h *Hub) Broadcast(m *Message) { h.bcast <- m }

func (h *Hub) Run() {
	for {
		select {
		case c := <-h.reg:
			h.mu.Lock()
			if h.rooms[c.roomID] == nil {
				h.rooms[c.roomID] = make(map[*Client]bool)
			}
			h.rooms[c.roomID][c] = true
			h.mu.Unlock()
			h.logger.Info("client joined", zap.String("room", c.roomID), zap.String("user", c.userID))

		case c := <-h.unreg:
			h.mu.Lock()
			if room := h.rooms[c.roomID]; room != nil {
				delete(room, c)
				if len(room) == 0 {
					delete(h.rooms, c.roomID)
				}
			}
			h.mu.Unlock()
			close(c.send)
			h.logger.Info("client left", zap.String("room", c.roomID), zap.String("user", c.userID))

		case msg := <-h.bcast:
			h.mu.RLock()
			room := h.rooms[msg.RoomID]
			h.mu.RUnlock()

			raw, _ := json.Marshal(msg)
			for c := range room {
				select {
				case c.send <- raw:
				default:
					close(c.send)
					h.mu.Lock()
					delete(h.rooms[msg.RoomID], c)
					h.mu.Unlock()
				}
			}
			if h.redis != nil {
				go h.persistMessage(msg, raw)
			}
		}
	}
}

func (h *Hub) persistMessage(msg *Message, raw []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	key := "chat:room:" + msg.RoomID + ":history"
	h.redis.LPush(ctx, key, raw)
	h.redis.LTrim(ctx, key, 0, historyMaxLen-1)
	h.redis.Expire(ctx, key, historyTTL)
}

// GetHistory retrieves the last n messages for a room from Redis in chronological order.
func GetHistory(ctx context.Context, rdb *redis.Client, roomID string, n int64) ([]Message, error) {
	key := "chat:room:" + roomID + ":history"
	vals, err := rdb.LRange(ctx, key, 0, n-1).Result()
	if err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(vals))
	for i := len(vals) - 1; i >= 0; i-- {
		var m Message
		if json.Unmarshal([]byte(vals[i]), &m) == nil {
			msgs = append(msgs, m)
		}
	}
	return msgs, nil
}
