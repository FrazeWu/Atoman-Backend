package service

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestExtAtomEntryPreservesXHTMLContent(t *testing.T) {
	rawFeed := `<feed xmlns="http://www.w3.org/2005/Atom">
		<title>Example</title>
		<entry>
			<title>XHTML entry</title>
			<id>entry-xhtml</id>
			<link rel="alternate" href="https://example.com/xhtml" />
			<updated>2026-08-20T00:00:00Z</updated>
			<content type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml"><h2>Section</h2><p>First paragraph.</p><p>Second paragraph.</p></div></content>
		</entry>
	</feed>`

	var parsed ExtAtom
	if err := xml.Unmarshal([]byte(rawFeed), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Entries) != 1 {
		t.Fatalf("entries=%d", len(parsed.Entries))
	}
	content := parsed.Entries[0].Content
	if !strings.Contains(content, "<h2>Section</h2>") || !strings.Contains(content, "<p>First paragraph.</p>") {
		t.Fatalf("xhtml content was flattened: %q", content)
	}

	normalized := normalizeAtomEntry(parsed.Entries[0], parsed.Title, "", time.Now().UTC())
	if !strings.Contains(normalized.ContentHTML, "<p>Second paragraph.</p>") {
		t.Fatalf("normalized content=%q", normalized.ContentHTML)
	}
}
