package feedlanguage

import (
	"strings"
	"unicode"
)

// NormalizeCode reduces BCP-47-like values to the language base used by feed filters.
func NormalizeCode(raw string) string {
	value := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(raw, "_", "-")))
	if value == "" || value == "und" || value == "unknown" || value == "mixed" {
		return ""
	}
	if index := strings.IndexByte(value, '-'); index >= 0 {
		value = value[:index]
	}
	if len(value) < 2 || len(value) > 3 {
		return ""
	}
	for _, r := range value {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return value
}

// Detect identifies common scripts and uses stop words for Latin-script languages.
// Short or ambiguous text intentionally returns an empty value.
func Detect(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}

	counts := map[string]int{}
	latinLetters := 0
	for _, r := range text {
		switch {
		case unicode.In(r, unicode.Hangul):
			counts["ko"]++
		case unicode.In(r, unicode.Hiragana, unicode.Katakana):
			counts["ja"]++
		case unicode.In(r, unicode.Han):
			counts["zh"]++
		case unicode.In(r, unicode.Cyrillic):
			counts["ru"]++
		case unicode.In(r, unicode.Arabic):
			counts["ar"]++
		case unicode.In(r, unicode.Hebrew):
			counts["he"]++
		case unicode.In(r, unicode.Devanagari):
			counts["hi"]++
		case unicode.IsLetter(r) && unicode.In(r, unicode.Latin):
			latinLetters++
		}
	}

	for _, language := range []string{"ko", "ja", "zh", "ru", "ar", "he", "hi"} {
		if counts[language] >= 2 {
			return language
		}
	}
	if latinLetters < 12 {
		return ""
	}

	words := tokenize(text)
	scores := map[string]int{}
	for _, word := range words {
		for language, stopWords := range latinStopWords {
			if stopWords[word] {
				scores[language]++
			}
		}
	}
	bestLanguage, bestScore := "", 0
	for _, language := range []string{"en", "de", "es", "fr", "it", "pt", "nl"} {
		if scores[language] > bestScore {
			bestLanguage = language
			bestScore = scores[language]
		}
	}
	if bestScore >= 2 {
		return bestLanguage
	}
	return "en"
}

func tokenize(text string) []string {
	words := make([]string, 0, 24)
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		words = append(words, current.String())
		current.Reset()
	}
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return words
}

var latinStopWords = map[string]map[string]bool{
	"en": {
		"the": true, "and": true, "of": true, "to": true, "in": true, "is": true, "for": true, "with": true, "that": true, "this": true,
	},
	"de": {
		"der": true, "die": true, "das": true, "und": true, "ist": true, "für": true, "mit": true, "nicht": true, "ein": true, "eine": true,
	},
	"es": {
		"el": true, "la": true, "los": true, "las": true, "de": true, "que": true, "en": true, "y": true, "para": true, "una": true,
	},
	"fr": {
		"le": true, "la": true, "les": true, "des": true, "de": true, "et": true, "est": true, "pour": true, "dans": true, "une": true,
	},
	"it": {
		"il": true, "lo": true, "la": true, "gli": true, "di": true, "che": true, "e": true, "per": true, "una": true, "con": true,
	},
	"pt": {
		"o": true, "a": true, "os": true, "as": true, "de": true, "que": true, "em": true, "e": true, "para": true, "uma": true,
	},
	"nl": {
		"de": true, "het": true, "een": true, "van": true, "en": true, "in": true, "voor": true, "met": true, "zijn": true, "dat": true,
	},
}
