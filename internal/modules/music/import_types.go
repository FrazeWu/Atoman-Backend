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

type ArtistMemberPayload struct {
	ArtistID  string `json:"artist_id"`
	JoinDate  string `json:"join_date"`
	LeaveDate string `json:"leave_date"`
}

type AlbumImportArtistPayload struct {
	Name            string                   `json:"name"`
	LegalName       string                   `json:"legal_name"`
	ImageURL        string                   `json:"image_url"`
	StageNames      []ArtistStageNamePayload `json:"stage_names"`
	BirthPlace      string                   `json:"birth_place"`
	ArtistForm      string                   `json:"artist_form"`
	ActiveStartDate string                   `json:"active_start_date"`
	ActiveEndDate   string                   `json:"active_end_date"`
	Members         []ArtistMemberPayload    `json:"members"`
}

type AlbumImportAlbumPayload struct {
	Title       string                    `json:"title"`
	CoverURL    string                    `json:"cover_url"`
	ReleaseDate string                    `json:"release_date"`
	ReleaseYear int                       `json:"release_year"`
	Tracks      []AlbumImportTrackPayload `json:"tracks"`
}

type AlbumImportPayload struct {
	Artist  AlbumImportArtistPayload   `json:"artist"`
	Artists []AlbumImportArtistPayload `json:"artists"`
	Album   AlbumImportAlbumPayload    `json:"album"`
}

type CreateAlbumImportSessionInput struct {
	Status  string             `json:"status"`
	Payload AlbumImportPayload `json:"payload"`
}

type CommitAlbumImportSessionInput struct {
	ArtistID string                         `json:"artist_id"`
	Artist   AlbumImportArtistPayload       `json:"artist"`
	Artists  []CommitAlbumImportArtistInput `json:"artists"`
	Album    AlbumImportAlbumPayload        `json:"album"`
}

type CommitAlbumImportArtistInput struct {
	ArtistID        string                   `json:"artist_id"`
	Name            string                   `json:"name"`
	LegalName       string                   `json:"legal_name"`
	ImageURL        string                   `json:"image_url"`
	StageNames      []ArtistStageNamePayload `json:"stage_names"`
	BirthPlace      string                   `json:"birth_place"`
	ArtistForm      string                   `json:"artist_form"`
	ActiveStartDate string                   `json:"active_start_date"`
	ActiveEndDate   string                   `json:"active_end_date"`
	Members         []ArtistMemberPayload    `json:"members"`
}

type StartAlbumImportMultipartInput struct {
	FileName    string `json:"fileName"`
	FileSize    int64  `json:"fileSize"`
	ContentType string `json:"contentType"`
}

type AlbumImportFileInput struct {
	RelativePath string `json:"relativePath"`
	FileName     string `json:"fileName"`
	FileSize     int64  `json:"fileSize"`
	ContentType  string `json:"contentType"`
}

type RegisterAlbumImportFilesInput struct {
	Files []AlbumImportFileInput `json:"files"`
}

type AlbumImportMultipartPartDTO struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
}

type AlbumImportMultipartDTO struct {
	ImportID       string                        `json:"importId"`
	FileName       string                        `json:"fileName"`
	FileSize       int64                         `json:"fileSize"`
	ObjectKey      string                        `json:"objectKey"`
	PartSize       int64                         `json:"partSize"`
	CompletedParts []AlbumImportMultipartPartDTO `json:"completedParts"`
}

type CreateAlbumImportMultipartPartInput struct {
	PartSize int64 `json:"partSize"`
}

type AlbumImportMultipartPartUploadDTO struct {
	PartNumber int    `json:"partNumber"`
	UploadURL  string `json:"uploadUrl"`
}

type CompleteAlbumImportMultipartPartInput struct {
	ETag string `json:"etag"`
	Size int64  `json:"size"`
}

type AlbumImportDTOTrack struct {
	Title    string `json:"title"`
	AudioKey string `json:"audioKey"`
	AudioURL string `json:"audioUrl"`
	Origin   string `json:"origin"`
}

type AlbumImportProgressDTO struct {
	Current int64 `json:"current"`
	Total   int64 `json:"total"`
}

