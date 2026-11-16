package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	clientErrorMaxRelease = 64
	clientErrorMaxRoute   = 256
	clientErrorMaxName    = 128
	clientErrorMaxMessage = 500
	clientErrorMaxStack   = 2000
	clientErrorMaxContext = 200
	clientErrorMaxAgent   = 256
	clientErrorMaxLines   = 12
)

var (
	clientErrorReleasePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,63}$`)
	clientErrorEmailPattern    = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	clientErrorBearerPattern   = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/\-]+=*`)
	clientErrorJWTPattern      = regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`)
	clientErrorFragmentPattern = regexp.MustCompile(`(?i)#(?:access_token|id_token|refresh_token|token|code)=[^\s]*`)
	clientErrorQueryPattern    = regexp.MustCompile(`\?[^\s#]*`)
	clientErrorSecretPattern   = regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token|token|auth|api_key|apikey|secret|password|passwd|email|signature|sig|jwt)=([^\s&]+)`)
)

func redactClientErrorText(value string) string {
	value = clientErrorEmailPattern.ReplaceAllString(value, "[redacted]")
	value = clientErrorBearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = clientErrorJWTPattern.ReplaceAllString(value, "[redacted]")
	value = clientErrorFragmentPattern.ReplaceAllString(value, "")
	value = clientErrorQueryPattern.ReplaceAllString(value, "")
	value = clientErrorSecretPattern.ReplaceAllString(value, "$1=[redacted]")
	return strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
}

func truncateClientErrorBytes(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	cut := value[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func limitClientErrorLines(value string, lines int) string {
	if lines <= 0 || value == "" {
		return ""
	}
	parts := strings.Split(value, "\n")
	if len(parts) <= lines {
		return value
	}
	return strings.Join(parts[:lines], "\n")
}

func sanitizeClientErrorRelease(value string) (string, bool) {
	value = truncateClientErrorBytes(strings.TrimSpace(value), clientErrorMaxRelease)
	if !clientErrorReleasePattern.MatchString(value) {
		return "", false
	}
	return value, true
}

func sanitizeClientErrorRoute(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Path == "" {
			return "", false
		}
		raw = parsed.Path
	}
	if index := strings.IndexAny(raw, "?#"); index >= 0 {
		raw = raw[:index]
	}
	raw = redactClientErrorText(raw)
	if !strings.HasPrefix(raw, "/") || strings.Contains(raw, "[redacted]") {
		return "", false
	}
	if strings.ContainsAny(raw, " @\\\t\r\n") {
		return "", false
	}
	raw = truncateClientErrorBytes(raw, clientErrorMaxRoute)
	if raw == "" || !strings.HasPrefix(raw, "/") {
		return "", false
	}
	return raw, true
}

func sanitizeClientErrorField(value string, max int) string {
	return truncateClientErrorBytes(strings.TrimSpace(redactClientErrorText(value)), max)
}

func clientErrorFingerprint(release, route, kind, name, message string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{release, route, kind, name, message}, "\n")))
	return hex.EncodeToString(sum[:])
}
