package main

import "time"

type Role string

const (
	RoleAdmin Role = "admin"
	RoleGuest Role = "guest"
)

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      Role   `json:"role"`
	AvatarURL string `json:"avatar_url"`
}

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Channel struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CategoryID int64  `json:"category_id"`
	Position   int    `json:"position"`
}

type ChannelCategory struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	Position int       `json:"position"`
	Channels []Channel `json:"channels,omitempty"`
}

type Message struct {
	ID        int64     `json:"id"`
	ChannelID int64     `json:"channel_id"`
	UserID    int64     `json:"user_id"`
	Username  string    `json:"username"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	AvatarURL string    `json:"avatar_url,omitempty"`
}

type NewMessageRequest struct {
	Content   string `json:"content"`
	ChannelID int64  `json:"channel_id"`
	UserID    int64  `json:"user_id"`
}

type ReorderItem struct {
	ID       int64 `json:"id"`
	Position int   `json:"position"`
}

// Upload struct for file uploads
type Upload struct {
	ID            int64     `json:"id"`
	UserID        int64     `json:"user_id"`
	OrigFilename  string    `json:"orig_filename"`
	StoredFilename string   `json:"stored_filename"`
	Filetype      string    `json:"filetype"`
	Filesize      int64     `json:"filesize"`
	UploadedAt    time.Time `json:"uploaded_at"`
}