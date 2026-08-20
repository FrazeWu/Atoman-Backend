package service

import (
	"strings"
	"testing"
)

func TestSanitizeFeedContentPreservesStructureCodeAndLazyImages(t *testing.T) {
	rawHTML := `<article>
		<h2>Reader heading</h2>
		<p>This feed article contains a complete opening paragraph with enough detail to describe the subject, establish context, and preserve publisher-provided structure for the in-app reader.</p>
		<p>A second paragraph adds analysis, references, and supporting explanation. It also includes enough complete sentences. The candidate should pass the quality threshold.</p>
		<pre><code>func main() {
	println("reader")
}</code></pre>
		<img data-src="/images/body.jpg" alt="Body illustration" width="1200" height="800" onerror="alert(1)">
		<script>alert(1)</script>
	</article>`

	candidate, err := SanitizeFeedContent("https://example.com/posts/reader", rawHTML)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<h2>Reader heading</h2>",
		"func main() {\n\tprintln(&#34;reader&#34;)\n}",
		`src="https://example.com/images/body.jpg"`,
		`loading="lazy"`,
		`decoding="async"`,
	} {
		if !strings.Contains(candidate.HTML, want) {
			t.Fatalf("expected %q in sanitized feed content: %s", want, candidate.HTML)
		}
	}
	for _, removed := range []string{"<script", "onerror", "data-src"} {
		if strings.Contains(candidate.HTML, removed) {
			t.Fatalf("expected %q removed from sanitized feed content: %s", removed, candidate.HTML)
		}
	}
	if candidate.Source != ReaderSourceFeed {
		t.Fatalf("source=%q", candidate.Source)
	}
	if candidate.QualityScore < ReaderQualityReadyThreshold {
		t.Fatalf("quality_score=%d flags=%v", candidate.QualityScore, candidate.QualityFlags)
	}
}

func TestChooseReaderCandidateRequiresMeaningfulPageImprovement(t *testing.T) {
	feed := ReaderCandidate{HTML: "<p>feed</p>", Source: ReaderSourceFeed, QualityScore: 72}
	nearbyPage := ReaderCandidate{HTML: "<p>page</p>", Source: ReaderSourcePage, QualityScore: 76}
	betterPage := ReaderCandidate{HTML: "<p>better</p>", Source: ReaderSourcePage, QualityScore: 82}

	if got := ChooseReaderCandidate(feed, nearbyPage); got.Source != ReaderSourceFeed {
		t.Fatalf("nearby score selected %q, want feed", got.Source)
	}
	if got := ChooseReaderCandidate(feed, betterPage); got.Source != ReaderSourcePage {
		t.Fatalf("better score selected %q, want page", got.Source)
	}
}

func TestExtractAndSanitizeFullTextUsesBestPageCandidate(t *testing.T) {
	rawHTML := `<html><body>
		<nav>` + strings.Repeat(`<a href="/tag">navigation link</a>`, 50) + `</nav>
		<article>
			<h1>Readable story</h1>
			<p>` + strings.Repeat("The article body explains its subject with complete sentences and useful detail. ", 12) + `</p>
			<p>` + strings.Repeat("Additional reporting provides context, evidence, and a clear conclusion for readers. ", 10) + `</p>
		</article>
	</body></html>`

	result, errorCode, err := ExtractAndSanitizeFullText("https://example.com/story", strings.NewReader(rawHTML))
	if err != nil {
		t.Fatalf("error_code=%s err=%v", errorCode, err)
	}
	if strings.Contains(result.HTML, "navigation link") {
		t.Fatalf("navigation leaked into reader result: %s", result.HTML)
	}
	if !strings.Contains(result.HTML, "Readable story") {
		t.Fatalf("article body missing from reader result: %s", result.HTML)
	}
	if result.QualityScore < ReaderQualityReadyThreshold {
		t.Fatalf("quality_score=%d flags=%v extractor=%s", result.QualityScore, result.QualityFlags, result.Extractor)
	}
}
