package model

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type MusicSource struct {
	Type  string `json:"type"`
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
}

type Artist struct {
	Base
	Name                     string         `json:"name" gorm:"not null"`
	Disambiguation           string         `json:"disambiguation,omitempty"`
	DisplayName              string         `json:"display_name" gorm:"-"`
	LegalName                string         `json:"legal_name"`
	StageNamesJSON           string         `json:"stage_names_json" gorm:"type:text"`
	Bio                      string         `json:"bio" gorm:"type:text"`
	ImageURL                 string         `json:"image_url"`
	Nationality              string         `json:"nationality"`
	BirthPlace               string         `json:"birth_place"`
	BirthDate                *time.Time     `json:"birth_date,omitempty" gorm:"type:date"`
	BirthDatePrecision       string         `json:"birth_date_precision,omitempty"`
	BirthYear                int            `json:"birth_year"`
	DeathYear                int            `json:"death_year"`
	ArtistForm               string         `json:"artist_form" gorm:"default:'person'"`
	ActiveStartDate          time.Time      `json:"active_start_date,omitempty" gorm:"type:date"`
	ActiveStartDatePrecision string         `json:"active_start_date_precision,omitempty"`
	ActiveEndDate            time.Time      `json:"active_end_date,omitempty" gorm:"type:date"`
	ActiveEndDatePrecision   string         `json:"active_end_date_precision,omitempty"`
	Members                  string         `json:"members" gorm:"type:text"`
	EntryStatus              string         `json:"entry_status" gorm:"default:'open'"`
	LifecycleStatus          string         `json:"lifecycle_status" gorm:"not null;default:'active';index;check:chk_artists_lifecycle_status,lifecycle_status IN ('draft','active','retired','merged')"`
	EditStatus               string         `json:"edit_status" gorm:"not null;default:'development';index;check:chk_artists_edit_status,edit_status IN ('development','locked','closed')"`
	CreatedBy                *uuid.UUID     `json:"created_by,omitempty" gorm:"type:uuid;index"`
	SourcesJSON              string         `json:"-" gorm:"type:jsonb;default:'[]'"`
	Sources                  []MusicSource  `json:"sources,omitempty" gorm:"-"`
	RedirectTo               *uuid.UUID     `json:"redirect_to,omitempty" gorm:"type:uuid"`
	Albums                   []Album        `json:"albums,omitempty" gorm:"many2many:album_artists;"`
	Songs                    []Song         `json:"songs,omitempty" gorm:"many2many:song_artists;"`
	Aliases                  []ArtistAlias  `json:"aliases,omitempty" gorm:"foreignKey:ArtistID"`
	MemberRelations          []ArtistMember `json:"-" gorm:"foreignKey:GroupArtistID"`
	PlayCount                int64          `json:"play_count" gorm:"-"`
	BookmarkCount            int64          `json:"bookmark_count" gorm:"-"`
}

func (Artist) TableName() string {
	return "Artists"
}

func (artist *Artist) AfterFind(_ *gorm.DB) error {
	artist.Albums = uniqueAlbumsByID(artist.Albums)
	artist.DisplayName = strings.TrimSpace(artist.Name)
	if disambiguation := strings.TrimSpace(artist.Disambiguation); disambiguation != "" {
		artist.DisplayName += "（" + disambiguation + "）"
	}
	if strings.TrimSpace(artist.SourcesJSON) != "" {
		_ = json.Unmarshal([]byte(artist.SourcesJSON), &artist.Sources)
	}
	return nil
}

type ArtistMember struct {
	Base
	GroupArtistID      uuid.UUID  `json:"group_artist_id" gorm:"type:uuid;index;not null"`
	GroupArtist        *Artist    `json:"group_artist,omitempty" gorm:"foreignKey:GroupArtistID"`
	MemberArtistID     uuid.UUID  `json:"member_artist_id" gorm:"type:uuid;index;not null"`
	MemberArtist       *Artist    `json:"member_artist,omitempty" gorm:"foreignKey:MemberArtistID"`
	JoinDate           *time.Time `json:"join_date,omitempty" gorm:"type:date"`
	JoinDatePrecision  string     `json:"join_date_precision,omitempty"`
	LeaveDate          *time.Time `json:"leave_date,omitempty" gorm:"type:date"`
	LeaveDatePrecision string     `json:"leave_date_precision,omitempty"`
}

