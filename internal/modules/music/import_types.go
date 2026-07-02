package music

type AlbumImportTrackPayload struct {
	Title       string `json:"title"`
	TrackNumber int    `json:"track_number"`
}

type ArtistStageNamePayload struct {
	Name          string `json:"name"`
	IsPrimary     bool   `json:"is_primary"`
	StartDateText string `json:"start_date_text"`
	EndDateText   string `json:"end_date_text"`
}

type AlbumImportArtistPayload struct {
	Name       string                   `json:"name"`
	LegalName  string                   `json:"legal_name"`
	StageNames []ArtistStageNamePayload `json:"stage_names"`
	BirthPlace string                   `json:"birth_place"`
}

type AlbumImportAlbumPayload struct {
	Title       string                    `json:"title"`
	ReleaseYear int                       `json:"release_year"`
	Tracks      []AlbumImportTrackPayload `json:"tracks"`
}

type AlbumImportPayload struct {
	Artist AlbumImportArtistPayload `json:"artist"`
	Album  AlbumImportAlbumPayload  `json:"album"`
}

type CreateAlbumImportSessionInput struct {
	Status  string             `json:"status"`
	Payload AlbumImportPayload `json:"payload"`
}

type CommitAlbumImportSessionInput struct {
	ArtistID string                   `json:"artist_id"`
	Artist AlbumImportArtistPayload `json:"artist"`
	Album  AlbumImportAlbumPayload  `json:"album"`
}

type AlbumImportDTOTrack struct {
	Title    string `json:"title"`
	AudioKey string `json:"audioKey"`
	AudioURL string `json:"audioUrl"`
	Origin   string `json:"origin"`
}

type AlbumImportDTO struct {
	ImportID          string                `json:"importId"`
	Status            string                `json:"status"`
	ArchiveName       string                `json:"archiveName"`
	UploadProgress    float64               `json:"uploadProgress"`
	UploadSpeed       float64               `json:"uploadSpeed"`
	CoverURL          string                `json:"coverUrl"`
	CoverKey          string                `json:"coverKey"`
	DerivedAlbumTitle string                `json:"derivedAlbumTitle"`
	DerivedCover      string                `json:"derivedCover"`
	DerivedTracks     []AlbumImportDTOTrack `json:"derivedTracks"`
	LastSyncedAt      string                `json:"lastSyncedAt"`
	ErrorMessage      string                `json:"errorMessage"`
}

const (
	AlbumImportStatusPendingUpload = "pending_upload"
	AlbumImportStatusReady         = "ready"
	AlbumImportStatusCommitted     = "committed"
)
