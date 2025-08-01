package main

import (
	"log"
	"net/http"

	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
)

const dbFile = "prisma.db"

func main() {
	db := initDB()
	defer db.Close()

	ensureTables(db)
	ensureInitialAdmin(db)
	ensureInitialCategoryAndChannel(db)

	hub := newHub()
	go hub.run()

	r := mux.NewRouter()
	registerRoutes(r, db, hub)

	// Serve uploads (avatars etc) before the web handler
	r.PathPrefix("/uploads/").Handler(http.StripPrefix("/uploads/", http.FileServer(http.Dir("uploads"))))

	// Catch-all: Serve Flutter web build from the "web" folder for any other route
	r.PathPrefix("/").Handler(serveWebApp())

	log.Println("Server started at :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}