func (ArtistMember) TableName() string {
	return "artist_members"
}

type Album struct {
	Base
	Title                string        `json:"title" gorm:"not null"`
	Description          string        `json:"description" gorm:"type:text"`
	Year                 int           `json:"year"`
	ReleaseYear          int           `json:"release_year"`
	ReleaseDate          time.Time     `json:"release_date" gorm:"type:date"`
	ReleaseDatePrecision string        `json:"release_date_precision,omitempty"`
	CoverURL             string        `json:"cover_url"`
	CoverSource          string        `json:"cover_source" gorm:"default:'local'"`
	Status               string        `json:"status" gorm:"default:'open'"`
	AlbumType            string        `json:"album_type" gorm:"default:'album'"`
	EditionType          string        `json:"edition_type" gorm:"default:'original'"`
	MusicBrainzMatched   bool          `json:"-" gorm:"column:musicbrainz_matched;not null;default:false;index"`
	MusicBrainzReleaseID string        `json:"-" gorm:"column:musicbrainz_release_id;type:varchar(36);index"`
	MusicBrainzMatchedAt *time.Time    `json:"-" gorm:"column:musicbrainz_matched_at"`
	CanonicalAlbumID     *uuid.UUID    `json:"canonical_album_id,omitempty" gorm:"type:uuid;index"`
	HotScore             float64       `json:"hot_score" gorm:"default:0;index"`
	EntryStatus          string        `json:"entry_status" gorm:"default:'open'"`
	LifecycleStatus      string        `json:"lifecycle_status" gorm:"not null;default:'active';index;check:chk_albums_lifecycle_status,lifecycle_status IN ('draft','active','retired','merged')"`
	EditStatus           string        `json:"edit_status" gorm:"not null;default:'development';index;check:chk_albums_edit_status,edit_status IN ('development','locked','closed')"`
	SourcesJSON          string        `json:"-" gorm:"type:jsonb;default:'[]'"`
	Sources              []MusicSource `json:"sources,omitempty" gorm:"-"`
	RedirectTo           *uuid.UUID    `json:"redirect_to,omitempty" gorm:"type:uuid;index"`
	UploadedBy           *uuid.UUID    `json:"uploaded_by" gorm:"type:uuid"`
	User                 *User         `json:"user,omitempty" gorm:"foreignKey:UploadedBy;references:UUID"`
	Artists              []Artist      `json:"artists,omitempty" gorm:"many2many:album_artists;"`
	ArtistCredits        []AlbumArtist `json:"artist_credits,omitempty" gorm:"foreignKey:AlbumID"`
	Songs                []Song        `json:"songs,omitempty" gorm:"foreignKey:AlbumID"`
	OtherVersions        []Album       `json:"other_versions,omitempty" gorm:"-"`
	PlayCount            int64         `json:"play_count"`
	SongCount            int64         `json:"song_count" gorm:"-"`
	BookmarkCount        int64         `json:"bookmark_count" gorm:"-"`
	RatingScore          float64       `json:"rating_score" gorm:"-"`
	RatingCount          int64         `json:"rating_count" gorm:"-"`
	ViewerRating         *int          `json:"viewer_rating,omitempty" gorm:"-"`
}

func (Album) TableName() string {
	return "Albums"
}

type AlbumRating struct {
	Base
	UserID  uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_album_ratings_user_album,priority:1,where:deleted_at IS NULL"`
	AlbumID uuid.UUID `json:"album_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_album_ratings_user_album,priority:2,where:deleted_at IS NULL"`
	Score   int       `json:"score" gorm:"not null;check:chk_album_ratings_score,score BETWEEN 1 AND 5"`
}

func (AlbumRating) TableName() string { return "album_ratings" }

