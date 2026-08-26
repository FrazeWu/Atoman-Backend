package music

import (
	"encoding/json"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

type Source = model.MusicSource

type SubmitEditRequest struct {
	Type       string         `json:"type"`
	EntityType string         `json:"entity_type"`
	EntityID   *uuid.UUID     `json:"entity_id"`
	Payload    map[string]any `json:"payload"`
	Changes    map[string]any `json:"changes"`
	Reason     string         `json:"reason"`
	Sources    []Source       `json:"sources"`
}

type MusicReleaseConversionRequest struct {
	Title         string                   `json:"title"`
	Description   string                   `json:"description"`
	ReleaseDate   string                   `json:"release_date"`
	ReleaseType   string                   `json:"release_type"`
	CoverURL      string                   `json:"cover_url"`
	ArtistCredits []AlbumArtistCreditInput `json:"artist_credits"`
	Sources       []Source                 `json:"sources"`
	Reason        string                   `json:"reason"`
}

type AlbumMergeRequest struct {
	SourceAlbumID uuid.UUID                  `json:"source_album_id"`
	Confirmed     bool                       `json:"confirmed"`
	SongMatches   []AlbumMergeSongMatchInput `json:"song_matches"`
}

type AlbumMergeSongMatchInput struct {
	SourceSongID uuid.UUID `json:"source_song_id"`
	TargetSongID uuid.UUID `json:"target_song_id"`
}

type AlbumMergeSongMatchResponse struct {
	SourceSong model.Song `json:"source_song"`
	TargetSong model.Song `json:"target_song"`
	Reason     string     `json:"reason"`
}

type AlbumMergePreviewResponse struct {
	SourceAlbum model.Album                   `json:"source_album"`
	TargetAlbum model.Album                   `json:"target_album"`
	Matches     []AlbumMergeSongMatchResponse `json:"matches"`
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

type CreatePlaylistBookmarkRequest struct {
	PlaylistID uuid.UUID `json:"playlist_id"`
}

type CreatePlaylistRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
	IsPublic    bool   `json:"is_public"`
}

type CreateArtistRequest struct {
	Name            string                   `json:"name"`
	Disambiguation  string                   `json:"disambiguation"`
	LegalName       string                   `json:"legal_name"`
	StageNames      []ArtistStageNamePayload `json:"stage_names"`
	Bio             string                   `json:"bio"`
	ImageURL        string                   `json:"image_url"`
	Nationality     string                   `json:"nationality"`
	BirthPlace      string                   `json:"birth_place"`
	BirthDate       string                   `json:"birth_date"` // YYYY-MM-DD、YYYY/MM/--、YYYY/--/-- 或 ----/--/--
	BirthYear       int                      `json:"birth_year"`
	DeathYear       int                      `json:"death_year"`
	ArtistForm      string                   `json:"artist_form"`
	ActiveStartDate string                   `json:"active_start_date"` // 支持完全未知日期 ----/--/--
	ActiveEndDate   string                   `json:"active_end_date"`   // 支持完全未知日期 ----/--/--
	Members         []ArtistMemberPayload    `json:"members"`
	Sources         []Source                 `json:"sources"`
	DraftContext    string                   `json:"draft_context"`
}

type UpdatePlaylistRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	CoverURL    *string `json:"cover_url"`
	IsPublic    *bool   `json:"is_public"`
}

type AddPlaylistSongRequest struct {
	SongID uuid.UUID `json:"song_id"`
}

type ReorderPlaylistSongsRequest struct {
	SongIDs []uuid.UUID `json:"song_ids"`
}

type RecordSongPlayRequest struct {
	SongID uuid.UUID `json:"song_id"`
}

type MusicRecommendationEventName string

const (
	MusicRecommendationEventImpression   MusicRecommendationEventName = "impression"
	MusicRecommendationEventClick        MusicRecommendationEventName = "click"
	MusicRecommendationEventPlayStart    MusicRecommendationEventName = "play_start"
	MusicRecommendationEventPlayComplete MusicRecommendationEventName = "play_complete"
	MusicRecommendationEventSkip         MusicRecommendationEventName = "skip"
)

type MusicRecommendationEventInput struct {
	Event      MusicRecommendationEventName `json:"event"`
	EntityType string                       `json:"entity_type"`
	EntityID   uuid.UUID                    `json:"entity_id"`
	Position   int                          `json:"position"`
	Reason     string                       `json:"reason"`
}

type RecordMusicRecommendationEventsRequest struct {
	RequestID string                          `json:"request_id"`
	Surface   string                          `json:"surface"`
	Events    []MusicRecommendationEventInput `json:"events"`
}

type SavePlaybackProgressRequest struct {
	SongID          uuid.UUID `json:"song_id" binding:"required"`
	PositionSeconds float64   `json:"position_seconds"`
	DurationSeconds float64   `json:"duration_seconds"`
	Completed       bool      `json:"completed"`
	ReportedAt      time.Time `json:"reported_at"`
}

type SavePlaybackSessionRequest struct {
	SongIDs         []uuid.UUID `json:"song_ids" binding:"required"`
	CurrentSongID   uuid.UUID   `json:"current_song_id" binding:"required"`
	PositionSeconds float64     `json:"position_seconds"`
	PlaybackMode    string      `json:"playback_mode" binding:"required"`
	ReportedAt      time.Time   `json:"reported_at"`
}

