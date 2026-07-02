package feedclass

import "testing"

func TestClassifyDetectsForumFromSourceURL(t *testing.T) {
	category := Classify(Source{
		Title:  "V2EX",
		RSSURL: "https://v2ex.com/index.xml",
		RecentItems: []RecentItem{
			{Title: "Item", Link: "https://example.com/post"},
		},
	})

	if category != "forum" {
		t.Fatalf("expected forum, got %q", category)
	}
}

func TestClassifyDoesNotTreat500pxAsSocial(t *testing.T) {
	category := Classify(Source{
		Title:  "500px:",
		RSSURL: "http://feed.500px.com/500px-editors",
		RecentItems: []RecentItem{
			{Title: "Photo", Link: "https://500px.com/photo/1049231221/example"},
		},
	})

	if category != "blog" {
		t.Fatalf("expected blog, got %q", category)
	}
}

func TestClassifyDoesNotTreatITHomeAsVideoOnlyBecauseTitleContainsIT(t *testing.T) {
	category := Classify(Source{
		Title:  "IT之家",
		RSSURL: "http://www.ithome.com/rss/",
		RecentItems: []RecentItem{
			{Title: "新闻条目", Link: "https://www.ithome.com/0/970/858.htm"},
		},
	})

	if category != "blog" && category != "news" {
		t.Fatalf("expected blog or news, got %q", category)
	}
}

func TestClassifyDoesNotTreatTwitterStatusWithWordVideoAsVideo(t *testing.T) {
	category := Classify(Source{
		Title:  "LangChain(@LangChainAI)",
		RSSURL: "https://api.xgo.ing/rss/user/862fee50a745423c87e2633b274caf1d",
		RecentItems: []RecentItem{
			{Title: "Video generation update", Link: "https://x.com/LangChain/status/2072031919370355112"},
		},
	})

	if category != "social" {
		t.Fatalf("expected social, got %q", category)
	}
}

func TestClassifyDoesNotTreatSingleVideoAttachmentAsVideoSource(t *testing.T) {
	category := Classify(Source{
		Title:  "IT之家",
		RSSURL: "http://www.ithome.com/rss/",
		RecentItems: []RecentItem{
			{Title: "新闻条目 1", Link: "https://www.ithome.com/0/970/858.htm", EnclosureType: "video/mp4"},
			{Title: "新闻条目 2", Link: "https://www.ithome.com/0/970/859.htm"},
			{Title: "新闻条目 3", Link: "https://www.ithome.com/0/970/860.htm"},
		},
	})

	if category == "video" {
		t.Fatalf("expected non-video source, got %q", category)
	}
}

func TestClassifyDoesNotTreatSingleAudioAttachmentAsPodcastSource(t *testing.T) {
	category := Classify(Source{
		Title:  "Google DeepMind(@GoogleDeepMind)",
		RSSURL: "https://api.xgo.ing/rss/user/a99538443a484fcc846bdcc8f50745ec",
		RecentItems: []RecentItem{
			{Title: "Status 1", Link: "https://x.com/GoogleDeepMind/status/1", EnclosureType: "audio/mpeg"},
			{Title: "Status 2", Link: "https://x.com/GoogleDeepMind/status/2"},
			{Title: "Status 3", Link: "https://x.com/GoogleDeepMind/status/3"},
		},
	})

	if category != "social" {
		t.Fatalf("expected social, got %q", category)
	}
}

func TestClassifyDoesNotTreatSingleYoutubeLinkAsVideoSource(t *testing.T) {
	category := Classify(Source{
		Title:  "卢昌海个人主页",
		RSSURL: "http://www.changhai.org/feed.xml",
		RecentItems: []RecentItem{
			{Title: "Video link", Link: "https://www.youtube.com/watch?v=fG0rf9oRM1o"},
			{Title: "Travel note", Link: "https://www.changhai.org/articles/tours/2024_Copenhagen/malmo.php"},
			{Title: "Essay", Link: "https://www.changhai.org/articles/miscellaneous/misc/2025_Spain.php"},
		},
	})

	if category == "video" {
		t.Fatalf("expected non-video source, got %q", category)
	}
}

func TestClassifyKeepsXStatusSourcesAsSocial(t *testing.T) {
	category := Classify(Source{
		Title:  "Lex Fridman(@lexfridman)",
		RSSURL: "https://api.xgo.ing/rss/user/adf65931519340f795e2336910b4cd15",
		RecentItems: []RecentItem{
			{Title: "Status 1", Link: "https://x.com/lexfridman/status/2072080363107533010"},
			{Title: "Status 2", Link: "https://x.com/lexfridman/status/2072080358397423808"},
			{Title: "Status 3", Link: "https://x.com/lexfridman/status/2065818277448732908"},
		},
	})

	if category != "social" {
		t.Fatalf("expected social, got %q", category)
	}
}

func TestClassifyDoesNotTreatAnthropicBlogAsNews(t *testing.T) {
	category := Classify(Source{
		Title:  "Anthropic Blog",
		RSSURL: "https://www.anthropic.com/news/rss.xml",
		RecentItems: []RecentItem{
			{Title: "Claude Code update", Link: "https://www.anthropic.com/engineering/claude-code"},
			{Title: "Research note", Link: "https://www.anthropic.com/research/alignment"},
			{Title: "Product post", Link: "https://www.anthropic.com/product/team-plan"},
		},
	})

	if category != "blog" {
		t.Fatalf("expected blog, got %q", category)
	}
}

func TestClassifyDoesNotTreatElasticBlogAsNews(t *testing.T) {
	category := Classify(Source{
		Title:  "Elastic Blog",
		RSSURL: "https://www.elastic.co/blog/feed",
		RecentItems: []RecentItem{
			{Title: "Elastic Stack 9", Link: "https://www.elastic.co/blog/elastic-stack-9"},
			{Title: "Search Labs", Link: "https://www.elastic.co/search-labs/blog/vector-search"},
			{Title: "Security release", Link: "https://www.elastic.co/blog/security-release"},
		},
	})

	if category != "blog" {
		t.Fatalf("expected blog, got %q", category)
	}
}