func (album *Album) AfterFind(_ *gorm.DB) error {
	album.Artists = uniqueArtistsByID(album.Artists)
	if strings.TrimSpace(album.SourcesJSON) != "" {
		_ = json.Unmarshal([]byte(album.SourcesJSON), &album.Sources)
	}
	return nil
}

func uniqueArtistsByID(artists []Artist) []Artist {
	unique := make([]Artist, 0, len(artists))
	seen := make(map[uuid.UUID]struct{}, len(artists))
	for _, artist := range artists {
		if _, exists := seen[artist.ID]; exists {
			continue
		}
		seen[artist.ID] = struct{}{}
		unique = append(unique, artist)
	}
	return unique
}

func uniqueAlbumsByID(albums []Album) []Album {
	unique := make([]Album, 0, len(albums))
	seen := make(map[uuid.UUID]struct{}, len(albums))
	for _, album := range albums {
		if _, exists := seen[album.ID]; exists {
			continue
		}
		seen[album.ID] = struct{}{}
		unique = append(unique, album)
	}
	return unique
}

type AlbumArtist struct {
	AlbumID    uuid.UUID `json:"album_id" gorm:"type:uuid;primaryKey"`
	ArtistID   uuid.UUID `json:"artist_id" gorm:"type:uuid;primaryKey"`
	Artist     *Artist   `json:"artist,omitempty" gorm:"foreignKey:ArtistID;references:ID"`
	Role       string    `json:"role" gorm:"primaryKey;default:'primary'"`
	CustomRole string    `json:"custom_role" gorm:"primaryKey;default:''"`
	Position   int       `json:"position" gorm:"default:1"`
	CreatedAt  time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt  time.Time `json:"updated_at" gorm:"column:updated_at"`
}

func (AlbumArtist) TableName() string {
	return "album_artists"
}

type Song struct {
	Base
	Title                string          `json:"title" gorm:"not null"`
	Description          string          `json:"description" gorm:"type:text"`
	ReleaseType          *string         `json:"release_type,omitempty" gorm:"type:varchar(16);index"`
	ReleaseDate          time.Time       `json:"release_date" gorm:"type:date"`
	ReleaseDatePrecision string          `json:"release_date_precision,omitempty"`
	TrackNumber          int             `json:"track_number"`
	DiscNumber           int             `json:"disc_number" gorm:"default:1"`
	Lyrics               string          `json:"lyrics" gorm:"type:text"`
	AudioURL             string          `json:"audio_url" gorm:"not null"`
	SourceKey            string          `json:"source_key" gorm:"type:text"`
	PlaybackKey          string          `json:"playback_key" gorm:"type:text"`
	AudioSource          string          `json:"audio_source" gorm:"default:'local'"`
	CoverURL             string          `json:"cover_url"`
	CoverSource          string          `json:"cover_source" gorm:"default:'local'"`
	SourcesJSON          string          `json:"-" gorm:"type:jsonb;default:'[]'"`
	Sources              []MusicSource   `json:"sources,omitempty" gorm:"-"`
	EffectiveSources     []MusicSource   `json:"effective_sources,omitempty" gorm:"-"`
	BatchID              string          `json:"batch_id" gorm:"index"`
	Status               string          `json:"status" gorm:"default:'open'"`
	LifecycleStatus      string          `json:"lifecycle_status" gorm:"not null;default:'active';index;check:chk_songs_lifecycle_status,lifecycle_status IN ('draft','active','retired','merged')"`
	EditStatus           string          `json:"edit_status" gorm:"not null;default:'development';index;check:chk_songs_edit_status,edit_status IN ('development','locked','closed')"`
	AlbumID              *uuid.UUID      `json:"album_id" gorm:"type:uuid"`
	Album                *Album          `json:"album,omitempty"`
	Artists              []Artist        `json:"artists,omitempty" gorm:"many2many:song_artists;"`
	ArtistCredits        []SongArtist    `json:"artist_credits,omitempty" gorm:"foreignKey:SongID"`
	UploadedBy           *uuid.UUID      `json:"uploaded_by" gorm:"type:uuid"`
	User                 *User           `json:"user,omitempty" gorm:"foreignKey:UploadedBy;references:UUID"`
	PlayCount            int64           `json:"play_count" gorm:"default:0"`
	DurationSec          int             `json:"duration_sec" gorm:"default:0"`
	SourceFileName       string          `json:"source_file_name"`
	SourceContainer      string          `json:"source_container"`
	SourceCodec          string          `json:"source_codec"`
	SourceBitrateKbps    int             `json:"source_bitrate_kbps" gorm:"default:0"`
	SourceSampleRateHz   int             `json:"source_sample_rate_hz" gorm:"default:0"`
	SourceBitDepth       int             `json:"source_bit_depth" gorm:"default:0"`
	SourceChannels       int             `json:"source_channels" gorm:"default:0"`
	SourceSizeBytes      int64           `json:"source_size_bytes" gorm:"default:0"`
	SourceLossless       bool            `json:"source_lossless" gorm:"default:false"`
	PlaybackContainer    string          `json:"playback_container"`
	PlaybackCodec        string          `json:"playback_codec"`
	PlaybackBitrateKbps  int             `json:"playback_bitrate_kbps" gorm:"default:0"`
	PlaybackSampleRateHz int             `json:"playback_sample_rate_hz" gorm:"default:0"`
	PlaybackChannels     int             `json:"playback_channels" gorm:"default:0"`
	WaveformPeaks        json.RawMessage `json:"waveform_peaks" gorm:"type:jsonb;not null;default:'[]'" swaggertype:"array,integer"`
	RatingScore          float64         `json:"rating_score" gorm:"-"`
	RatingCount          int64           `json:"rating_count" gorm:"-"`
	ViewerRating         *int            `json:"viewer_rating,omitempty" gorm:"-"`
}

