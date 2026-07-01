package music

import "github.com/google/uuid"

type Source struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

type SubmitEditRequest struct {
	Type       string         `json:"type"`
	EntityType string         `json:"entity_type"`
	EntityID   *uuid.UUID     `json:"entity_id"`
	Payload    map[string]any `json:"payload"`
	Changes    map[string]any `json:"changes"`
	Reason     string         `json:"reason"`
	Sources    []Source       `json:"sources"`
}

type VoteRequest struct {
	Vote    string `json:"vote"`
	Comment string `json:"comment"`
}

type DecisionRequest struct {
	Reason string `json:"reason"`
}

type CreateArtistBookmarkRequest struct {
	ArtistID uuid.UUID `json:"artist_id"`
}

type CreateAlbumBookmarkRequest struct {
	AlbumID uuid.UUID `json:"album_id"`
}

type CreateSongBookmarkRequest struct {
	SongID uuid.UUID `json:"song_id"`
}

type CreatePlaylistRequest struct {
	Name string `json:"name"`
}

type AddPlaylistSongRequest struct {
	SongID uuid.UUID `json:"song_id"`
}

type PlaylistSummaryResponse struct {
	ID          uuid.UUID `json:"id"`
	UserID      uuid.UUID `json:"user_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	SongCount   int64     `json:"song_count"`
}
