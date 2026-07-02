package handlers

type CommentInput struct {
	GuestName    string `json:"guest_name"`
	Content      string `json:"content" binding:"required"`
	TimestampSec *int   `json:"timestamp_sec"`
}
