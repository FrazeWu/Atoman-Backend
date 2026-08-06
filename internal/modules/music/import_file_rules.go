package music

import "strings"

func shouldIgnoreAlbumImportPath(value string) bool {
	normalized := strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	for _, segment := range strings.Split(normalized, "/") {
		segment = strings.TrimSpace(segment)
		if segment == "" || segment == "." || segment == ".." {
			continue
		}
		lower := strings.ToLower(segment)
		if strings.HasPrefix(segment, ".") {
			return true
		}
		switch lower {
		case "__macosx", "thumbs.db", "desktop.ini", "system volume information":
			return true
		}
	}
	return false
}
