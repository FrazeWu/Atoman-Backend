package books

import (
	"math"
	"strconv"
	"strings"

	"atoman/internal/platform/apperr"
)

type BookPDFRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type BookReadingMarkAnchor struct {
	EPUBStartCFI   string        `json:"epub_start_cfi,omitempty"`
	EPUBEndCFI     string        `json:"epub_end_cfi,omitempty"`
	PDFPage        int           `json:"pdf_page,omitempty"`
	PDFRects       []BookPDFRect `json:"pdf_rects,omitempty"`
	TXTOffsetStart int64         `json:"txt_offset_start,omitempty"`
	TXTOffsetEnd   int64         `json:"txt_offset_end,omitempty"`
}

type bookCFIStep struct {
	kind  byte
	value int64
}

// Only point CFIs for document/text positions are accepted here. A range is
// represented by two point CFIs (epub.js collapse), not by guessing at commas.
// Sorting follows https://w3c.github.io/epub-specs/epub33/epubcfi/#sec-sorting
// Assertions, including escaped brackets and commas, do not affect location.
func parseBookPointCFI(raw string) ([]bookCFIStep, error) {
	invalid := func() ([]bookCFIStep, error) {
		return nil, apperr.BadRequest("books.invalid_mark_anchor", "Use a document/text point EPUB CFI")
	}
	if len(raw) > bookReadingCFIMaxLength || !strings.HasPrefix(raw, "epubcfi(") || !strings.HasSuffix(raw, ")") {
		return invalid()
	}
	payload := raw[8 : len(raw)-1]
	var stripped strings.Builder
	assertion := false
	for i := 0; i < len(payload); i++ {
		c := payload[i]
		if c == '^' {
			if !assertion || i+1 >= len(payload) || !strings.ContainsRune("^[](),;=", rune(payload[i+1])) {
				return invalid()
			}
			i++
			continue
		}
		if c == '[' {
			if assertion {
				return invalid()
			}
			assertion = true
			continue
		}
		if c == ']' {
			if !assertion {
				return invalid()
			}
			assertion = false
			continue
		}
		if !assertion {
			stripped.WriteByte(c)
		}
	}
	if assertion {
		return invalid()
	}
	parts := strings.Split(stripped.String(), "!")
	if len(parts) < 2 {
		return invalid()
	}
	steps := make([]bookCFIStep, 0, 16)
	for partIndex, part := range parts {
		if partIndex > 0 {
			steps = append(steps, bookCFIStep{kind: '!'})
		}
		if len(part) == 0 || part[0] != '/' {
			return invalid()
		}
		for i := 0; i < len(part); {
			kind := part[i]
			if kind != '/' && kind != ':' {
				return invalid()
			}
			i++
			start := i
			for i < len(part) && part[i] >= '0' && part[i] <= '9' {
				i++
			}
			if i == start {
				return invalid()
			}
			value, err := strconv.ParseInt(part[start:i], 10, 64)
			if err != nil || (kind == '/' && value == 0) {
				return invalid()
			}
			steps = append(steps, bookCFIStep{kind: kind, value: value})
			if kind == ':' && (i != len(part) || partIndex != len(parts)-1) {
				return invalid()
			}
		}
	}
	return steps, nil
}

