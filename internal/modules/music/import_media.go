package music

import (
	"os"
	"strings"
)

func resolveAlbumImportCoverURL(payload map[string]any) string {
	for _, field := range []string{"cover_url", "derived_cover"} {
		if value := strings.TrimSpace(stringValue(payload[field])); value != "" {
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
