package books

import (
	"atoman/internal/platform/apperr"
	"math"
	"strings"
	"unicode/utf8"
)

// BookChapterAnchor stores a location in an immutable source revision.
type BookChapterAnchor struct {
	Format       string  `json:"format"`
	EPUBCFI      string  `json:"epub_cfi,omitempty"`
	EPUBHref     string  `json:"epub_href,omitempty"`
	PDFPage      int     `json:"pdf_page,omitempty"`
	PDFOffset    float64 `json:"pdf_offset,omitempty"`
	TXTOffset    int64   `json:"txt_offset,omitempty"`
	ChapterTitle string  `json:"chapter_title,omitempty"`
}

func normalizeBookChapterAnchor(v BookChapterAnchor, format string) (BookChapterAnchor, error) {
	v.Format = strings.ToLower(strings.TrimSpace(v.Format))
	v.EPUBCFI = strings.TrimSpace(v.EPUBCFI)
	v.EPUBHref = strings.TrimSpace(v.EPUBHref)
	v.ChapterTitle = strings.TrimSpace(v.ChapterTitle)
	reject := func() (BookChapterAnchor, error) {
		return BookChapterAnchor{}, apperr.BadRequest("books.invalid_chapter_anchor", "Invalid source chapter position")
	}
	if v.Format != format || !utf8.ValidString(v.ChapterTitle) || len(v.ChapterTitle) > 255 || len(v.EPUBCFI) > 4096 || len(v.EPUBHref) > 1024 {
		return reject()
	}
	if math.IsNaN(v.PDFOffset) || math.IsInf(v.PDFOffset, 0) || v.PDFOffset < 0 || v.PDFOffset > 1 {
		return reject()
	}
	switch format {
	case "epub":
		if v.EPUBCFI == "" || !strings.HasPrefix(v.EPUBCFI, "epubcfi(") || v.PDFPage != 0 || v.PDFOffset != 0 || v.TXTOffset != 0 {
			return reject()
		}
	case "pdf":
		if v.PDFPage < 1 || v.PDFPage > 1000000 || v.EPUBCFI != "" || v.EPUBHref != "" || v.TXTOffset != 0 {
			return reject()
		}
	case "txt":
		if v.TXTOffset < 0 || v.EPUBCFI != "" || v.EPUBHref != "" || v.PDFPage != 0 || v.PDFOffset != 0 {
			return reject()
		}
	case "cbz", "cbr":
		if v.PDFPage < 1 || v.PDFPage > 1000000 || v.EPUBCFI != "" || v.EPUBHref != "" || v.PDFOffset != 0 || v.TXTOffset != 0 {
			return reject()
		}
	default:
		return reject()
	}
	return v, nil
}
