package model

import "github.com/google/uuid"

type MusicSongLyric struct {
	Base
	SongID      uuid.UUID `json:"song_id" gorm:"type:uuid;not null;uniqueIndex:idx_music_song_lyrics_song"`
	Content     string    `json:"content" gorm:"type:text;not null;default:''"`
	Translation string    `json:"translation" gorm:"type:text;not null;default:''"`
	Format      string    `json:"format" gorm:"not null;default:'plain';check:chk_music_song_lyrics_format,format IN ('plain','lrc')"`
	Version     int       `json:"version" gorm:"not null;default:1"`
	UpdatedBy   uuid.UUID `json:"updated_by" gorm:"type:uuid;not null;index"`
	EditSummary string    `json:"edit_summary" gorm:"type:text;not null;default:''"`
}

func (MusicSongLyric) TableName() string { return "music_song_lyrics" }

type MusicSongLyricLine struct {
	Base
	LyricID     uuid.UUID `json:"lyric_id" gorm:"type:uuid;not null;index"`
	LineKey     string    `json:"line_key" gorm:"not null"`
	LineIndex   int       `json:"line_index" gorm:"not null"`
	TimeMS      *int      `json:"time_ms,omitempty"`
	Text        string    `json:"text" gorm:"type:text;not null"`
	Translation string    `json:"translation" gorm:"type:text;not null;default:''"`
}

func (MusicSongLyricLine) TableName() string { return "music_song_lyric_lines" }

type MusicSongLyricVersion struct {
	Base
	SongID      uuid.UUID `json:"song_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_song_lyric_versions_song_version,priority:1"`
	Version     int       `json:"version" gorm:"not null;uniqueIndex:idx_music_song_lyric_versions_song_version,priority:2"`
	Content     string    `json:"content" gorm:"type:text;not null;default:''"`
	Translation string    `json:"translation" gorm:"type:text;not null;default:''"`
	Format      string    `json:"format" gorm:"not null;default:'plain';check:chk_music_song_lyric_versions_format,format IN ('plain','lrc')"`
	EditSummary string    `json:"edit_summary" gorm:"type:text;not null;default:''"`
	CreatedBy   uuid.UUID `json:"created_by" gorm:"type:uuid;not null;index"`
}

func (MusicSongLyricVersion) TableName() string { return "music_song_lyric_versions" }

type MusicLyricAnnotation struct {
	Base
	SongID       uuid.UUID `json:"song_id" gorm:"type:uuid;not null;index"`
	LineID       uuid.UUID `json:"line_id" gorm:"type:uuid;not null;index"`
	SelectedText string    `json:"selected_text" gorm:"type:text;not null"`
	StartOffset  int       `json:"start_offset" gorm:"not null"`
	EndOffset    int       `json:"end_offset" gorm:"not null"`
	Body         string    `json:"body" gorm:"type:text;not null"`
	CreatedBy    uuid.UUID `json:"created_by" gorm:"type:uuid;not null;index"`
	Status       string    `json:"status" gorm:"not null;default:'active';check:chk_music_lyric_annotations_status,status IN ('active','needs_rebind','deleted')"`
}

func (MusicLyricAnnotation) TableName() string { return "music_lyric_annotations" }

type MusicLyricAnnotationVote struct {
	Base
	AnnotationID uuid.UUID `json:"annotation_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_lyric_annotation_votes_annotation_user,priority:1"`
	UserID       uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_music_lyric_annotation_votes_annotation_user,priority:2"`
	Vote         string    `json:"vote" gorm:"not null;check:chk_music_lyric_annotation_votes_vote,vote IN ('up','down')"`
}

func (MusicLyricAnnotationVote) TableName() string { return "music_lyric_annotation_votes" }
