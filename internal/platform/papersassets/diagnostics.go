package papersassets

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxDiagnosticsBytes = 12 << 10
const MaxDiagnostics = 12

// CompileDiagnostic is an untrusted, bounded excerpt of a compiler error.
// Only snapshot-relative source filenames are accepted as navigation targets.
type CompileDiagnostic struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
	Tool    string `json:"tool"`
}

var latexFileLine = regexp.MustCompile(`^([^:]{1,520}):([1-9][0-9]{0,6}):[[:space:]]*(.+)$`)
var bibLine = regexp.MustCompile(`(?i)(?:---)?line ([1-9][0-9]{0,6}) of file ([^[:cntrl:]]{1,520})$`)

// Suppress guest/runtime absolute paths in error excerpts; project paths
// appear separately only when independently matched against the snapshot.
var pathToken = regexp.MustCompile(`[^[:space:]'"` + "\x60" + `]*[/][^[:space:]'"` + "\x60" + `]*`)

func cleanDiagnostic(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(pathToken.ReplaceAllString(s, "[path omitted]")), " ")
	if len(s) > 360 {
		s = s[:360]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return strings.TrimSpace(s)
}

func diagnosticSource(name string, allowed map[string]int) string {
	name = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(name), "/inputs/"), "./")
	if _, ok := allowed[name]; ok {
		return name
	}
	return ""
}
func diagnosticLocation(d *CompileDiagnostic, name, rawLine string, allowed map[string]int) {
	n, err := strconv.Atoi(rawLine)
	path := diagnosticSource(name, allowed)
	if err == nil && n > 0 && path != "" && n <= allowed[path] {
		d.File = path
		d.Line = n
	}
}

func ParseCompileDiagnostics(texLog, bibLog string, snapshot Snapshot) []CompileDiagnostic {
	if snapshot.Validate() != nil {
		return nil
	}
	allowed := map[string]int{}
	for _, f := range snapshot.Files {
		if SourceKind(f.Path) == "text" {
			allowed[f.Path] = strings.Count(f.Content, "\n") + 1
		}
	}
	var out []CompileDiagnostic
	seen := map[string]bool{}
	add := func(d CompileDiagnostic) {
		d.Message = cleanDiagnostic(d.Message)
		if d.Message == "" || len(out) >= MaxDiagnostics {
			return
		}
		key := d.Tool + "|" + d.File + "|" + strconv.Itoa(d.Line) + "|" + d.Message
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, d)
	}
	// pdflatex -file-line-error provides explicit locations. Never infer a
	// nested file from a log's unmatched parenthesis or expose arbitrary paths.
	for _, line := range strings.Split(texLog, "\n") {
		line = strings.TrimSpace(line)
		if len(line) > 2048 {
			continue
		}
		if m := latexFileLine.FindStringSubmatch(line); m != nil {
			d := CompileDiagnostic{Tool: "latex", Message: m[3]}
			diagnosticLocation(&d, m[1], m[2], allowed)
			if d.File != "" {
				add(d)
			} else if strings.Contains(m[3], "Error:") || strings.Contains(m[3], "Undefined control sequence") || strings.Contains(m[3], "Emergency stop") {
				add(d)
			}
		} else if strings.HasPrefix(line, "! ") {
			add(CompileDiagnostic{Tool: "latex", Message: strings.TrimPrefix(line, "! ")})

		}
		if len(out) >= MaxDiagnostics {
			break
		}
	}
	for _, line := range strings.Split(bibLog, "\n") {
		line = strings.TrimSpace(line)
		if len(line) > 2048 {
			continue
		}
		if m := bibLine.FindStringSubmatch(line); m != nil {
			d := CompileDiagnostic{Tool: "bibtex", Message: strings.TrimSpace(line[:strings.LastIndex(line, m[0])])}
			diagnosticLocation(&d, m[2], m[1], allowed)
			if d.Message == "" {
				d.Message = "BibTeX syntax error"
			}
			add(d)
		} else if strings.HasPrefix(line, "I couldn't open ") || strings.HasPrefix(line, "I found no ") || strings.HasPrefix(line, "Illegal, another ") || strings.HasPrefix(line, "Too many ") {
			add(CompileDiagnostic{Tool: "bibtex", Message: line})
		}
		if len(out) >= MaxDiagnostics {
			break
		}
	}
	return out
}

// Validate again at the queue boundary. The launcher wire payload and database
// must not carry arbitrary log objects, host paths, or unbounded strings.
func EncodeCompileDiagnostics(items []CompileDiagnostic) string {
	if len(items) == 0 || len(items) > MaxDiagnostics {
		return ""
	}
	for _, d := range items {
		if d.Tool != "latex" && d.Tool != "bibtex" {
			return ""
		}
		if d.Message == "" || len(d.Message) > 360 || d.Message != cleanDiagnostic(d.Message) {
			return ""
		}
		if d.File == "" {
			if d.Line != 0 {
				return ""
			}
		} else if !ValidSourcePath(d.File) || SourceKind(d.File) != "text" || d.Line < 1 || d.Line > 1048576 {
			return ""
		}
	}
	raw, err := json.Marshal(items)
	if err != nil || len(raw) > MaxDiagnosticsBytes {
		return ""
	}
	return string(raw)
}
func DecodeCompileDiagnostics(raw string) []CompileDiagnostic {
	if raw == "" || len(raw) > MaxDiagnosticsBytes {
		return nil
	}
	var items []CompileDiagnostic
	if json.Unmarshal([]byte(raw), &items) != nil || EncodeCompileDiagnostics(items) == "" {
		return nil
	}
	return items
}
