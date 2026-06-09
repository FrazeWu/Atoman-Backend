package model

import (
	"time"

	"github.com/google/uuid"
)

type Artist struct {
	Base
	Name        string        `json:"name" gorm:"unique;not null"`
	Bio         string        `json:"bio" gorm:"type:text"`
	ImageURL    string        `json:"image_url"`
	Nationality string        `json:"nationality"`
	BirthYear   int           `json:"birth_year"`
	DeathYear   int           `json:"death_year"`
	Members     string        `json:"members" gorm:"type:text"`
	EntryStatus string        `json:"entry_status" gorm:"default:'open'"`
	RedirectTo  *uuid.UUID    `json:"redirect_to,omitempty" gorm:"type:uuid"`
	Albums      []Album       `json:"albums,omitempty" gorm:"many2many:album_artists;"`
	Songs       []Song        `json:"songs,omitempty" gorm:"many2many:song_artists;"`
	Aliases     []ArtistAlias `json:"aliases,omitempty" gorm:"foreignKey:ArtistID"`
}

func (Artist) TableName() string {
	return "Artists"
}

type Album struct {
	Base
	Title       string     `json:"title" gorm:"not null"`
	Year        int        `json:"year"`
	ReleaseDate time.Time  `json:"release_date" gorm:"type:date"`
	CoverURL    string     `json:"cover_url"`
	CoverSource string     `json:"cover_source" gorm:"default:'local'"`
	Status      string     `json:"status" gorm:"default:'open'"`
	AlbumType   string     `json:"album_type" gorm:"default:'album'"`
	EntryStatus string     `json:"entry_status" gorm:"default:'open'"`
	UploadedBy  *uuid.UUID `json:"uploaded_by" gorm:"type:uuid"`
	User        *User      `json:"user,omitempty" gorm:"foreignKey:UploadedBy;references:UUID"`
	Artists     []Artist   `json:"artists,omitempty" gorm:"many2many:album_artists;"`
	Songs       []Song     `json:"songs,omitempty" gorm:"foreignKey:AlbumID"`
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
