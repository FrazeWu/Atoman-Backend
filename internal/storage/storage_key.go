package storage

import (
	"path/filepath"
	"strings"
	"time"
)

func buildYearMonth(t time.Time) string {
	return t.UTC().Format("2006/01")
}

func cleanFilename(name string) string {
	base := filepath.Base(name)
	if base == "." || base == string(filepath.Separator) {
		return "unknown"
	}
	return base
}

func BuildUserMediaKey(prefix, kind, userID, filename string, createdAt time.Time) string {
	return strings.Trim(prefix, "/") + "/" + strings.Trim(kind, "/") + "/users/" + strings.Trim(userID, "/") + "/" + buildYearMonth(createdAt) + "/" + cleanFilename(filename)
}

func BuildMusicUploadKey(kind, userID, filename string, createdAt time.Time) string {
	return "music/" + strings.Trim(kind, "/") + "/uploads/users/" + strings.Trim(userID, "/") + "/" + buildYearMonth(createdAt) + "/" + cleanFilename(filename)
}

func BuildMusicAlbumCoverKey(albumID, ext string) string {
	return "music/albums/" + strings.Trim(albumID, "/") + "/cover" + normalizeExtension(ext)
}

func BuildMusicAlbumTrackKey(albumID, songID, ext string) string {
	return "music/albums/" + strings.Trim(albumID, "/") + "/tracks/" + strings.Trim(songID, "/") + normalizeExtension(ext)
}

func normalizeExtension(ext string) string {
	ext = strings.TrimSpace(ext)
	if ext == "" || strings.HasPrefix(ext, ".") {
		return ext
	}
	return "." + ext
}
