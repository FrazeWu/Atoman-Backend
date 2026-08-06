package music

import (
	"errors"
	"os"
	"path"
	"strconv"
	"strings"

	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
)

const (
	defaultAlbumImportMaxTotalBytes int64 = 10 * 1024 * 1024 * 1024
	defaultAlbumImportMaxFileBytes  int64 = 4 * 1024 * 1024 * 1024
	defaultAlbumImportMaxFiles            = 5000
	defaultAlbumImportMaxTracks           = 300

	albumImportMaxTotalBytesEnv = "MUSIC_ALBUM_IMPORT_MAX_TOTAL_BYTES"
	albumImportMaxFileBytesEnv  = "MUSIC_ALBUM_IMPORT_MAX_FILE_BYTES"
	albumImportMaxFilesEnv      = "MUSIC_ALBUM_IMPORT_MAX_FILES"
	albumImportMaxTracksEnv     = "MUSIC_ALBUM_IMPORT_MAX_TRACKS"
)

type albumImportUploadLimits struct {
	MaxTotalBytes int64
	MaxFileBytes  int64
	MaxFiles      int
	MaxTracks     int
}

type normalizedAlbumImportFile struct {
	AlbumImportFileInput
	Role   string
	Format string
}

type albumImportCleanupTarget struct {
	Action   string `json:"action"`
	Key      string `json:"key"`
	UploadID string `json:"uploadId,omitempty"`
}

var errAlbumImportObjectSizeMismatch = errors.New("album import object size mismatch")

func albumImportUploadLimitsFromEnv() albumImportUploadLimits {
	return albumImportUploadLimits{
		MaxTotalBytes: positiveInt64Env(albumImportMaxTotalBytesEnv, defaultAlbumImportMaxTotalBytes),
		MaxFileBytes:  positiveInt64Env(albumImportMaxFileBytesEnv, defaultAlbumImportMaxFileBytes),
		MaxFiles:      int(positiveInt64Env(albumImportMaxFilesEnv, defaultAlbumImportMaxFiles)),
		MaxTracks:     int(positiveInt64Env(albumImportMaxTracksEnv, defaultAlbumImportMaxTracks)),
	}
}

func positiveInt64Env(key string, fallback int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func detectAlbumImportFileRole(fileName string) (string, string, error) {
	lower := strings.ToLower(strings.TrimSpace(fileName))
	archives := []string{"tar.bz2", "tar.gz", "tar.xz", "tgz", "zip", "rar", "7z", "tar"}
	for _, format := range archives {
		if strings.HasSuffix(lower, "."+format) {
			return AlbumImportFileRoleArchive, format, nil
		}
	}
	audio := map[string]bool{
		"mp3": true, "flac": true, "wav": true, "m4a": true, "aac": true, "ogg": true,
		"opus": true, "aiff": true, "aif": true, "wma": true, "ape": true, "alac": true,
	}
	covers := map[string]bool{
		"jpg": true, "jpeg": true, "png": true, "webp": true, "avif": true,
		"heic": true, "heif": true, "tiff": true, "tif": true, "bmp": true,
	}
	extension := strings.TrimPrefix(path.Ext(lower), ".")
	switch {
	case audio[extension]:
		return AlbumImportFileRoleAudio, extension, nil
	case extension == "cue":
		return AlbumImportFileRoleCue, extension, nil
	case covers[extension]:
		return AlbumImportFileRoleCover, extension, nil
	default:
		return "", "", apperr.BadRequest("validation.invalid_request", "unsupported album import file type")
	}
}

func normalizeAlbumImportFiles(input []AlbumImportFileInput, limits albumImportUploadLimits) ([]normalizedAlbumImportFile, string, error) {
	if len(input) == 0 || len(input) > limits.MaxFiles {
		return nil, "", apperr.BadRequest("validation.invalid_request", "album import file count is invalid")
	}

	files := make([]normalizedAlbumImportFile, 0, len(input))
	var totalSize int64
	hasFolder := false
	archiveCount := 0
	trackCount := 0
	folderRoot := ""
	for _, raw := range input {
		fileName := strings.TrimSpace(raw.FileName)
		relativePath := strings.ReplaceAll(strings.TrimSpace(raw.RelativePath), `\`, "/")
		if fileName == "" || relativePath == "" || strings.ContainsAny(fileName, `/\`) {
			return nil, "", apperr.BadRequest("validation.invalid_request", "album import file name is invalid")
		}
		if isAbsoluteAlbumImportPath(relativePath) {
			return nil, "", apperr.BadRequest("validation.invalid_request", "album import relative path is invalid")
		}
		for _, segment := range strings.Split(relativePath, "/") {
			if segment == ".." {
				return nil, "", apperr.BadRequest("validation.invalid_request", "album import relative path is invalid")
			}
		}
		relativePath = path.Clean(relativePath)
		if relativePath == "." || path.Base(relativePath) != fileName {
			return nil, "", apperr.BadRequest("validation.invalid_request", "album import relative path is invalid")
		}
		if shouldIgnoreAlbumImportPath(relativePath) {
			continue
		}
		if raw.FileSize <= 0 || raw.FileSize > limits.MaxFileBytes || totalSize > limits.MaxTotalBytes-raw.FileSize {
			return nil, "", apperr.BadRequest("validation.invalid_request", "album import file size is invalid")
		}
		role, format, err := detectAlbumImportFileRole(fileName)
		if err != nil {
			return nil, "", err
		}
		if role == AlbumImportFileRoleArchive {
			archiveCount++
		} else if role == AlbumImportFileRoleAudio {
			trackCount++
			if trackCount > limits.MaxTracks {
				return nil, "", apperr.BadRequest("validation.invalid_request", "album import track count is invalid")
			}
		}
		totalSize += raw.FileSize
		hasFolder = hasFolder || strings.Contains(relativePath, "/")
		files = append(files, normalizedAlbumImportFile{
			AlbumImportFileInput: AlbumImportFileInput{
				RelativePath: relativePath,
				FileName:     fileName,
				FileSize:     raw.FileSize,
				ContentType:  strings.TrimSpace(raw.ContentType),
			},
			Role: role, Format: format,
		})
	}
	if len(files) == 0 {
		return nil, "", apperr.BadRequest("validation.invalid_request", "no supported album import files found")
	}

	if archiveCount > 0 {
		if archiveCount != 1 || len(files) != 1 {
			return nil, "", apperr.BadRequest("validation.invalid_request", "archive cannot be mixed with other files")
		}
		return files, AlbumImportInputModeArchive, nil
	}
	if hasFolder {
		for _, file := range files {
			segments := strings.Split(file.RelativePath, "/")
			if len(segments) < 2 {
				return nil, "", apperr.BadRequest("validation.invalid_request", "folder import must use one album root")
			}
			if folderRoot == "" {
				folderRoot = segments[0]
			} else if segments[0] != folderRoot {
				return nil, "", apperr.BadRequest("validation.invalid_request", "folder import must use one album root")
			}
		}
		return files, AlbumImportInputModeFolder, nil
	}
	return files, AlbumImportInputModeFiles, nil
}

func isAbsoluteAlbumImportPath(value string) bool {
	if strings.HasPrefix(value, "/") {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && value[2] == '/'
}

func albumImportSourceKey(userID, sessionID, fileID uuid.UUID, format string) string {
	return "music/album-imports/source/users/" + userID.String() + "/sessions/" + sessionID.String() + "/files/" + fileID.String() + "/objects/" + uuid.NewString() + "/source." + format
}
