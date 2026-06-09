package service

import (
	"strings"
	"testing"
)

func TestExtractAndSanitizeFullText_PrefersArticle(t *testing.T) {
	html := `
	<html><body>
	<nav>menu</nav>
	<article>
	  <h1>Hello</h1>
	  <p>This is a long paragraph with enough content to survive extraction and sanitization for the full text reader, including descriptive wording about the article body, editorial structure, and narrative flow that pushes the extracted content beyond the minimum threshold.</p>
	  <p>Another paragraph with <a href="https://example.com">a link</a> and enough additional copy to clearly exceed the minimum extraction threshold for the worker, preserving the safe anchor while stripping unsafe attributes from the surrounding markup during sanitization.</p>
	  <img src="https://cdn.example.com/img.jpg" onerror="alert(1)" />
	</article>
	<script>alert(1)</script>
	</body></html>`

	result, errCode, err := ExtractAndSanitizeFullText("https://example.com/post", strings.NewReader(html))
	if err != nil {
		t.Fatalf("unexpected errCode=%s err=%v", errCode, err)
	}
	if !strings.Contains(result.HTML, "<h1>Hello</h1>") {
		t.Fatal("expected heading kept")
	}
	if strings.Contains(result.HTML, "<script") || strings.Contains(result.HTML, "onerror") {
		t.Fatal("expected unsafe nodes removed")
	}
	if !strings.Contains(result.HTML, `src="https://cdn.example.com/img.jpg"`) {
		t.Fatal("expected external image src kept")
	}
	if result.WordCount < 300 {
		t.Fatalf("expected enough extracted text, got %d", result.WordCount)
	}
}

func TestExtractAndSanitizeFullText_FallsBackToCommonContentSelector(t *testing.T) {
	html := `
	<html><body>
	<div class="sidebar">Links links links</div>
	<div class="post-content">
	  <p>This fallback container should be used when there is no semantic article element but there is still substantial content for extraction, including a long descriptive passage about the preserved reading flow, the editorial structure of the page, and the importance of extracting meaningful body text instead of nearby utility chrome.</p>
	  <p>It also keeps <a href="/relative">relative links</a> while stripping dangerous attributes from nested elements during sanitization, and it adds another dense paragraph describing lists, notes, citations, and supporting explanations so the extracted body clearly exceeds the minimum length threshold required by the worker.</p>
	</div>
	</body></html>`

	result, errCode, err := ExtractAndSanitizeFullText("https://example.com/post", strings.NewReader(html))
	if err != nil {
		t.Fatalf("unexpected errCode=%s err=%v", errCode, err)
	}
	if !strings.Contains(result.HTML, "fallback container should be used") {
		t.Fatal("expected post-content text kept")
	}
	if !strings.Contains(result.HTML, `href="/relative"`) {
		t.Fatal("expected safe relative link kept")
	}
}

func TestExtractAndSanitizeFullText_SanitizesDisallowedMarkupAndKeepsAllowedStructure(t *testing.T) {
	html := `
	<html><body>
	<article>
	  <h1>Sanitized article</h1>
	  <p style="color:red">This article body is intentionally long enough to survive the extraction threshold while demonstrating that inline styles are removed, that dangerous descendants are stripped, and that safe semantic structure remains readable for the downstream full text reader without introducing any extra wrapper logic or unrelated cleanup.</p>
	  <blockquote>A quoted passage remains available to readers and keeps the editorial flow intact for long-form reading experiences inside the application.</blockquote>
	  <ul>
	    <li>First retained list item with enough descriptive words to contribute meaningful content to the extracted full text body.</li>
	    <li>Second retained list item describing supporting context, citations, and notes that belong in the sanitized reader output.</li>
	  </ul>
	  <p><a href="https://example.com/reference">Reference link</a> and <a href="javascript:alert(1)">bad link</a> sit beside an <img src="/image.jpg" alt="Article image" title="cover" width="640" height="480" onerror="alert(1)" /> that should remain after sanitization.</p>
	  <style>.hidden { display:none; }</style>
	  <iframe src="https://evil.example/embed"></iframe>
	  <form action="/submit"><input name="email" /></form>
	</article>
	</body></html>`

	result, errCode, err := ExtractAndSanitizeFullText("https://example.com/post", strings.NewReader(html))
	if err != nil {
		t.Fatalf("unexpected errCode=%s err=%v", errCode, err)
	}
	for _, disallowed := range []string{"<style", "<iframe", "<form", "style=", "javascript:"} {
		if strings.Contains(result.HTML, disallowed) {
			t.Fatalf("expected %q removed from sanitized html: %s", disallowed, result.HTML)
		}
	}
	for _, allowed := range []string{"<blockquote>", "<ul>", "<li>", `href="https://example.com/reference"`, `rel="noopener noreferrer"`, `src="https://example.com/image.jpg"`, `alt="Article image"`, `title="cover"`, `width="640"`, `height="480"`} {
		if !strings.Contains(result.HTML, allowed) {
			t.Fatalf("expected %q kept in sanitized html: %s", allowed, result.HTML)
		}
	}
	if strings.Contains(result.HTML, `target="_blank"`) {
		t.Fatalf("expected sanitized external links to omit target attribute: %s", result.HTML)
	}
	if !strings.Contains(result.HTML, `<a>bad link</a>`) {
		t.Fatalf("expected dangerous link href to be stripped while keeping link text: %s", result.HTML)
	}
}

