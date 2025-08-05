package main

import (
	"context"
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
	"time"

	"github.com/gorilla/mux"
)

// Registers all HTTP routes and handlers
func registerRoutes(r *mux.Router, db *sql.DB, hub *Hub) {
	// Public routes
	r.HandleFunc("/api/login", loginHandler(db)).Methods("POST")
	r.HandleFunc("/api/register", registerHandler(db)).Methods("POST")

	// Authenticated API routes
	api := r.PathPrefix("/api").Subrouter()
	api.Use(requireToken(db)) // Apply middleware to all /api routes after this point

	api.HandleFunc("/categories", getCategoriesHandler(db)).Methods("GET")
	api.HandleFunc("/categories", createCategoryHandler(db)).Methods("POST")

	api.HandleFunc("/channels/{id:[0-9]+}", updateChannelHandler(db)).Methods("PUT")
	api.HandleFunc("/channels/{id:[0-9]+}", deleteChannelHandler(db)).Methods("DELETE")
	api.HandleFunc("/channels", createChannelHandler(db)).Methods("POST")
	api.HandleFunc("/channels/{id:[0-9]+}/messages", getMessagesHandler(db)).Methods("GET")

	api.HandleFunc("/messages", createMessageHandler(db, hub)).Methods("POST")

	api.HandleFunc("/reorder/categories", reorderHandler(db, "channel_categories")).Methods("POST")
	api.HandleFunc("/reorder/channels", reorderHandler(db, "channels")).Methods("POST")

	api.HandleFunc("/upload-avatar", uploadAvatarHandler(db)).Methods("POST")
	api.HandleFunc("/upload-file", uploadFileHandler(db)).Methods("POST")

	// WebSocket route (handled separately, auth is inside serveWs)
	r.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, db, w, r)
	})

	// Static file serving
	r.PathPrefix("/uploads/").Handler(http.StripPrefix("/uploads/", http.FileServer(http.Dir("uploads"))))
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

		token, err := createSession(db, user.ID)
		if err != nil {
			log.Printf("Failed to create session for '%s': %v", creds.Username, err)
			http.Error(w, "Failed to create session", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		// FIX: Return a flat JSON object for easier client-side parsing.
		// It includes all user fields plus the token.
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":         user.ID,
			"username":   user.Username,
			"role":       user.Role,
			"avatar_url": user.AvatarURL,
			"token":      token,
		})
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

			chRows, err := db.Query("SELECT id, name, category_id, position FROM channels WHERE category_id = $1 ORDER BY position", cat.ID)
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
		stmt, _ := db.Prepare("INSERT INTO channel_categories(name, position) VALUES($1, $2)")
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
		db.QueryRow("SELECT MAX(position) FROM channels WHERE category_id = $1", newChannel.CategoryID).Scan(&maxPosition)
		stmt, _ := db.Prepare("INSERT INTO channels(name, category_id, position) VALUES($1, $2, $3)")
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

		_, err = db.Exec("UPDATE channels SET name = $1 WHERE id = $2", newName, channelID)
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
		_, err = db.Exec("DELETE FROM channels WHERE id = $1", channelID)
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
            WHERE m.channel_id = $1 ORDER BY m.created_at DESC`, channelID)
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
		// FIX: User is now reliably retrieved from the context.
		user := userFromContext(r.Context())
		if user == nil {
			http.Error(w, "Authentication error: User not found in context", http.StatusUnauthorized)
			return
		}

		var req NewMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if req.Content == "" || req.ChannelID == 0 {
			http.Error(w, "Missing fields", http.StatusBadRequest)
			return
		}

		// Set the UserID from the authenticated user context
		req.UserID = user.ID

		tx, _ := db.Begin()
		stmt, _ := tx.Prepare("INSERT INTO messages(channel_id, user_id, content) VALUES($1, $2, $3) RETURNING id")
		var id int64
		err := stmt.QueryRow(req.ChannelID, req.UserID, req.Content).Scan(&id)
		stmt.Close()
		if err != nil {
			tx.Rollback()
			http.Error(w, "Failed to send message", http.StatusInternalServerError)
			return
		}
		tx.Commit()

		var msg Message
		var avatarURL sql.NullString
		row := db.QueryRow(`
            SELECT m.id, m.channel_id, m.user_id, u.username, m.content, m.created_at, u.avatar_url
            FROM messages m JOIN users u ON m.user_id = u.id
            WHERE m.id = $1`, id)
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
		stmt, _ := tx.Prepare(fmt.Sprintf("UPDATE %s SET position = $1 WHERE id = $2", tableName))
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
		user := userFromContext(r.Context())
		if user == nil {
			http.Error(w, "User not found", http.StatusUnauthorized)
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
		filename := fmt.Sprintf("avatar_%d%s", user.ID, ext)
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
		thumbPath := filepath.Join("uploads", fmt.Sprintf("thumb_%d%s", user.ID, ext))
		cmd := exec.Command("convert", filePath, "-resize", "100x100", thumbPath)
		if err := cmd.Run(); err != nil {
			log.Printf("Failed to create thumbnail: %v", err)
		}
		avatarURL := fmt.Sprintf("/uploads/%s", filename)
		_, err = db.Exec("UPDATE users SET avatar_url = $1 WHERE id = $2", avatarURL, user.ID)
		if err != nil {
			http.Error(w, "Failed to update user", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"avatar_url": avatarURL})
	}
}

// --- File upload handler ---
func uploadFileHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const maxUploadSize = 100 << 20 // 100MB
		err := r.ParseMultipartForm(maxUploadSize)
		if err != nil {
			http.Error(w, "Failed to parse form", http.StatusBadRequest)
			return
		}
		user := userFromContext(r.Context())
		if user == nil {
			http.Error(w, "User not found", http.StatusUnauthorized)
			return
		}
		file, handler, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "Error retrieving file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		if err := os.MkdirAll("uploads", os.ModePerm); err != nil {
			http.Error(w, "Failed to create upload directory", http.StatusInternalServerError)
			return
		}

		timestamp := time.Now().UnixNano()
		ext := filepath.Ext(handler.Filename)
		storedFilename := fmt.Sprintf("file_%d_%d%s", user.ID, timestamp, ext)
		filePath := filepath.Join("uploads", storedFilename)

		dst, err := os.Create(filePath)
		if err != nil {
			http.Error(w, "Failed to create file", http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		n, err := io.Copy(dst, file)
		if err != nil {
			http.Error(w, "Failed to save file", http.StatusInternalServerError)
			return
		}

		filetype := handler.Header.Get("Content-Type")

		var uploadID int64
		err = db.QueryRow(
			`INSERT INTO uploads (user_id, orig_filename, stored_filename, filetype, filesize) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			user.ID, handler.Filename, storedFilename, filetype, n,
		).Scan(&uploadID)
		if err != nil {
			http.Error(w, "Failed to record upload", http.StatusInternalServerError)
			return
		}

		uploadURL := fmt.Sprintf("/uploads/%s", storedFilename)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":              uploadID,
			"orig_filename":   handler.Filename,
			"stored_filename": storedFilename,
			"filetype":        filetype,
			"filesize":        n,
			"url":             uploadURL,
		})
	}
}

// --- Token/session middleware ---

type contextKey string

const userContextKey = contextKey("user")

func userFromContext(ctx context.Context) *User {
	user, _ := ctx.Value(userContextKey).(*User)
	return user
}

// requireToken is middleware that checks for a valid bearer token.
func requireToken(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "Missing token", http.StatusUnauthorized)
				return
			}

			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
			if tokenStr == authHeader { // No "Bearer " prefix found
				http.Error(w, "Invalid token format", http.StatusUnauthorized)
				return
			}

			user, ok := getUserByToken(db, tokenStr)
			if !ok {
				http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
				return
			}

			refreshSession(db, tokenStr)
			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