type PlaybackSessionResponse struct {
	Queue           []model.Song `json:"queue"`
	CurrentSongID   uuid.UUID    `json:"current_song_id"`
	PositionSeconds float64      `json:"position_seconds"`
	PlaybackMode    string       `json:"playback_mode"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

type PlaylistSongStatusResponse struct {
	Data struct {
		SongIDs []uuid.UUID `json:"song_ids"`
	} `json:"data"`
}

type PlaylistSummaryResponse struct {
	ID          uuid.UUID `json:"id"`
	UserID      uuid.UUID `json:"user_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CoverURL    string    `json:"cover_url,omitempty"`
	IsPublic    bool      `json:"is_public"`
	Kind        string    `json:"kind"`
	SongCount   int64     `json:"song_count"`
}

type PaginationMetaResponse struct {
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
	HasMore  bool  `json:"has_more"`
}

type PlaylistSummaryListResponse struct {
	Data []PlaylistSummaryResponse `json:"data"`
	Meta PaginationMetaResponse    `json:"meta"`
}

type PlaylistBookmarkListResponse struct {
	Data []model.PlaylistBookmark `json:"data"`
	Meta PaginationMetaResponse   `json:"meta"`
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
	Position   int                 `json:"position"`
}

type ListeningHistoryItemResponse struct {
	ID           uuid.UUID           `json:"id"`
	Song         *PlaylistSongDetail `json:"song"`
	PlayCount    int64               `json:"play_count"`
	LastPlayedAt time.Time           `json:"last_played_at"`
}

type ListeningHistoryListResponse struct {
	Data []ListeningHistoryItemResponse `json:"data"`
	Meta PaginationMetaResponse         `json:"meta"`
}

type HomeResponse struct {
	RequestID         string                        `json:"request_id,omitempty"`
	Personalized      bool                          `json:"personalized"`
	ContinueListening *model.MusicPlaybackProgress  `json:"continue_listening,omitempty"`
	RecentlyPlayed    []model.MusicListeningHistory `json:"recently_played"`
	ForYou            []HomeAlbumRecommendation     `json:"for_you"`
	ForYouReason      string                        `json:"for_you_reason,omitempty"`
}

type HomeAlbumRecommendation struct {
	ID                   uuid.UUID         `json:"id"`
	Title                string            `json:"title"`
	CoverURL             string            `json:"cover_url,omitempty"`
	Status               string            `json:"status,omitempty"`
	EntryStatus          string            `json:"entry_status"`
	Year                 int               `json:"year,omitempty"`
	ReleaseDate          string            `json:"release_date,omitempty"`
	ReleaseDatePrecision string            `json:"release_date_precision,omitempty"`
	AlbumType            string            `json:"album_type,omitempty"`
	Artists              []HomeAlbumArtist `json:"artists,omitempty"`
	PlayCount            int64             `json:"play_count"`
	BookmarkCount        int64             `json:"bookmark_count"`
	SongCount            int64             `json:"song_count"`
	Reason               string            `json:"reason"`
}

type HomeAlbumArtist struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type PlaylistSongDetail struct {
	ID            uuid.UUID       `json:"id"`
	Title         string          `json:"title"`
	TrackNumber   int             `json:"track_number"`
	AudioURL      string          `json:"audio_url"`
	WaveformPeaks json.RawMessage `json:"waveform_peaks" swaggertype:"array,integer"`
	CoverURL      string          `json:"cover_url"`
	EntryStatus   string          `json:"entry_status"`
}

type ArtistMemberGroupItemResponse struct {
	ArtistID           uuid.UUID `json:"artist_id"`
	Name               string    `json:"name"`
	ImageURL           string    `json:"image_url,omitempty"`
	JoinDate           string    `json:"join_date,omitempty"`
	JoinDatePrecision  string    `json:"join_date_precision,omitempty"`
	LeaveDate          string    `json:"leave_date,omitempty"`
	LeaveDatePrecision string    `json:"leave_date_precision,omitempty"`
	IsPublished        bool      `json:"is_published"`
}

type ArtistMemberGroupsResponse struct {
	Current []ArtistMemberGroupItemResponse `json:"current"`
	Former  []ArtistMemberGroupItemResponse `json:"former"`
}

type ArtistDetailResponse struct {
	ID                       uuid.UUID                  `json:"id"`
	Name                     string                     `json:"name"`
	Disambiguation           string                     `json:"disambiguation,omitempty"`
	DisplayName              string                     `json:"display_name"`
	LegalName                string                     `json:"legal_name"`
	StageNamesJSON           string                     `json:"stage_names_json"`
	Bio                      string                     `json:"bio"`
	ImageURL                 string                     `json:"image_url"`
	Nationality              string                     `json:"nationality"`
	BirthPlace               string                     `json:"birth_place"`
	BirthDate                any                        `json:"birth_date,omitempty"`
	BirthDatePrecision       string                     `json:"birth_date_precision,omitempty"`
	BirthYear                int                        `json:"birth_year"`
	DeathYear                int                        `json:"death_year"`
	ArtistForm               string                     `json:"artist_form"`
	ActiveStartDate          string                     `json:"active_start_date,omitempty"`
	ActiveStartDatePrecision string                     `json:"active_start_date_precision,omitempty"`
	ActiveEndDate            string                     `json:"active_end_date,omitempty"`
	ActiveEndDatePrecision   string                     `json:"active_end_date_precision,omitempty"`
	Members                  string                     `json:"members"`
	EntryStatus              string                     `json:"entry_status"`
	LifecycleStatus          string                     `json:"lifecycle_status"`
	EditStatus               string                     `json:"edit_status"`
	RedirectTo               *uuid.UUID                 `json:"redirect_to,omitempty"`
	Albums                   any                        `json:"albums,omitempty"`
	Aliases                  any                        `json:"aliases,omitempty"`
	PlayCount                int64                      `json:"play_count"`
	BookmarkCount            int64                      `json:"bookmark_count"`
	MemberGroups             ArtistMemberGroupsResponse `json:"member_groups"`
	Sources                  []Source                   `json:"sources,omitempty"`
}
