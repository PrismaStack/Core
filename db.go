package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
)

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

func addColumnIfNotExists(db *sql.DB, tableName, columnName, columnType string) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		log.Printf("Could not get table info for %s: %v", tableName, err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notnull int
		var dflt_value sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notnull, &dflt_value, &pk); err == nil {
			if name == columnName {
				return
			}
		}
	}

	log.Printf("Adding column '%s' to table '%s'...", columnName, tableName)
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, columnName, columnType))
	if err != nil {
		log.Printf("Failed to add column %s to %s: %v", columnName, tableName, err)
	}
}

func ensureTables(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS users (
        id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT UNIQUE NOT NULL,
        password TEXT NOT NULL, role TEXT NOT NULL, avatar_url TEXT
    )`)
	db.Exec(`CREATE TABLE IF NOT EXISTS channel_categories (
        id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE NOT NULL,
        position INTEGER NOT NULL DEFAULT 0
    )`)
	db.Exec(`CREATE TABLE IF NOT EXISTS channels (
        id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
        category_id INTEGER NOT NULL, position INTEGER NOT NULL DEFAULT 0,
        FOREIGN KEY (category_id) REFERENCES channel_categories(id)
    )`)
	db.Exec(`CREATE TABLE IF NOT EXISTS messages (
        id INTEGER PRIMARY KEY AUTOINCREMENT, channel_id INTEGER NOT NULL, user_id INTEGER NOT NULL,
        content TEXT NOT NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE, FOREIGN KEY (user_id) REFERENCES users(id)
    )`)

	addColumnIfNotExists(db, "users", "avatar_url", "TEXT")
	addColumnIfNotExists(db, "channel_categories", "position", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfNotExists(db, "channels", "position", "INTEGER NOT NULL DEFAULT 0")
}

func ensureInitialCategoryAndChannel(db *sql.DB) {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM channel_categories").Scan(&count)
	if count == 0 {
		res, err := db.Exec("INSERT INTO channel_categories (name, position) VALUES (?, 0)", "General")
		if err == nil {
			catID, _ := res.LastInsertId()
			db.Exec("INSERT INTO channels (name, category_id, position) VALUES (?, ?, 0)", "general-lobby", catID)
			db.Exec("INSERT INTO channels (name, category_id, position) VALUES (?, ?, 1)", "off-topic", catID)
			log.Println("Created initial 'General' category and channels.")
		}
	}
}

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
	_, err := db.Exec(`INSERT INTO users (username, password, role) VALUES (?, ?, ?)`, username, password, string(role))
	return err == nil
}

func checkUser(db *sql.DB, username, password string) (*User, bool) {
	row := db.QueryRow(`SELECT id, username, role, avatar_url FROM users WHERE username=? AND password=?`, username, password)
	var u User
	var roleStr string
	var avatarURL sql.NullString
	err := row.Scan(&u.ID, &u.Username, &roleStr, &avatarURL)
	if err != nil {
		// A failed login is a common case, so logging a full error might be too noisy.
		// log.Printf("Login check failed for user '%s': %v", username, err)
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