func (Song) TableName() string {
	return "Songs"
}

type SongRating struct {
	Base
	UserID uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_song_ratings_user_song,priority:1,where:deleted_at IS NULL"`
	SongID uuid.UUID `json:"song_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_song_ratings_user_song,priority:2,where:deleted_at IS NULL"`
	Score  int       `json:"score" gorm:"not null;check:chk_song_ratings_score,score BETWEEN 1 AND 5"`
}

func (SongRating) TableName() string { return "song_ratings" }

func (song *Song) AfterFind(_ *gorm.DB) error {
	if strings.TrimSpace(song.SourcesJSON) != "" {
		if err := json.Unmarshal([]byte(song.SourcesJSON), &song.Sources); err != nil {
			song.Sources = nil
		}
	}
	if len(song.Sources) > 0 {
		song.EffectiveSources = append([]MusicSource(nil), song.Sources...)
	}
	return nil
}

func (song *Song) BeforeCreate(tx *gorm.DB) error {
	if err := song.Base.BeforeCreate(tx); err != nil {
		return err
	}
	if len(song.WaveformPeaks) == 0 {
		song.WaveformPeaks = json.RawMessage("[]")
	}
	return nil
}

type SongArtist struct {
	SongID     uuid.UUID `json:"song_id" gorm:"type:uuid;primaryKey"`
	ArtistID   uuid.UUID `json:"artist_id" gorm:"type:uuid;primaryKey"`
	Artist     *Artist   `json:"artist,omitempty" gorm:"foreignKey:ArtistID;references:ID"`
	Role       string    `json:"role" gorm:"primaryKey;default:'primary'"`
	CustomRole string    `json:"custom_role" gorm:"primaryKey;default:''"`
	Position   int       `json:"position" gorm:"default:1"`
	CreatedAt  time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt  time.Time `json:"updated_at" gorm:"column:updated_at"`
}

func (SongArtist) TableName() string {
	return "song_artists"
}

