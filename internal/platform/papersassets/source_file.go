package papersassets

import (
	"encoding/base64"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// User source paths are never interpreted as host command arguments. Keep a
// conservative set of portable filename characters, with no hidden/dot
// components, traversal, separators other than slash or control characters.
func ValidSourcePath(name string) bool {
	if name == "" || len(name) > 512 || !utf8.ValidString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || len(part) > 255 || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.TrimSpace(part) != part {
			return false
		}
		for _, r := range part {
			if !(unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '_' || r == '-' || r == '.' || r == ' ' || r == '(' || r == ')') {
				return false
			}
		}
	}
	return true
}
func SourceKind(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".tex", ".bib", ".sty", ".cls", ".txt", ".md", ".bst", ".bbl", ".cfg", ".def", ".clo", ".bbx", ".cbx", ".lbx", ".fd", ".dtx", ".ins":
		return "text"
	case ".png", ".jpg", ".jpeg", ".pdf":
		return "binary"
	default:
		return ""
	}
}
func (f SourceFile) Bytes() ([]byte, error) {
	if !ValidSourcePath(f.Path) {
		return nil, ErrInvalid
	}
	want := SourceKind(f.Path)
	if want == "text" && (f.Kind == "" || f.Kind == "text") {
		if !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) {
			return nil, ErrInvalid
		}
		return []byte(f.Content), nil
	}
	if want != "binary" || f.Kind != "binary" || len(f.Content) > MaxSnapshotBytes {
		return nil, ErrInvalid
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(f.Content)
	if err != nil || len(raw) == 0 || base64.StdEncoding.EncodeToString(raw) != f.Content {
		return nil, ErrInvalid
	}
	// These are untrusted graphics, not host-side decoded images. TeX opens
	// them only inside the existing isolated no-shell-escape runtime.
	return raw, nil
}