func compareBookCFISteps(a, b []bookCFIStep) int {
	rank := func(kind byte) int {
		switch kind {
		case ':':
			return 0
		case '/':
			return 1
		default:
			return 2
		}
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if x, y := rank(a[i].kind), rank(b[i].kind); x != y {
			if x < y {
				return -1
			}
			return 1
		}
		if a[i].value < b[i].value {
			return -1
		}
		if a[i].value > b[i].value {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

func compareBookPointCFI(a, b string) int {
	left, _ := parseBookPointCFI(a)
	right, _ := parseBookPointCFI(b)
	return compareBookCFISteps(left, right)
}

func validateBookReadingMarkAnchor(anchor BookReadingMarkAnchor, format, kind string) error {
	invalid := func() error {
		return apperr.BadRequest("books.invalid_mark_anchor", "Anchor must match the revision format and mark kind")
	}
	if anchor.TXTOffsetStart < 0 || anchor.TXTOffsetEnd < 0 || anchor.TXTOffsetStart > bookReadingMaxTXTOffset || anchor.TXTOffsetEnd > bookReadingMaxTXTOffset {
		return invalid()
	}
	switch format {
	case "epub":
		if anchor.PDFPage != 0 || len(anchor.PDFRects) != 0 || anchor.TXTOffsetStart != 0 || anchor.TXTOffsetEnd != 0 {
			return invalid()
		}
		start, err := parseBookPointCFI(anchor.EPUBStartCFI)
		if err != nil {
			return err
		}
		if kind == "bookmark" {
			if anchor.EPUBEndCFI != "" {
				return invalid()
			}
			return nil
		}
		end, err := parseBookPointCFI(anchor.EPUBEndCFI)
		if err != nil {
			return err
		}
		if compareBookCFISteps(start, end) >= 0 {
			return invalid()
		}
	case "pdf", "cbz", "cbr":
		if anchor.PDFPage < 1 || anchor.PDFPage > bookReadingMaxPDFPage || anchor.EPUBStartCFI != "" || anchor.EPUBEndCFI != "" || anchor.TXTOffsetStart != 0 || anchor.TXTOffsetEnd != 0 {
			return invalid()
		}
		if kind == "bookmark" {
			if len(anchor.PDFRects) != 0 {
				return invalid()
			}
			return nil
		}
		if len(anchor.PDFRects) == 0 || len(anchor.PDFRects) > 1000 {
			return invalid()
		}
		for _, r := range anchor.PDFRects {
			for _, v := range []float64{r.X, r.Y, r.Width, r.Height} {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					return invalid()
				}
			}
			if r.X < 0 || r.Y < 0 || r.Width <= 0 || r.Height <= 0 || r.X+r.Width > 1 || r.Y+r.Height > 1 {
				return invalid()
			}
		}
	case "txt":
		if anchor.PDFPage != 0 || len(anchor.PDFRects) != 0 || anchor.EPUBStartCFI != "" || anchor.EPUBEndCFI != "" {
			return invalid()
		}
		if kind == "bookmark" {
			if anchor.TXTOffsetEnd != 0 {
				return invalid()
			}
			return nil
		}
		if anchor.TXTOffsetStart >= anchor.TXTOffsetEnd {
			return invalid()
		}
	default:
		return invalid()
	}
	return nil
}

func bookMarkAnchorsOverlap(a, b BookReadingMarkAnchor, format string) bool {
	switch format {
	case "txt":
		return a.TXTOffsetStart < b.TXTOffsetEnd && b.TXTOffsetStart < a.TXTOffsetEnd
	case "epub":
		return compareBookPointCFI(a.EPUBStartCFI, b.EPUBEndCFI) < 0 && compareBookPointCFI(b.EPUBStartCFI, a.EPUBEndCFI) < 0
	default:
		if a.PDFPage != b.PDFPage {
			return false
		}
		for _, x := range a.PDFRects {
			for _, y := range b.PDFRects {
				if x.X < y.X+y.Width && y.X < x.X+x.Width && x.Y < y.Y+y.Height && y.Y < x.Y+x.Height {
					return true
				}
			}
		}
		return false
	}
}

func unionBookMarkAnchors(a, b BookReadingMarkAnchor, format string) BookReadingMarkAnchor {
	switch format {
	case "txt":
		if b.TXTOffsetStart < a.TXTOffsetStart {
			a.TXTOffsetStart = b.TXTOffsetStart
		}
		if b.TXTOffsetEnd > a.TXTOffsetEnd {
			a.TXTOffsetEnd = b.TXTOffsetEnd
		}
	case "epub":
		if compareBookPointCFI(b.EPUBStartCFI, a.EPUBStartCFI) < 0 {
			a.EPUBStartCFI = b.EPUBStartCFI
		}
		if compareBookPointCFI(b.EPUBEndCFI, a.EPUBEndCFI) > 0 {
			a.EPUBEndCFI = b.EPUBEndCFI
		}
	default:
		// Keep the exact rectangles; a bounding box would paint unselected text.
		for _, rect := range b.PDFRects {
			found := false
			for _, existing := range a.PDFRects {
				if rect == existing {
					found = true
					break
				}
			}
			if !found {
				a.PDFRects = append(a.PDFRects, rect)
			}
		}
	}
	return a
}
