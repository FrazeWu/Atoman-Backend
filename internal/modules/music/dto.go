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
	Name        string `json:"name"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
	IsPublic    bool   `json:"is_public"`
}

type AddPlaylistSongRequest struct {
	SongID uuid.UUID `json:"song_id"`
}

type RecordSongPlayRequest struct {
	SongID uuid.UUID `json:"song_id"`
}

type PlaylistSummaryResponse struct {
	ID          uuid.UUID `json:"id"`
	UserID      uuid.UUID `json:"user_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CoverURL    string    `json:"cover_url,omitempty"`
	IsPublic    bool      `json:"is_public"`
	SongCount   int64     `json:"song_count"`
}

type DiscoverItemResponse struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Title       string `json:"title"`
	Summary     string `json:"summary,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
	TargetPath  string `json:"target_path"`
	SongCount   int64  `json:"song_count,omitempty"`
	OwnerUserID string `json:"owner_user_id,omitempty"`
	Name        string `json:"name,omitempty"`
	LegalName   string `json:"legal_name,omitempty"`
	Bio         string `json:"bio,omitempty"`
	CoverURL    string `json:"cover_url,omitempty"`
	Description string `json:"description,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
	Year        int    `json:"year,omitempty"`
	Artists     []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"artists,omitempty"`
}

type PaginationMetaResponse struct {
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
	HasMore  bool  `json:"has_more"`
}

type PlaylistSummaryListResponse struct {
	Data []PlaylistSummaryResponse `json:"data"`
	Meta PaginationMetaResponse   `json:"meta"`
}

type DiscoverListResponse struct {
	Data []DiscoverItemResponse `json:"data"`
	Meta PaginationMetaResponse `json:"meta"`
}

type PlaylistSongsListResponse struct {
	Data []PlaylistSongResponse `json:"data"`
	Meta PaginationMetaResponse `json:"meta"`
}

type PlaylistSongResponse struct {
	ID         uuid.UUID           `json:"id"`
	PlaylistID uuid.UUID           `json:"playlist_id"`
	SongID     uuid.UUID           `json:"song_id"`
	Song       *PlaylistSongDetail `json:"song,omitempty"`
}

type PlaylistSongDetail struct {
	ID          uuid.UUID `json:"id"`
	Title       string    `json:"title"`
	TrackNumber int       `json:"track_number"`
	AudioURL    string    `json:"audio_url"`
	CoverURL    string    `json:"cover_url"`
	EntryStatus string    `json:"entry_status"`
}
