package model

import (
	"time"

	"github.com/google/uuid"
)

type Artist struct {
	Base
	Name            string         `json:"name" gorm:"unique;not null"`
	LegalName       string         `json:"legal_name"`
	StageNamesJSON  string         `json:"stage_names_json" gorm:"type:text"`
	Bio             string         `json:"bio" gorm:"type:text"`
	ImageURL        string         `json:"image_url"`
	Nationality     string         `json:"nationality"`
	BirthPlace      string         `json:"birth_place"`
	BirthDate       *time.Time     `json:"birth_date,omitempty" gorm:"type:date"`
	BirthYear       int            `json:"birth_year"`
	DeathYear       int            `json:"death_year"`
	ArtistForm      string         `json:"artist_form" gorm:"default:'person'"`
	ActiveStartDate time.Time      `json:"active_start_date,omitempty" gorm:"type:date"`
	ActiveEndDate   time.Time      `json:"active_end_date,omitempty" gorm:"type:date"`
	Members         string         `json:"members" gorm:"type:text"`
	EntryStatus     string         `json:"entry_status" gorm:"default:'open'"`
	RedirectTo      *uuid.UUID     `json:"redirect_to,omitempty" gorm:"type:uuid"`
	Albums          []Album        `json:"albums,omitempty" gorm:"many2many:album_artists;"`
	Songs           []Song         `json:"songs,omitempty" gorm:"many2many:song_artists;"`
	Aliases         []ArtistAlias  `json:"aliases,omitempty" gorm:"foreignKey:ArtistID"`
	MemberRelations []ArtistMember `json:"-" gorm:"foreignKey:GroupArtistID"`
	PlayCount       int64          `json:"play_count" gorm:"-"`
	BookmarkCount   int64          `json:"bookmark_count" gorm:"-"`
}

func (Artist) TableName() string {
	return "Artists"
}

type ArtistMember struct {
	Base
	GroupArtistID  uuid.UUID  `json:"group_artist_id" gorm:"type:uuid;index;not null"`
	GroupArtist    *Artist    `json:"group_artist,omitempty" gorm:"foreignKey:GroupArtistID"`
	MemberArtistID uuid.UUID  `json:"member_artist_id" gorm:"type:uuid;index;not null"`
	MemberArtist   *Artist    `json:"member_artist,omitempty" gorm:"foreignKey:MemberArtistID"`
	JoinDate       *time.Time `json:"join_date,omitempty" gorm:"type:date"`
	LeaveDate      *time.Time `json:"leave_date,omitempty" gorm:"type:date"`
}

func (ArtistMember) TableName() string {
	return "artist_members"
}

type Album struct {
	Base
	Title         string     `json:"title" gorm:"not null"`
	Year          int        `json:"year"`
	ReleaseYear   int        `json:"release_year"`
	ReleaseDate   time.Time  `json:"release_date" gorm:"type:date"`
	CoverURL      string     `json:"cover_url"`
	CoverSource   string     `json:"cover_source" gorm:"default:'local'"`
	Status        string     `json:"status" gorm:"default:'open'"`
	AlbumType     string     `json:"album_type" gorm:"default:'album'"`
	HotScore      float64    `json:"hot_score" gorm:"default:0;index"`
	EntryStatus   string     `json:"entry_status" gorm:"default:'open'"`
	UploadedBy    *uuid.UUID `json:"uploaded_by" gorm:"type:uuid"`
	User          *User      `json:"user,omitempty" gorm:"foreignKey:UploadedBy;references:UUID"`
	Artists       []Artist   `json:"artists,omitempty" gorm:"many2many:album_artists;"`
	Songs         []Song     `json:"songs,omitempty" gorm:"foreignKey:AlbumID"`
	PlayCount     int64      `json:"play_count"`
	BookmarkCount int64      `json:"bookmark_count" gorm:"-"`
}

func (Album) TableName() string {
	return "Albums"
}

