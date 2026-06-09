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