type SongAudioReplacement struct {
	Base
	SongID           uuid.UUID  `json:"song_id" gorm:"type:uuid;not null;index"`
	Song             *Song      `json:"song,omitempty" gorm:"foreignKey:SongID"`
	RequestedBy      uuid.UUID  `json:"requested_by" gorm:"type:uuid;not null;index"`
	AssetID          *uuid.UUID `json:"asset_id,omitempty" gorm:"type:uuid;index"`
	AudioURL         string     `json:"audio_url" gorm:"type:text;not null"`
	SourceKey        string     `json:"source_key" gorm:"type:text"`
	PreviousAudioURL string     `json:"previous_audio_url" gorm:"type:text"`
	Status           string     `json:"status" gorm:"not null;default:'pending';index"`
	ErrorMessage     string     `json:"error_message" gorm:"type:text"`
	LockedBy         string     `json:"locked_by"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	RevisionID       *uuid.UUID `json:"revision_id,omitempty" gorm:"type:uuid"`
}

func (SongAudioReplacement) TableName() string { return "music_song_audio_replacements" }

type AlbumCorrection struct {
	Base
	AlbumID uuid.UUID  `json:"album_id" gorm:"type:uuid;not null"`
	Album   *Album     `json:"album,omitempty" gorm:"foreignKey:AlbumID"`
	UserID  *uuid.UUID `json:"user_id" gorm:"type:uuid"`
	User    *User      `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Status  string     `json:"status" gorm:"default:'pending'"`

	OriginalTitle       string     `json:"original_title"`
	OriginalCoverURL    string     `json:"original_cover_url" gorm:"type:text"`
	OriginalReleaseDate *time.Time `json:"original_release_date" gorm:"type:date"`
	OriginalArtistIDs   string     `json:"original_artist_ids" gorm:"type:text"`

	CorrectedTitle       string     `json:"corrected_title"`
	CorrectedCoverURL    string     `json:"corrected_cover_url" gorm:"type:text"`
	CorrectedCoverSource string     `json:"corrected_cover_source" gorm:"default:'local'"`
	CorrectedReleaseDate *time.Time `json:"corrected_release_date" gorm:"type:date"`
	CorrectedArtistIDs   string     `json:"corrected_artist_ids" gorm:"type:text"`

	Reason         string     `json:"reason" gorm:"type:text"`
	ApprovedAt     *time.Time `json:"approved_at"`
	ApprovedBy     *uuid.UUID `json:"approved_by" gorm:"type:uuid"`
	ApprovedByUser *User      `json:"approved_by_user,omitempty" gorm:"foreignKey:ApprovedBy;references:UUID"`
	RejectedAt     *time.Time `json:"rejected_at"`
	RejectedBy     *uuid.UUID `json:"rejected_by" gorm:"type:uuid"`
	RejectedByUser *User      `json:"rejected_by_user,omitempty" gorm:"foreignKey:RejectedBy;references:UUID"`
}

func (AlbumCorrection) TableName() string {
	return "album_corrections"
}

type SongCorrection struct {
	Base
	SongID uuid.UUID  `json:"song_id" gorm:"type:uuid;not null"`
	Song   *Song      `json:"song,omitempty" gorm:"foreignKey:SongID"`
	UserID *uuid.UUID `json:"user_id" gorm:"type:uuid"`
	User   *User      `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Status string     `json:"status" gorm:"default:'pending'"`

	FieldName      string `json:"field_name" gorm:"not null"`
	CurrentValue   string `json:"current_value" gorm:"type:text"`
	CorrectedValue string `json:"corrected_value" gorm:"type:text;not null"`

	Reason         string     `json:"reason" gorm:"type:text"`
	ApprovedAt     *time.Time `json:"approved_at"`
	ApprovedBy     *uuid.UUID `json:"approved_by" gorm:"type:uuid"`
	ApprovedByUser *User      `json:"approved_by_user,omitempty" gorm:"foreignKey:ApprovedBy;references:UUID"`
	RejectedAt     *time.Time `json:"rejected_at"`
	RejectedBy     *uuid.UUID `json:"rejected_by" gorm:"type:uuid"`
	RejectedByUser *User      `json:"rejected_by_user,omitempty" gorm:"foreignKey:RejectedBy;references:UUID"`
}

func (SongCorrection) TableName() string {
	return "song_corrections"
}

// ArtistCorrection is a proposed change to a confirmed Artist entry, submitted by users.
// Status: pending | approved | rejected
type ArtistCorrection struct {
	Base
	ArtistID    uuid.UUID  `json:"artist_id" gorm:"type:uuid;not null"`
	Artist      *Artist    `json:"artist,omitempty" gorm:"foreignKey:ArtistID"`
	UserID      *uuid.UUID `json:"user_id" gorm:"type:uuid"`
	User        *User      `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Description string     `json:"description" gorm:"type:text;not null"` // 修改说明
	Reason      string     `json:"reason" gorm:"type:text"`               // 修改理由
	Status      string     `json:"status" gorm:"default:'pending'"`       // pending|approved|rejected
	ApprovedBy  *uuid.UUID `json:"approved_by" gorm:"type:uuid"`
	ApprovedAt  *time.Time `json:"approved_at"`
}

