package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"fmt"

	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
)

const (
	dbFile = "prisma.db"
)

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

type Role string

const (
	RoleAdmin Role = "admin"
	RoleGuest Role = "guest"
)

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
}

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func main() {
	db := initDB()
	defer db.Close()

	ensureTables(db)
	ensureInitialAdmin(db)

	http.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var creds Credentials
		if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		user, ok := checkUser(db, creds.Username, creds.Password)
		if !ok {
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(user)
	})

	http.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("websocket upgrade:", err)
			return
		}
		defer ws.Close()
		// Placeholder: handle messages here
		for {
			mt, msg, err := ws.ReadMessage()
			if err != nil {
				break
			}
			log.Printf("recv: %s", msg)
			ws.WriteMessage(mt, []byte("pong"))
		}
	})

	log.Println("Server started at :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// --- DB functions ---

func initDB() *sql.DB {
	if _, err := os.Stat(dbFile); os.IsNotExist(err) {
		file, err := os.Create(dbFile)
		if err != nil {
			log.Fatalf("Failed to create db file: %v", err)
		}
		file.Close()
	}
	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		log.Fatalf("Failed to open db: %v", err)
	}
	return db
}

func ensureTables(db *sql.DB) {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		password TEXT NOT NULL,
		role TEXT NOT NULL
	)`)
	if err != nil {
		log.Fatalf("Failed to create tables: %v", err)
	}
}

// Prompt on CLI for admin account if none exists
func ensureInitialAdmin(db *sql.DB) {
	var count int
	row := db.QueryRow(`SELECT COUNT(*) FROM users WHERE role=?`, RoleAdmin)
	row.Scan(&count)
	if count > 0 {
		return
	}
	log.Println("No admin user found. Please create your initial admin account:")
	var username, password string
	for {
		print("Username: ")
		_, err := fmt.Scanln(&username)
		if err != nil || len(username) < 3 {
			log.Println("Invalid username.")
			continue
		}
		break
	}
	for {
		print("Password: ")
		_, err := fmt.Scanln(&password)
		if err != nil || len(password) < 4 {
			log.Println("Password must be at least 4 characters.")
			continue
		}
		break
	}
	if createUser(db, username, password, RoleAdmin) {
		log.Println("Admin user created successfully.")
	} else {
		log.Println("Failed to create admin user.")
		os.Exit(1)
	}
}

func createUser(db *sql.DB, username, password string, role Role) bool {
	_, err := db.Exec(
		`INSERT INTO users (username, password, role) VALUES (?, ?, ?)`,
		username, password, string(role),
	)
	return err == nil
}

func checkUser(db *sql.DB, username, password string) (*User, bool) {
	row := db.QueryRow(`SELECT id, username, role FROM users WHERE username=? AND password=?`, username, password)
	var u User
	var role string
	err := row.Scan(&u.ID, &u.Username, &role)
	if err != nil {
		return nil, false
	}
	u.Role = Role(role)
	return &u, true
}
