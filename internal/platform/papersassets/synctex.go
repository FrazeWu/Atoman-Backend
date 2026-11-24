package papersassets

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
)

// A compact line-level index derived from the actual compiled SyncTeX data.
// Only project-relative source paths are retained; neither TeX installation
// paths nor generated auxiliary-file names leave the isolated workspace.
const MaxSourceMapBytes = 384 << 10
const maxSourceMapPoints = 12000

type SourceMap struct {
	Version int      `json:"version"`
	Files   []string `json:"files"`
	// [file index, 1-based line, 1-based page, x*10, y*10] in PDF points,
	// measured from the top-left of the unrotated page.
	Points    [][5]int `json:"points"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (m SourceMap) Validate() error {
	if m.Version != 1 || len(m.Files) < 1 || len(m.Files) > 64 || len(m.Points) < 1 || len(m.Points) > maxSourceMapPoints {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if !ValidSourcePath(f) || SourceKind(f) != "text" || seen[f] {
			return ErrInvalid
		}
		seen[f] = true
	}
	for _, p := range m.Points {
		if p[0] < 0 || p[0] >= len(m.Files) || p[1] < 1 || p[1] > 1048576 || p[2] < 1 || p[2] > 1000 || p[3] < 0 || p[3] > 200000 || p[4] < 0 || p[4] > 200000 {
			return ErrInvalid
		}
	}
	return nil
}
func ValidateSourceMap(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxSourceMapBytes {
		return ErrInvalid
	}
	var m SourceMap
	if err := json.Unmarshal(raw, &m); err != nil {
		return ErrInvalid
	}
	return m.Validate()
}

func ParseSyncTeX(src io.Reader, snapshot Snapshot) ([]byte, error) {
	gz, err := gzip.NewReader(io.LimitReader(src, 8<<20))
	if err != nil {
		return nil, ErrInvalid
	}
	defer gz.Close()
	allowed := map[string]int{}
	for _, f := range snapshot.Files {
		if SourceKind(f.Path) == "text" && f.Kind != "binary" {
			allowed[f.Path] = strings.Count(f.Content, "\n") + 1
		}
	}
	m := SourceMap{Version: 1}
	tags := map[int]int{}
	indices := map[string]int{}
	seen := map[[5]int]bool{}
	// Bound decompressed size, line length, record count, and every coordinate.
	limited := &io.LimitedReader{R: gz, N: (32 << 20) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 8192)
	unit, mag, xOffset, yOffset := 1, 1000, 0, 0
	page := 0
	content := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Input:") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) != 3 {
				continue
			}
			tag, e := strconv.Atoi(parts[1])
			if e != nil || tag < 1 || tag > 1000000 {
				continue
			}
			p := parts[2]
			if strings.HasPrefix(p, "/inputs/") {
				p = strings.TrimPrefix(p, "/inputs/")
			} else if strings.HasPrefix(p, "/") {
				continue
			}
			p = path.Clean(p)
			if _, ok := allowed[p]; !ok {
				continue
			}
			i, ok := indices[p]
			if !ok {
				i = len(m.Files)
				if i >= 64 {
					continue
				}
				indices[p] = i
				m.Files = append(m.Files, p)
			}
			tags[tag] = i
			continue
		}
		if !content {
			for _, field := range []struct {
				name string
				v    *int
			}{{"Unit:", &unit}, {"Magnification:", &mag}, {"X Offset:", &xOffset}, {"Y Offset:", &yOffset}} {
				if strings.HasPrefix(line, field.name) {
					v, e := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, field.name)))
					if e != nil || v < -1000000000 || v > 1000000000 {
						return nil, ErrInvalid
					}
					*field.v = v
				}
			}
			if line == "Content:" {
				if unit <= 0 || unit > 100000 || mag <= 0 || mag > 100000 {
					return nil, ErrInvalid
				}
				content = true
			}
			continue
		}
		if strings.HasPrefix(line, "{") {
			p, e := strconv.Atoi(line[1:])
			if e != nil || p < 1 || p > 1000 {
				page = 0
			} else {
				page = p
			}
			continue
		}
		if strings.HasPrefix(line, "}") {
			page = 0
			continue
		}
		if line == "Postamble:" {
			break
		}
		if page == 0 || len(line) < 3 || (line[0] != 'x' && line[0] != 'h') {
			continue
		}
		parts := strings.SplitN(line[1:], ":", 3)
		if len(parts) < 2 {
			continue
		}
		a := strings.Split(parts[0], ",")
		b := strings.Split(parts[1], ",")
		if len(a) < 2 || len(b) != 2 {
			continue
		}
		tag, e1 := strconv.Atoi(a[0])
		ln, e2 := strconv.Atoi(a[1])
		x, e3 := strconv.ParseInt(b[0], 10, 64)
		y, e4 := strconv.ParseInt(b[1], 10, 64)
		i, ok := tags[tag]
		if !ok || e1 != nil || e2 != nil || e3 != nil || e4 != nil || ln < 1 || ln > allowed[m.Files[i]] {
			continue
		}
		factor := float64(unit) * float64(mag) / 1000 * 72 / 72.27 / 65536
		xx := math.Round((float64(x) + float64(xOffset)) * factor * 10)
		yy := math.Round((float64(y) + float64(yOffset)) * factor * 10)
		if line[0] == 'h' && len(parts) == 3 {
			dim := strings.Split(parts[2], ",")
			if len(dim) == 3 {
				w, we := strconv.ParseInt(dim[0], 10, 64)
				h, he := strconv.ParseInt(dim[1], 10, 64)
				if we == nil && he == nil && w > 0 && h > 0 && float64(h)*factor < 400 {
					xx += math.Round(float64(w) * factor * 5)
					yy -= math.Round(float64(h) * factor * 5)
				}
			}
		}
		if xx < 0 || xx > 200000 || yy < 0 || yy > 200000 {
			continue
		}
		p := [5]int{i, ln, page, int(xx), int(yy)}
		// Keep several horizontal anchors per line (two-column layouts), not
		// every glyph. Deduplicate at about 36pt horizontal / 2pt vertical.
		k := [5]int{i, ln, page, p[3] / 360, p[4] / 20}
		if seen[k] {
			continue
		}
		if len(m.Points) >= maxSourceMapPoints {
			m.Truncated = true
			continue
		}
		seen[k] = true
		m.Points = append(m.Points, p)
	}
	if scanner.Err() != nil || limited.N <= 0 || !content || m.Validate() != nil {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxSourceMapBytes {
		m.Truncated = true
		for len(raw) > MaxSourceMapBytes && len(m.Points) > 1 {
			m.Points = m.Points[:len(m.Points)*3/4]
			raw, err = json.Marshal(m)
			if err != nil {
				return nil, err
			}
		}
	}
	if ValidateSourceMap(raw) != nil {
		return nil, ErrInvalid
	}
	return raw, nil
}
