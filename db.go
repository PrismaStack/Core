package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"
)

const (
	pgUser     = "prisma"
	pgPassword = "Srl097130!"
	pgDB       = "prisma"
	pgHost     = "localhost"
	pgPort     = "5432"
)

func postgresDSN() string {
	return fmt.Sprintf(
		"user=%s password=%s dbname=%s host=%s port=%s sslmode=disable",
		pgUser, pgPassword, pgDB, pgHost, pgPort,
	)
}

func initDB() *sql.DB {
	db, err := sql.Open("postgres", postgresDSN())
	if err != nil {
		log.Fatalf("Failed to open db: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}
	return db
}

func ensureTables(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS users (
        id SERIAL PRIMARY KEY, username TEXT UNIQUE NOT NULL,
        password TEXT NOT NULL, role TEXT NOT NULL, avatar_url TEXT
    )`)
	db.Exec(`CREATE TABLE IF NOT EXISTS sessions (
        token TEXT PRIMARY KEY,
        user_id INTEGER NOT NULL REFERENCES users(id),
        created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
        expires_at TIMESTAMPTZ
    )`)
	db.Exec(`CREATE TABLE IF NOT EXISTS channel_categories (
        id SERIAL PRIMARY KEY, name TEXT UNIQUE NOT NULL,
        position INTEGER NOT NULL DEFAULT 0
    )`)
	db.Exec(`CREATE TABLE IF NOT EXISTS channels (
        id SERIAL PRIMARY KEY, name TEXT NOT NULL,
        category_id INTEGER NOT NULL REFERENCES channel_categories(id),
        position INTEGER NOT NULL DEFAULT 0
    )`)
	db.Exec(`CREATE TABLE IF NOT EXISTS messages (
        id SERIAL PRIMARY KEY, channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
        user_id INTEGER NOT NULL REFERENCES users(id),
        content TEXT NOT NULL, created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
    )`)
	db.Exec(`CREATE TABLE IF NOT EXISTS uploads (
        id SERIAL PRIMARY KEY,
        user_id INTEGER NOT NULL REFERENCES users(id),
        orig_filename TEXT NOT NULL,
        stored_filename TEXT NOT NULL,
        filetype TEXT,
        filesize INTEGER,
        uploaded_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
    )`)
	db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_url TEXT`)
	db.Exec(`ALTER TABLE channel_categories ADD COLUMN IF NOT EXISTS position INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE channels ADD COLUMN IF NOT EXISTS position INTEGER NOT NULL DEFAULT 0`)
}

func ensureInitialCategoryAndChannel(db *sql.DB) {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM channel_categories").Scan(&count)
	if count == 0 {
		var catID int64
		err := db.QueryRow(
			"INSERT INTO channel_categories (name, position) VALUES ($1, 0) RETURNING id", "General",
		).Scan(&catID)
		if err == nil {
			db.Exec("INSERT INTO channels (name, category_id, position) VALUES ($1, $2, 0)", "general-lobby", catID)
			db.Exec("INSERT INTO channels (name, category_id, position) VALUES ($1, $2, 1)", "off-topic", catID)
			log.Println("Created initial 'General' category and channels.")
		}
	}
}

func ensureInitialAdmin(db *sql.DB) {
	var count int
	row := db.QueryRow(`SELECT COUNT(*) FROM users WHERE role=$1`, RoleAdmin)
	row.Scan(&count)
	if count > 0 {
		return
	}

	log.Println("No admin user found. Please create your initial admin account:")
	var username, password string
	for {
		fmt.Print("Username: ")
		_, err := fmt.Scanln(&username)
		if err != nil || len(username) < 3 {
			continue
		}
		break
	}
	for {
		fmt.Print("Password: ")
		_, err := fmt.Scanln(&password)
		if err != nil || len(password) < 4 {
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
	_, err := db.Exec(`INSERT INTO users (username, password, role) VALUES ($1, $2, $3)`, username, password, string(role))
	return err == nil
}

func checkUser(db *sql.DB, username, password string) (*User, bool) {
	row := db.QueryRow(
		`SELECT id, username, role, avatar_url FROM users WHERE username=$1 AND password=$2`, username, password,
	)
	var u User
	var roleStr string
	var avatarURL sql.NullString
	err := row.Scan(&u.ID, &u.Username, &roleStr, &avatarURL)
	if err != nil {
		return nil, false
	}
	u.Role = Role(roleStr)
	if avatarURL.Valid {
		u.AvatarURL = avatarURL.String
	} else {
		u.AvatarURL = ""
	}
	return &u, true
}

// Token/session logic

func generateToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

func createSession(db *sql.DB, userID int64) (string, error) {
	token, err := generateToken()
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(30 * 24 * time.Hour)
	_, err = db.Exec(`INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, $3)`, token, userID, expires)
	if err != nil {
		return "", err
	}
	return token, nil
}

func getUserByToken(db *sql.DB, token string) (*User, bool) {
	row := db.QueryRow(`
		SELECT u.id, u.username, u.role, u.avatar_url
		FROM sessions s
		JOIN users u ON s.user_id = u.id
		WHERE s.token = $1 AND (s.expires_at IS NULL OR s.expires_at > NOW())`, token)
	var u User
	var roleStr string
	var avatarURL sql.NullString
	err := row.Scan(&u.ID, &u.Username, &roleStr, &avatarURL)
	if err != nil {
		return nil, false
	}
	u.Role = Role(roleStr)
	if avatarURL.Valid {
		u.AvatarURL = avatarURL.String
	} else {
		u.AvatarURL = ""
	}
	return &u, true
}

func refreshSession(db *sql.DB, token string) {
	db.Exec(`UPDATE sessions SET expires_at=$1 WHERE token=$2`, time.Now().Add(30*24*time.Hour), token)
}