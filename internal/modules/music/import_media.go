package music

import (
	"os"
	"strings"
)

func isAlbumImportPreviewURL(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "blob:") || strings.HasPrefix(value, "data:")
}

func resolveAlbumImportCommitCoverURL(value string, payload map[string]any) string {
	value = strings.TrimSpace(value)
	if value == "" || isAlbumImportPreviewURL(value) {
		return resolveAlbumImportCoverURL(payload)
	}
	return value
}

func resolveAlbumImportCoverURL(payload map[string]any) string {
	for _, field := range []string{"cover_url", "derived_cover"} {
		if value := strings.TrimSpace(stringValue(payload[field])); value != "" && !isAlbumImportPreviewURL(value) {
			return value
		}
	}

	key := strings.TrimLeft(strings.TrimSpace(stringValue(payload["cover_key"])), "/")
	prefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
	if key == "" || prefix == "" {
		return ""
	}
	return prefix + "/" + key
}