type AlbumImportFileDTO struct {
	ID               string                        `json:"fileId"`
	RelativePath     string                        `json:"relativePath"`
	FileName         string                        `json:"fileName"`
	Role             string                        `json:"role"`
	DetectedFormat   string                        `json:"detectedFormat"`
	ContentType      string                        `json:"contentType"`
	Size             int64                         `json:"size"`
	SourceKey        string                        `json:"sourceKey"`
	PartSize         int64                         `json:"partSize"`
	CompletedParts   []AlbumImportMultipartPartDTO `json:"completedParts"`
	PlaybackKey      string                        `json:"playbackKey"`
	UploadStatus     string                        `json:"uploadStatus"`
	ProcessingStatus string                        `json:"processingStatus"`
	DiscNumber       int                           `json:"discNumber"`
	TrackNumber      int                           `json:"trackNumber"`
	Title            string                        `json:"title"`
	DurationSeconds  float64                       `json:"durationSeconds"`
	ErrorMessage     string                        `json:"errorMessage"`
}

type AlbumImportErrorDTO struct {
	FileID  string `json:"fileId"`
	Message string `json:"message"`
}

type AlbumImportDTO struct {
	ImportID          string                 `json:"importId"`
	Status            string                 `json:"status"`
	InputMode         string                 `json:"inputMode"`
	Stage             string                 `json:"stage"`
	Progress          AlbumImportProgressDTO `json:"progress"`
	Files             []AlbumImportFileDTO   `json:"files"`
	Tracks            []AlbumImportDTOTrack  `json:"tracks"`
	Errors            []AlbumImportErrorDTO  `json:"errors"`
	ArchiveName       string                 `json:"archiveName"`
	UploadProgress    float64                `json:"uploadProgress"`
	UploadSpeed       float64                `json:"uploadSpeed"`
	CoverURL          string                 `json:"coverUrl"`
	CoverKey          string                 `json:"coverKey"`
	DerivedAlbumTitle string                 `json:"derivedAlbumTitle"`
	DerivedCover      string                 `json:"derivedCover"`
	DerivedTracks     []AlbumImportDTOTrack  `json:"derivedTracks"`
	LastSyncedAt      string                 `json:"lastSyncedAt"`
	ErrorMessage      string                 `json:"errorMessage"`
}

type AlbumImportResponse struct {
	Data AlbumImportDTO `json:"data"`
}

type AlbumImportFileResponse struct {
	Data AlbumImportFileDTO `json:"data"`
}

type AlbumImportMultipartPartUploadResponse struct {
	Data AlbumImportMultipartPartUploadDTO `json:"data"`
}

const (
	AlbumImportInputModeAuto    = "auto"
	AlbumImportInputModeArchive = "archive"
	AlbumImportInputModeFiles   = "files"
	AlbumImportInputModeFolder  = "folder"

	AlbumImportFileRoleArchive = "archive"
	AlbumImportFileRoleAudio   = "audio"
	AlbumImportFileRoleCue     = "cue"
	AlbumImportFileRoleCover   = "cover"

	AlbumImportFileUploadStatusUploading  = "uploading"
	AlbumImportFileUploadStatusCompleting = "completing"
	AlbumImportFileUploadStatusUploaded   = "uploaded"
	AlbumImportFileUploadStatusFailed     = "failed"

	AlbumImportFileProcessingStatusPending = "pending"
	AlbumImportFileProcessingStatusFailed  = "failed"

	AlbumImportJobStatusQueued   = "queued"
	AlbumImportJobStatusRunning  = "running"
	AlbumImportJobStatusFailed   = "failed"
	AlbumImportJobStatusCanceled = "canceled"

	AlbumImportStageUpload      = "upload"
	AlbumImportStageQueued      = "queued"
	AlbumImportStageExtracting  = "extracting"
	AlbumImportStageAnalyzing   = "analyzing"
	AlbumImportStageTranscoding = "transcoding"
	AlbumImportStageReady       = "ready"
	AlbumImportStageCommitting  = "committing"
	AlbumImportStageCompleted   = "completed"
	AlbumImportStageFailed      = "failed"
	AlbumImportStageCanceled    = "canceled"

	AlbumImportStatusPendingUpload  = "pending_upload"
	AlbumImportStatusUploading      = "uploading"
	AlbumImportStatusUploaded       = "uploaded"
	AlbumImportStatusQueued         = "queued"
	AlbumImportStatusExtracting     = "extracting"
	AlbumImportStatusAnalyzing      = "analyzing"
	AlbumImportStatusTranscoding    = "transcoding"
	AlbumImportStatusReady          = "ready"
	AlbumImportStatusNeedsAttention = "needs_attention"
	AlbumImportStatusCommitting     = "committing"
	AlbumImportStatusFailed         = "failed"
	AlbumImportStatusCanceled       = "canceled"
	AlbumImportStatusCommitted      = "committed"
)
