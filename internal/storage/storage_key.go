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

const (
	UserAvatarOldSlot = "old"
	UserAvatarNewSlot = "new"
)

func BuildUserAvatarSlotKey(userID, slot string) string {
	return "users/avatars/" + strings.Trim(userID, "/") + "/" + strings.Trim(slot, "/")
}

func BuildMusicUploadKey(kind, userID, filename string, createdAt time.Time) string {
	return "music/" + strings.Trim(kind, "/") + "/uploads/users/" + strings.Trim(userID, "/") + "/" + buildYearMonth(createdAt) + "/" + cleanFilename(filename)
}

func BuildBookPrivateObjectKey(userID, importID, format string) string {
	return "books/private/users/" + strings.Trim(userID, "/") + "/imports/" + strings.Trim(importID, "/") + "/source" + normalizeExtension(format)
}

func BuildBookPublishedObjectKey(assetID, format string) string {
	return "books/public/assets/" + strings.Trim(assetID, "/") + "/source" + normalizeExtension(format)
}

func BuildBookPublicationEvidenceObjectKey(requestID, evidenceID, extension string) string {
	return "books/private/publication-evidence/requests/" + strings.Trim(requestID, "/") + "/" + strings.Trim(evidenceID, "/") + normalizeExtension(extension)
}

func BuildMusicAlbumCoverKey(albumID, ext string) string {
	return "music/albums/" + strings.Trim(albumID, "/") + "/cover" + normalizeExtension(ext)
}

func BuildMusicAlbumTrackKey(albumID, songID, ext string) string {
	return "music/albums/" + strings.Trim(albumID, "/") + "/tracks/" + strings.Trim(songID, "/") + normalizeExtension(ext)
}

func BuildMusicAlbumCoverVersionKey(albumID, assetID, ext string) string {
	return "music/albums/" + strings.Trim(albumID, "/") + "/covers/" + strings.Trim(assetID, "/") + normalizeExtension(ext)
}

func BuildMusicAlbumTrackVersionKey(albumID, songID, assetID, ext string) string {
	return "music/albums/" + strings.Trim(albumID, "/") + "/tracks/" + strings.Trim(songID, "/") + "/" + strings.Trim(assetID, "/") + normalizeExtension(ext)
}

func BuildMusicArtistImageVersionKey(artistID, assetID, ext string) string {
	return "music/artists/" + strings.Trim(artistID, "/") + "/images/" + strings.Trim(assetID, "/") + normalizeExtension(ext)
}

func BuildMusicPlaylistCoverVersionKey(playlistID, assetID, ext string) string {
	return "music/playlists/" + strings.Trim(playlistID, "/") + "/covers/" + strings.Trim(assetID, "/") + normalizeExtension(ext)
}

func BuildMusicSongCoverVersionKey(songID, assetID, ext string) string {
	return "music/songs/" + strings.Trim(songID, "/") + "/covers/" + strings.Trim(assetID, "/") + normalizeExtension(ext)
}

func BuildMusicSongAudioVersionKey(songID, assetID, ext string) string {
	return "music/songs/" + strings.Trim(songID, "/") + "/audio/" + strings.Trim(assetID, "/") + normalizeExtension(ext)
}

func normalizeExtension(ext string) string {
	ext = strings.TrimSpace(ext)
	if ext == "" || strings.HasPrefix(ext, ".") {
		return ext
	}
	return "." + ext
}