func TestExtractAndSanitizeFullText_RemovesDecorativeIconImages(t *testing.T) {
	html := `
	<html><body>
	<article>
	  <p>This article body starts with enough descriptive text to clear the extraction threshold while keeping the first paragraph meaningful and representative of the legitimate reading experience that should survive the full text pipeline.</p>
	  <p><img src="https://cdn.example.com/avatar/pi.png" alt="少数派编辑部头像" width="48" height="48" />This paragraph continues with enough surrounding prose to keep the article valid after decorative author icons are removed from the sanitized result and should read naturally as a normal sentence in the final reader.</p>
	  <p><img src="https://cdn.example.com/content/hero.jpg" alt="正文配图" width="1200" height="800" />A second dense paragraph discusses the product details, editorial context, and review framing at length so the large in-article illustration remains while the tiny decorative icon is stripped from the stored HTML.</p>
	</article>
	</body></html>`

	result, errCode, err := ExtractAndSanitizeFullText("https://example.com/post", strings.NewReader(html))
	if err != nil {
		t.Fatalf("unexpected errCode=%s err=%v", errCode, err)
	}
	if strings.Contains(result.HTML, "avatar/pi.png") {
		t.Fatalf("expected decorative avatar icon removed from sanitized html: %s", result.HTML)
	}
	if !strings.Contains(result.HTML, "content/hero.jpg") {
		t.Fatalf("expected legitimate content image kept in sanitized html: %s", result.HTML)
	}
}

func TestExtractAndSanitizeFullText_RemovesHiddenAndDecorativeInlineChrome(t *testing.T) {
	html := `
	<html><body>
	<article>
	  <p>This article opens with a substantial paragraph about the main topic, editorial context, and reader-facing explanation so the full text extractor has enough legitimate prose to preserve while removing decorative inline chrome from nearby elements.</p>
	  <p><span class="iconfont">broken share icon</span><span aria-hidden="true">hidden glyph label</span><span hidden>hidden utility copy</span>The visible sentence should continue normally after decorative controls are removed from the sanitized full text output.</p>
	  <p><span class="sr-only">screen reader helper copy</span><span class="visually-hidden">visual hiding helper copy</span>Another visible paragraph discusses analysis, background, and practical reading context at enough length to stay above the extraction threshold without relying on any of the removed helper nodes.</p>
	  <p><span class="term">inline glossary term</span> remains visible because ordinary inline text wrappers can still carry legitimate article prose.</p>
	</article>
	</body></html>`

	result, errCode, err := ExtractAndSanitizeFullText("https://example.com/post", strings.NewReader(html))
	if err != nil {
		t.Fatalf("unexpected errCode=%s err=%v", errCode, err)
	}
	for _, removed := range []string{"broken share icon", "hidden glyph label", "hidden utility copy", "screen reader helper copy", "visual hiding helper copy"} {
		if strings.Contains(result.HTML, removed) {
			t.Fatalf("expected decorative or hidden text %q removed from sanitized html: %s", removed, result.HTML)
		}
	}
	for _, kept := range []string{"The visible sentence should continue normally", "Another visible paragraph discusses analysis", "inline glossary term"} {
		if !strings.Contains(result.HTML, kept) {
			t.Fatalf("expected legitimate text %q kept in sanitized html: %s", kept, result.HTML)
		}
	}
}