func (ArtistCorrection) TableName() string { return "artist_corrections" }

// ArtistAlias represents an alternative name for an artist
type ArtistAlias struct {
	Base
	ArtistID   uuid.UUID `json:"artist_id" gorm:"type:uuid;index;not null"`
	Artist     *Artist   `json:"artist,omitempty" gorm:"foreignKey:ArtistID"`
	Alias      string    `json:"alias" gorm:"not null"`
	IsMainName bool      `json:"is_main_name" gorm:"default:false"`
}

func (ArtistAlias) TableName() string {
	return "artist_aliases"
}

// ArtistMerge records when one artist was merged into another
type ArtistMerge struct {
	Base
	SourceArtistID uuid.UUID `json:"source_artist_id" gorm:"type:uuid;not null;index"`
	TargetArtistID uuid.UUID `json:"target_artist_id" gorm:"type:uuid;not null;index"`
	MergedBy       uuid.UUID `json:"merged_by" gorm:"type:uuid;not null"`
	MergedByUser   *User     `json:"merged_by_user,omitempty" gorm:"foreignKey:MergedBy;references:UUID"`
	MergedAt       time.Time `json:"merged_at"`
}

func (ArtistMerge) TableName() string {
	return "artist_merges"
}

type ArtistBookmark struct {
	Base
	UserID   uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_artist_bookmarks_user_artist,priority:1,where:deleted_at IS NULL"`
	ArtistID uuid.UUID `json:"artist_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_artist_bookmarks_user_artist,priority:2,where:deleted_at IS NULL"`
	Artist   *Artist   `json:"artist,omitempty" gorm:"foreignKey:ArtistID"`
}

func (ArtistBookmark) TableName() string {
	return "music_artist_bookmarks"
}

type AlbumBookmark struct {
	Base
	UserID  uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_album_bookmarks_user_album,priority:1,where:deleted_at IS NULL"`
	AlbumID uuid.UUID `json:"album_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_album_bookmarks_user_album,priority:2,where:deleted_at IS NULL"`
	Album   *Album    `json:"album,omitempty" gorm:"foreignKey:AlbumID"`
}

func (AlbumBookmark) TableName() string {
	return "music_album_bookmarks"
}

type Playlist struct {
	Base
	UserID        uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	User          *User     `json:"-" gorm:"foreignKey:UserID;references:UUID"`
	Name          string    `json:"name" gorm:"not null"`
	Description   string    `json:"description" gorm:"type:text"`
	CoverURL      string    `json:"cover_url"`
	IsPublic      bool      `json:"is_public" gorm:"default:false;index"`
	Kind          string    `json:"kind" gorm:"default:'user';index"`
	OwnerUsername string    `json:"owner_username,omitempty" gorm:"-"`
	SongCount     int64     `json:"song_count" gorm:"-"`
}

func (Playlist) TableName() string {
	return "music_playlists"
}

type PlaylistBookmark struct {
	Base
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_playlist_bookmarks_user_playlist,priority:1,where:deleted_at IS NULL"`
	PlaylistID uuid.UUID `json:"playlist_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_playlist_bookmarks_user_playlist,priority:2,where:deleted_at IS NULL"`
	Playlist   *Playlist `json:"playlist,omitempty" gorm:"foreignKey:PlaylistID"`
}

