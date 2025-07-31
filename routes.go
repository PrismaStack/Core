package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
)

// Registers all HTTP routes and handlers
func registerRoutes(r *mux.Router, db *sql.DB, hub *Hub) {
	r.HandleFunc("/api/login", loginHandler(db)).Methods("POST")
	r.HandleFunc("/api/register", registerHandler(db)).Methods("POST")
	r.HandleFunc("/api/categories", getCategoriesHandler(db)).Methods("GET")
	r.HandleFunc("/api/categories", createCategoryHandler(db)).Methods("POST")
	r.HandleFunc("/api/channels/{id:[0-9]+}", updateChannelHandler(db)).Methods("PUT")
	r.HandleFunc("/api/channels/{id:[0-9]+}", deleteChannelHandler(db)).Methods("DELETE")
	r.HandleFunc("/api/channels", createChannelHandler(db)).Methods("POST")
	r.HandleFunc("/api/channels/{id:[0-9]+}/messages", getMessagesHandler(db)).Methods("GET")
	r.HandleFunc("/api/messages", createMessageHandler(db, hub)).Methods("POST")
	r.HandleFunc("/api/reorder/categories", reorderHandler(db, "channel_categories")).Methods("POST")
	r.HandleFunc("/api/reorder/channels", reorderHandler(db, "channels")).Methods("POST")
	r.HandleFunc("/api/upload-avatar", uploadAvatarHandler(db)).Methods("POST")
	r.PathPrefix("/uploads/").Handler(http.StripPrefix("/uploads/", http.FileServer(http.Dir("uploads"))))
	r.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, db, w, r)
	})
}

// --- Handler functions ---

func loginHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var creds Credentials
		if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		user, ok := checkUser(db, creds.Username, creds.Password)
		if !ok {
			log.Printf("Login failed for '%s'", creds.Username)
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(user)
	}
}

func registerHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var creds Credentials
		if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if len(creds.Username) < 1 || len(creds.Password) < 4 {
			http.Error(w, "Invalid username or password length", http.StatusBadRequest)
			return
		}
		if !createUser(db, creds.Username, creds.Password, RoleGuest) {
			log.Printf("Registration failed: Username %s is already taken or DB error", creds.Username)
			http.Error(w, "Username is already taken", http.StatusConflict)
			return
		}
		log.Printf("Registered new user: %s", creds.Username)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "User registered successfully")
	}
}

func getCategoriesHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		categories := []ChannelCategory{}
		catRows, err := db.Query("SELECT id, name, position FROM channel_categories ORDER BY position")
		if err != nil {
			log.Printf("DB Error getting categories: %v", err)
			http.Error(w, "Failed to fetch categories", http.StatusInternalServerError)
			return
		}
		defer catRows.Close()

		for catRows.Next() {
			var cat ChannelCategory
			if err := catRows.Scan(&cat.ID, &cat.Name, &cat.Position); err != nil {
				log.Printf("DB Error scanning category: %v", err)
				continue
			}

			chRows, err := db.Query("SELECT id, name, category_id, position FROM channels WHERE category_id = ? ORDER BY position", cat.ID)
			if err != nil {
				log.Printf("DB Error getting channels for category %d: %v", cat.ID, err)
				continue
			}

			channels := []Channel{}
			for chRows.Next() {
				var ch Channel
				if err := chRows.Scan(&ch.ID, &ch.Name, &ch.CategoryID, &ch.Position); err != nil {
					log.Printf("DB Error scanning channel: %v", err)
				} else {
					channels = append(channels, ch)
				}
			}
			chRows.Close()
			cat.Channels = channels
			categories = append(categories, cat)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(categories)
	}
}

func createCategoryHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var newCategory ChannelCategory
		json.NewDecoder(r.Body).Decode(&newCategory)
		if strings.TrimSpace(newCategory.Name) == "" {
			http.Error(w, "Category name cannot be empty", http.StatusBadRequest)
			return
		}
		var maxPosition sql.NullInt64
		db.QueryRow("SELECT MAX(position) FROM channel_categories").Scan(&maxPosition)
		stmt, _ := db.Prepare("INSERT INTO channel_categories(name, position) VALUES(?, ?)")
		res, err := stmt.Exec(newCategory.Name, maxPosition.Int64+1)
		if err != nil {
			http.Error(w, "Failed to create category", http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		newCategory.ID = id
		newCategory.Channels = nil
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(newCategory)
	}
}

func createChannelHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var newChannel Channel
		json.NewDecoder(r.Body).Decode(&newChannel)
		if newChannel.Name == "" || newChannel.CategoryID == 0 {
			http.Error(w, "Missing channel name or category ID", http.StatusBadRequest)
			return
		}
		var maxPosition sql.NullInt64
		db.QueryRow("SELECT MAX(position) FROM channels WHERE category_id = ?", newChannel.CategoryID).Scan(&maxPosition)
		stmt, _ := db.Prepare("INSERT INTO channels(name, category_id, position) VALUES(?, ?, ?)")
		res, err := stmt.Exec(newChannel.Name, newChannel.CategoryID, maxPosition.Int64+1)
		if err != nil {
			http.Error(w, "Failed to create channel", http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		newChannel.ID = id
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(newChannel)
	}
}

func updateChannelHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		channelID, err := strconv.ParseInt(vars["id"], 10, 64)
		if err != nil {
			http.Error(w, "Invalid channel ID", http.StatusBadRequest)
			return
		}

		var reqBody map[string]string
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		newName, ok := reqBody["name"]
		if !ok || strings.TrimSpace(newName) == "" {
			http.Error(w, "Channel name cannot be empty", http.StatusBadRequest)
			return
		}

		_, err = db.Exec("UPDATE channels SET name = ? WHERE id = ?", newName, channelID)
		if err != nil {
			log.Printf("DB Error updating channel: %v", err)
			http.Error(w, "Failed to update channel", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

func deleteChannelHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		channelID, err := strconv.ParseInt(vars["id"], 10, 64)
		if err != nil {
			http.Error(w, "Invalid channel ID", http.StatusBadRequest)
			return
		}
		_, err = db.Exec("DELETE FROM channels WHERE id = ?", channelID)
		if err != nil {
			log.Printf("DB Error deleting channel: %v", err)
			http.Error(w, "Failed to delete channel", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func getMessagesHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		channelID, _ := strconv.Atoi(vars["id"])
		rows, err := db.Query(`
            SELECT m.id, m.channel_id, m.user_id, u.username, m.content, m.created_at, u.avatar_url
            FROM messages m JOIN users u ON m.user_id = u.id
            WHERE m.channel_id = ? ORDER BY m.created_at DESC`, channelID)
		if err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		messages := []Message{}
		for rows.Next() {
			var msg Message
			var avatarURL sql.NullString
			if err := rows.Scan(&msg.ID, &msg.ChannelID, &msg.UserID, &msg.Username, &msg.Content, &msg.CreatedAt, &avatarURL); err == nil {
				if avatarURL.Valid {
					msg.AvatarURL = avatarURL.String
				}
				messages = append(messages, msg)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(messages)
	}
}

func createMessageHandler(db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req NewMessageRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Content == "" || req.ChannelID == 0 || req.UserID == 0 {
			http.Error(w, "Missing fields", http.StatusBadRequest)
			return
		}

		tx, _ := db.Begin()
		stmt, _ := tx.Prepare("INSERT INTO messages(channel_id, user_id, content) VALUES(?, ?, ?)")
		res, err := stmt.Exec(req.ChannelID, req.UserID, req.Content)
		if err != nil {
			tx.Rollback()
			http.Error(w, "Failed to send message", http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		tx.Commit()

		var msg Message
		var avatarURL sql.NullString
		row := db.QueryRow(`
            SELECT m.id, m.channel_id, m.user_id, u.username, m.content, m.created_at, u.avatar_url
            FROM messages m JOIN users u ON m.user_id = u.id
            WHERE m.id = ?`, id)
		if err := row.Scan(&msg.ID, &msg.ChannelID, &msg.UserID, &msg.Username, &msg.Content, &msg.CreatedAt, &avatarURL); err != nil {
			log.Printf("Could not retrieve message for broadcast: %v", err)
		} else {
			if avatarURL.Valid {
				msg.AvatarURL = avatarURL.String
			}
			payloadBytes, _ := json.Marshal(msg)
			wrappedMsg, _ := json.Marshal(WebSocketMessage{Event: "new_message", Payload: json.RawMessage(payloadBytes)})
			hub.broadcast <- wrappedMsg
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]int64{"id": id})
	}
}

func reorderHandler(db *sql.DB, tableName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var items []ReorderItem
		json.NewDecoder(r.Body).Decode(&items)
		tx, _ := db.Begin()
		stmt, _ := tx.Prepare(fmt.Sprintf("UPDATE %s SET position = ? WHERE id = ?", tableName))
		defer stmt.Close()
		for _, item := range items {
			if _, err := stmt.Exec(item.Position, item.ID); err != nil {
				tx.Rollback()
				http.Error(w, "Failed to update item", http.StatusInternalServerError)
				return
			}
		}
		tx.Commit()
		w.WriteHeader(http.StatusOK)
	}
}

func uploadAvatarHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseMultipartForm(10 << 20)
		if err != nil {
			http.Error(w, "Failed to parse form", http.StatusBadRequest)
			return
		}
		userID := r.FormValue("user_id")
		if userID == "" {
			http.Error(w, "User ID required", http.StatusBadRequest)
			return
		}
		file, handler, err := r.FormFile("avatar")
		if err != nil {
			http.Error(w, "Error retrieving file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		if err := os.MkdirAll("uploads", os.ModePerm); err != nil {
			http.Error(w, "Failed to create upload directory", http.StatusInternalServerError)
			return
		}
		ext := filepath.Ext(handler.Filename)
		filename := fmt.Sprintf("avatar_%s%s", userID, ext)
		filePath := filepath.Join("uploads", filename)
		dst, err := os.Create(filePath)
		if err != nil {
			http.Error(w, "Failed to create file", http.StatusInternalServerError)
			return
		}
		defer dst.Close()
		if _, err := io.Copy(dst, file); err != nil {
			http.Error(w, "Failed to save file", http.StatusInternalServerError)
			return
		}
		thumbPath := filepath.Join("uploads", fmt.Sprintf("thumb_%s%s", userID, ext))
		cmd := exec.Command("convert", filePath, "-resize", "100x100", thumbPath)
		if err := cmd.Run(); err != nil {
			log.Printf("Failed to create thumbnail: %v", err)
		}
		avatarURL := fmt.Sprintf("/uploads/%s", filename)
		_, err = db.Exec("UPDATE users SET avatar_url = ? WHERE id = ?", avatarURL, userID)
		if err != nil {
			http.Error(w, "Failed to update user", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"avatar_url": avatarURL})
	}
}