func TestExtractAndSanitizeFullText_RemovesObviousNonContentSections(t *testing.T) {
	html := `
	<html><body>
	<article>
	  <h1>Reader mode article</h1>
	  <p>This article contains a substantial opening passage describing the main thesis, background context, editorial framing, and supporting evidence so the extracted body comfortably exceeds the minimum full text threshold even after obvious non-content sections are removed from the selected subtree.</p>
	  <div class="share-buttons social-tools">
	    <p>Share this article on every network right now.</p>
	  </div>
	  <p>A second dense paragraph continues the legitimate article body with additional analysis, examples, citations, and scene-setting language so the resulting sanitized HTML still represents a long-form reading experience after comments, ads, recommendations, and sharing widgets are stripped out in full.</p>
	  <section id="comments">
	    <p>First comment saying the author is wrong.</p>
	  </section>
	  <div class="ad sponsored">
	    <p>Buy miracle products from this advertisement.</p>
	  </div>
	  <div id="related-stories" class="recommendations">
	    <p>Read these related stories next.</p>
	  </div>
	</article>
	</body></html>`

	result, errCode, err := ExtractAndSanitizeFullText("https://example.com/post", strings.NewReader(html))
	if err != nil {
		t.Fatalf("unexpected errCode=%s err=%v", errCode, err)
	}
	for _, kept := range []string{"Reader mode article", "substantial opening passage", "second dense paragraph continues the legitimate article body"} {
		if !strings.Contains(result.HTML, kept) {
			t.Fatalf("expected %q kept in sanitized html: %s", kept, result.HTML)
		}
	}
	for _, removed := range []string{"Share this article on every network right now.", "First comment saying the author is wrong.", "Buy miracle products from this advertisement.", "Read these related stories next."} {
		if strings.Contains(result.HTML, removed) {
			t.Fatalf("expected obvious non-content text %q removed from sanitized html: %s", removed, result.HTML)
		}
	}
}

func TestExtractAndSanitizeFullText_PreservesWhitespaceAroundInlineElements(t *testing.T) {
	html := `
	<html><body>
		<article>
		  <p>Hello <strong>world</strong> again with enough surrounding narrative to keep the first paragraph comfortably inside the extraction threshold for the reader mode sanitizer without relying on any unrelated wrappers or shortcuts.</p>
		  <p>Please see <a href="https://example.com/link">link</a> here while the paragraph continues with additional editorial phrasing, source context, and descriptive wording so the extracted text remains long-form and clearly above the minimum character count.</p>
		  <p>Use <code>fmt.Println</code> carefully in prose that keeps discussing implementation notes, maintenance concerns, and surrounding article commentary long enough for this regression test to exercise sanitized inline spacing without becoming too short.</p>
		</article>
		</body></html>`

	result, errCode, err := ExtractAndSanitizeFullText("https://example.com/post", strings.NewReader(html))
	if err != nil {
		t.Fatalf("unexpected errCode=%s err=%v", errCode, err)
	}
	for _, want := range []string{"Hello <strong>world</strong> again", "see <a href=\"https://example.com/link\" rel=\"noopener noreferrer\">link</a> here", "Use <code>fmt.Println</code> carefully"} {
		if !strings.Contains(result.HTML, want) {
			t.Fatalf("expected inline spacing preserved around %q in sanitized html: %s", want, result.HTML)
		}
	}
}

func TestExtractAndSanitizeFullText_KeepsLegitimateContentWithBroadClassOrIDNames(t *testing.T) {
	html := `
	<html><body>
		<article>
		  <div class="commentary">
		    <p>This commentary section is the actual story body, presenting a substantial opening discussion about the subject matter, historical framing, and narrative stakes so the extractor must keep it instead of mistaking the token for a comment widget.</p>
		  </div>
		  <section id="shareholder-letter">
		    <p>The shareholder letter continues with detailed explanation, financial context, governance analysis, and editorial interpretation that clearly belongs to the legitimate article body rather than a social sharing surface.</p>
		  </section>
		  <div class="related-work">
		    <p>The related work passage discusses prior research, cites neighboring studies, and extends the main thesis with enough detail to remain valid reading content rather than a recommendation rail that should be stripped.</p>
		  </div>
		</article>
		</body></html>`

	result, errCode, err := ExtractAndSanitizeFullText("https://example.com/post", strings.NewReader(html))
	if err != nil {
		t.Fatalf("unexpected errCode=%s err=%v", errCode, err)
	}
	for _, want := range []string{"This commentary section is the actual story body", "The shareholder letter continues with detailed explanation", "The related work passage discusses prior research"} {
		if !strings.Contains(result.HTML, want) {
			t.Fatalf("expected legitimate content %q kept in sanitized html: %s", want, result.HTML)
		}
	}
}

func TestExtractAndSanitizeFullText_TooShortFails(t *testing.T) {
	html := `<html><body><article><p>short</p></article></body></html>`
	_, errCode, err := ExtractAndSanitizeFullText("https://example.com/post", strings.NewReader(html))
	if err == nil {
		t.Fatal("expected error")
	}
	if errCode != FullTextErrorExtractTooShort {
		t.Fatalf("got %s", errCode)
	}
}

func TestExtractAndSanitizeFullText_SanitizeEmptyFails(t *testing.T) {
	html := `<html><body><article><script>alert(1)</script></article></body></html>`
	_, errCode, err := ExtractAndSanitizeFullText("https://example.com/post", strings.NewReader(html))
	if err == nil {
		t.Fatal("expected error")
	}
	if errCode != FullTextErrorSanitizeEmpty {
		t.Fatalf("got %s", errCode)
	}
}
