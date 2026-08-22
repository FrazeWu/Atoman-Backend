package feedlanguage

import "testing"

func TestNormalizeCode(t *testing.T) {
	cases := map[string]string{
		"en-US": "en",
		"zh_CN": "zh",
		"ko":    "ko",
		"und":   "",
	}
	for input, expected := range cases {
		if got := NormalizeCode(input); got != expected {
			t.Fatalf("NormalizeCode(%q)=%q, want %q", input, got, expected)
		}
	}
}

func TestDetectCommonLanguages(t *testing.T) {
	cases := map[string]string{
		"This is an English article with enough words to detect the language.": "en",
		"这是一个包含足够文字的中文文章，用于测试语言识别。":                                            "zh",
		"이것은 언어 감지를 위한 충분한 한국어 문장입니다.":                                         "ko",
		"これは言語検出を確認するための十分な日本語の記事です。":                                          "ja",
	}
	for input, expected := range cases {
		if got := Detect(input); got != expected {
			t.Fatalf("Detect(%q)=%q, want %q", input, got, expected)
		}
	}
}

func TestDetectReturnsUnknownForShortText(t *testing.T) {
	if got := Detect("API 42"); got != "" {
		t.Fatalf("Detect(short text)=%q, want empty", got)
	}
}
