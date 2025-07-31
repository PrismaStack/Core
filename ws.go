package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/gorilla/websocket"
)

// --- WebSocket Hub & Client ---

type WebSocketMessage struct {
	Event   string      `json:"event"`
	Payload interface{} `json:"payload"`
}

type Hub struct {
	clients         map[*Client]bool
	broadcast       chan []byte
	register        chan *Client
	unregister      chan *Client
	onlineUsers     map[int64]User
	connectionCount map[int64]int
}

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	user User
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func newHub() *Hub {
	return &Hub{
		broadcast:       make(chan []byte),
		register:        make(chan *Client),
		unregister:      make(chan *Client),
		clients:         make(map[*Client]bool),
		onlineUsers:     make(map[int64]User),
		connectionCount: make(map[int64]int),
	}
}

func (h *Hub) broadcastPresence() {
	online := []User{}
	for _, user := range h.onlineUsers {
		online = append(online, user)
	}

	payloadBytes, err := json.Marshal(online)
	if err != nil {
		log.Printf("Error marshalling presence payload: %v", err)
		return
	}

	message, err := json.Marshal(WebSocketMessage{
		Event:   "presence_update",
		Payload: json.RawMessage(payloadBytes),
	})
	if err != nil {
		log.Printf("Error marshalling presence message: %v", err)
		return
	}

	h.broadcast <- message
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
			if client.user.ID != 0 {
				isNewOnlineUser := h.connectionCount[client.user.ID] == 0
				h.connectionCount[client.user.ID]++

				if isNewOnlineUser {
					h.onlineUsers[client.user.ID] = client.user
					go h.broadcastPresence() // Launch in a goroutine to avoid blocking
				} else {
					online := []User{}
					for _, user := range h.onlineUsers {
						online = append(online, user)
					}
					payloadBytes, err := json.Marshal(online)
					if err != nil {
						log.Printf("Error marshalling existing presence payload: %v", err)
						continue
					}
					message, err := json.Marshal(WebSocketMessage{
						Event:   "presence_update",
						Payload: json.RawMessage(payloadBytes),
					})
					if err != nil {
						log.Printf("Error marshalling existing presence message: %v", err)
						continue
					}
					client.send <- message
				}
			}
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				if client.user.ID != 0 {
					h.connectionCount[client.user.ID]--
					if h.connectionCount[client.user.ID] == 0 {
						delete(h.onlineUsers, client.user.ID)
						delete(h.connectionCount, client.user.ID)
						// **FIXED**: Launch in a goroutine to prevent deadlock.
						go h.broadcastPresence()
					}
				}
			}
		case message := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					if client.user.ID != 0 {
						h.connectionCount[client.user.ID]--
						if h.connectionCount[client.user.ID] == 0 {
							delete(h.onlineUsers, client.user.ID)
							delete(h.connectionCount, client.user.ID)
							// **FIXED**: Launch in a goroutine to prevent deadlock.
							go h.broadcastPresence()
						}
					}
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

func (c *Client) writePump() {
	defer c.conn.Close()
	for message := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
			c.hub.unregister <- c
			return
		}
	}
	c.conn.WriteMessage(websocket.CloseMessage, []byte{})
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}

// THE FIX IS HERE - start writePump and readPump before registering the client.
func serveWs(hub *Hub, db *sql.DB, w http.ResponseWriter, r *http.Request) {
	userIDStr := r.URL.Query().Get("user_id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid user_id", http.StatusBadRequest)
		return
	}

	var user User
	var roleStr string
	var avatarURL sql.NullString
	err = db.QueryRow("SELECT id, username, role, avatar_url FROM users WHERE id = ?", userID).Scan(&user.ID, &user.Username, &roleStr, &avatarURL)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	user.Role = Role(roleStr)
	if avatarURL.Valid {
		user.AvatarURL = avatarURL.String
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	client := &Client{hub: hub, conn: conn, send: make(chan []byte, 256), user: user}

	// Start the writer and reader goroutines BEFORE registering the client.
	// This prevents a deadlock where the hub tries to send a message to a
	// client that isn't ready to receive it yet.
	go client.writePump()
	go client.readPump()

	// Now it's safe to register the client.
	client.hub.register <- client
}