type AlbumArtist struct {
	AlbumID   uuid.UUID `json:"album_id" gorm:"type:uuid;primaryKey"`
	ArtistID  uuid.UUID `json:"artist_id" gorm:"type:uuid;primaryKey"`
	Role      string    `json:"role" gorm:"default:'primary'"`
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

func (AlbumArtist) TableName() string {
	return "album_artists"
}

type Song struct {
	Base
	Title       string     `json:"title" gorm:"not null"`
	ReleaseDate time.Time  `json:"release_date" gorm:"type:date"`
	TrackNumber int        `json:"track_number"`
	Lyrics      string     `json:"lyrics" gorm:"type:text"`
	AudioURL    string     `json:"audio_url" gorm:"not null"`
	AudioSource string     `json:"audio_source" gorm:"default:'local'"`
	CoverURL    string     `json:"cover_url"`
	CoverSource string     `json:"cover_source" gorm:"default:'local'"`
	BatchID     string     `json:"batch_id" gorm:"index"`
	Status      string     `json:"status" gorm:"default:'open'"`
	AlbumID     *uuid.UUID `json:"album_id" gorm:"type:uuid"`
	Album       *Album     `json:"album,omitempty"`
	Artists     []Artist   `json:"artists,omitempty" gorm:"many2many:song_artists;"`
	UploadedBy  *uuid.UUID `json:"uploaded_by" gorm:"type:uuid"`
	User        *User      `json:"user,omitempty" gorm:"foreignKey:UploadedBy;references:UUID"`
	PlayCount   int64      `json:"play_count" gorm:"default:0"`
}

func (Song) TableName() string {
	return "Songs"
}

type SongArtist struct {
	SongID    uuid.UUID `json:"song_id" gorm:"type:uuid;primaryKey"`
	ArtistID  uuid.UUID `json:"artist_id" gorm:"type:uuid;primaryKey"`
	Role      string    `json:"role" gorm:"default:'primary'"`
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

func (SongArtist) TableName() string {
	return "song_artists"
}

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

// LyricAnnotation is a user annotation on a specific line of song lyrics
type LyricAnnotation struct {
	Base
	SongID     uuid.UUID `json:"song_id" gorm:"type:uuid;index;not null"`
	Song       *Song     `json:"song,omitempty" gorm:"foreignKey:SongID"`
	LineNumber int       `json:"line_number" gorm:"not null"`
	Content    string    `json:"content" gorm:"type:text;not null"`
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null"`
	User       *User     `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
}

func (LyricAnnotation) TableName() string {
	return "lyric_annotations"
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

type SongBookmark struct {
	Base
	UserID uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_song_bookmarks_user_song,priority:1,where:deleted_at IS NULL"`
	SongID uuid.UUID `json:"song_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_song_bookmarks_user_song,priority:2,where:deleted_at IS NULL"`
	Song   *Song     `json:"song,omitempty" gorm:"foreignKey:SongID"`
}

func (SongBookmark) TableName() string {
	return "music_song_bookmarks"
}

type Playlist struct {
	Base
	UserID      uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	Name        string    `json:"name" gorm:"not null"`
	Description string    `json:"description" gorm:"type:text"`
	CoverURL    string    `json:"cover_url"`
	IsPublic    bool      `json:"is_public" gorm:"default:false;index"`
}

func (Playlist) TableName() string {
	return "music_playlists"
}

type PlaylistSong struct {
	Base
	PlaylistID uuid.UUID `json:"playlist_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_playlist_songs_playlist_song,priority:1,where:deleted_at IS NULL"`
	SongID     uuid.UUID `json:"song_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_playlist_songs_playlist_song,priority:2,where:deleted_at IS NULL"`
	Song       *Song     `json:"song,omitempty" gorm:"foreignKey:SongID"`
}

func (PlaylistSong) TableName() string {
	return "music_playlist_songs"
}