func (PlaylistBookmark) TableName() string {
	return "music_playlist_bookmarks"
}

type PlaylistSong struct {
	Base
	PlaylistID uuid.UUID `json:"playlist_id" gorm:"type:uuid;not null;index;index:idx_music_playlist_songs_playlist_position,priority:1;uniqueIndex:idx_music_playlist_songs_playlist_song,priority:1,where:deleted_at IS NULL"`
	SongID     uuid.UUID `json:"song_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_playlist_songs_playlist_song,priority:2,where:deleted_at IS NULL"`
	Song       *Song     `json:"song,omitempty" gorm:"foreignKey:SongID"`
	Position   int       `json:"position" gorm:"not null;default:0;index:idx_music_playlist_songs_playlist_position,priority:2"`
}

func (PlaylistSong) TableName() string {
	return "music_playlist_songs"
}

type MusicListeningHistory struct {
	Base
	UserID       uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index:idx_music_listening_history_user_last_played,priority:1;uniqueIndex:idx_music_listening_history_user_song,priority:1,where:deleted_at IS NULL"`
	SongID       uuid.UUID `json:"song_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_listening_history_user_song,priority:2,where:deleted_at IS NULL"`
	Song         *Song     `json:"song,omitempty" gorm:"foreignKey:SongID"`
	PlayCount    int64     `json:"play_count" gorm:"not null;default:1"`
	LastPlayedAt time.Time `json:"last_played_at" gorm:"not null;index:idx_music_listening_history_user_last_played,priority:2,sort:desc"`
}

func (MusicListeningHistory) TableName() string {
	return "music_listening_histories"
}

type MusicPlaybackSession struct {
	Base
	UserID          uuid.UUID       `json:"user_id" gorm:"type:uuid;not null;uniqueIndex:idx_music_playback_sessions_user,where:deleted_at IS NULL"`
	CurrentSongID   *uuid.UUID      `json:"current_song_id,omitempty" gorm:"type:uuid;index"`
	QueueJSON       json.RawMessage `json:"-" gorm:"type:jsonb;not null;default:'[]'"`
	PositionSeconds float64         `json:"position_seconds" gorm:"not null;default:0"`
	PlaybackMode    string          `json:"playback_mode" gorm:"type:varchar(16);not null;default:'loop'"`
	ReportedAt      time.Time       `json:"reported_at" gorm:"not null;default:CURRENT_TIMESTAMP;index"`
}

func (MusicPlaybackSession) TableName() string {
	return "music_playback_sessions"
}

type MusicPlaybackProgress struct {
	Base
	UserID          uuid.UUID `json:"user_id" gorm:"type:uuid;not null;uniqueIndex:idx_music_playback_progress_user_song,priority:1,where:deleted_at IS NULL"`
	SongID          uuid.UUID `json:"song_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_playback_progress_user_song,priority:2,where:deleted_at IS NULL"`
	Song            *Song     `json:"song,omitempty" gorm:"foreignKey:SongID"`
	PositionSeconds float64   `json:"position_seconds" gorm:"not null;default:0"`
	DurationSeconds float64   `json:"duration_seconds" gorm:"not null;default:0"`
	Completed       bool      `json:"completed" gorm:"not null;default:false"`
	ReportedAt      time.Time `json:"reported_at" gorm:"not null;default:CURRENT_TIMESTAMP;index"`
	UpdatedAt       time.Time `json:"updated_at" gorm:"not null;index:idx_music_playback_progress_user_updated,priority:2,sort:desc"`
}

func (MusicPlaybackProgress) TableName() string {
	return "music_playback_progresses"
}

type MusicSearchInteraction struct {
	Base
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	Query      string    `json:"query" gorm:"type:varchar(200);not null"`
	EntityType string    `json:"entity_type" gorm:"type:varchar(16);not null;index"`
	EntityID   uuid.UUID `json:"entity_id" gorm:"type:uuid;not null;index"`
}

func (MusicSearchInteraction) TableName() string {
	return "music_search_interactions"
}
