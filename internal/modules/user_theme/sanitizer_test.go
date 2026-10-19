package user_theme_test

import (
	"errors"
	"strings"
	"testing"

	"atoman/internal/modules/user_theme"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeAndScopeCSS_NormalScoping(t *testing.T) {
	creatorID := uuid.MustParse("0192e2b4-7b9c-7000-8000-000000000001")
	scopePrefix := `.creator-theme-scope[data-creator-id="0192e2b4-7b9c-7000-8000-000000000001"]`

	tests := []struct {
		name     string
		rawCSS   string
		contains []string
	}{
		{
			name:   "single class selector",
			rawCSS: ".card { color: red; background: blue; }",
			contains: []string{
				scopePrefix + " .card",
				"color: red;",
				"background: blue;",
			},
		},
		{
			name:   "multiple selectors",
			rawCSS: "h1, h2, .sub-heading { font-weight: bold; }",
			contains: []string{
				scopePrefix + " h1",
				scopePrefix + " h2",
				scopePrefix + " .sub-heading",
				"font-weight: bold;",
			},
		},
		{
			name:   "pseudo-class and combinator",
			rawCSS: "a:hover { text-decoration: underline; }\n.nav > li.active { color: #fff; }",
			contains: []string{
				scopePrefix + " a:hover",
				scopePrefix + " .nav > li.active",
				"text-decoration: underline;",
				"color: #fff;",
			},
		},
		{
			name:   "root and body selectors mapped to scope",
			rawCSS: ":root { --theme-color: #333; }\nbody { background: #fafafa; }\nbody .content { padding: 16px; }",
			contains: []string{
				scopePrefix + " {",
				"--theme-color: #333;",
				"background: #fafafa;",
				scopePrefix + " .content",
				"padding: 16px;",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := user_theme.SanitizeAndScopeCSS(tc.rawCSS, creatorID)
			require.NoError(t, err)
			for _, exp := range tc.contains {
				assert.Contains(t, out, exp)
			}
		})
	}
}

func TestSanitizeAndScopeCSS_AtRules(t *testing.T) {
	creatorID := uuid.MustParse("0192e2b4-7b9c-7000-8000-000000000002")
	scopePrefix := `.creator-theme-scope[data-creator-id="0192e2b4-7b9c-7000-8000-000000000002"]`

	t.Run("media query scopes inner rules", func(t *testing.T) {
		raw := `@media (max-width: 768px) {
			.sidebar { display: none; }
			.content { width: 100%; }
		}`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.Contains(t, out, "@media (max-width: 768px)")
		assert.Contains(t, out, scopePrefix+" .sidebar")
		assert.Contains(t, out, scopePrefix+" .content")
	})

	t.Run("supports query scopes inner rules", func(t *testing.T) {
		raw := `@supports (display: grid) {
			.grid-box { display: grid; }
		}`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.Contains(t, out, "@supports (display: grid)")
		assert.Contains(t, out, scopePrefix+" .grid-box")
	})

	t.Run("keyframes preserves keyframe selectors without scope prefix", func(t *testing.T) {
		raw := `@keyframes pulse {
			0% { opacity: 0; }
			50% { opacity: 0.5; }
			100% { opacity: 1; }
		}`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.Contains(t, out, "@keyframes pulse")
		assert.Contains(t, out, "0% {")
		assert.Contains(t, out, "50% {")
		assert.Contains(t, out, "100% {")
		assert.NotContains(t, out, scopePrefix+" 0%")
		assert.NotContains(t, out, scopePrefix+" 100%")
	})
}

func TestSanitizeAndScopeCSS_DangerousDirectivesStripped(t *testing.T) {
	creatorID := uuid.MustParse("0192e2b4-7b9c-7000-8000-000000000003")

	t.Run("strips import directives", func(t *testing.T) {
		raw := `@import url("https://evil.com/xss.css");
@import 'https://malicious.org/style.css' screen;
.safe { color: green; }`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.NotContains(t, out, "@import")
		assert.NotContains(t, out, "evil.com")
		assert.NotContains(t, out, "malicious.org")
		assert.Contains(t, out, ".safe")
		assert.Contains(t, out, "color: green;")
	})

	t.Run("strips expression properties", func(t *testing.T) {
		raw := `.box {
			width: expression(alert('xss'));
			height: 100px;
		}`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.NotContains(t, out, "expression")
		assert.NotContains(t, out, "alert")
		assert.Contains(t, out, "height: 100px;")
	})

	t.Run("strips javascript url values", func(t *testing.T) {
		raw := `.bg {
			background-image: url(javascript:alert(1));
			list-style: url("javascript:void(0)");
			color: red;
		}`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.NotContains(t, out, "javascript:")
		assert.Contains(t, out, "color: red;")
	})

	t.Run("strips behavior properties", func(t *testing.T) {
		raw := `.old-ie {
			behavior: url(pngfix.htc);
			-ms-behavior: url(pngfix.htc);
			margin: 10px;
		}`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.NotContains(t, out, "behavior")
		assert.NotContains(t, out, "pngfix.htc")
		assert.Contains(t, out, "margin: 10px;")
	})
}

func TestSanitizeAndScopeCSS_OversizedCSSRejected(t *testing.T) {
	creatorID := uuid.MustParse("0192e2b4-7b9c-7000-8000-000000000004")

	// Max size is 64KB = 64 * 1024 = 65536 bytes
	assert.Equal(t, 64*1024, user_theme.MaxCSSSizeBytes)

	// Exceed limit
	hugeCSS := strings.Repeat("a", user_theme.MaxCSSSizeBytes+1)
	_, err := user_theme.SanitizeAndScopeCSS(hugeCSS, creatorID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, user_theme.ErrCSSSizeExceeded))

	// Within limit
	validSmallCSS := ".card { color: red; }"
	out, err := user_theme.SanitizeAndScopeCSS(validSmallCSS, creatorID)
	require.NoError(t, err)
	assert.NotEmpty(t, out)
}

func TestSanitizeAndScopeCSS_NilCreatorIDAndEmpty(t *testing.T) {
	t.Run("nil creator id returns error", func(t *testing.T) {
		_, err := user_theme.SanitizeAndScopeCSS(".card { color: red; }", uuid.Nil)
		require.Error(t, err)
	})

	t.Run("empty or whitespace css returns empty string without error", func(t *testing.T) {
		creatorID := uuid.MustParse("0192e2b4-7b9c-7000-8000-000000000005")
		out, err := user_theme.SanitizeAndScopeCSS("", creatorID)
		require.NoError(t, err)
		assert.Empty(t, out)

		out, err = user_theme.SanitizeAndScopeCSS("   \n\t  ", creatorID)
		require.NoError(t, err)
		assert.Empty(t, out)
	})
}

func TestSanitizeAndScopeCSS_EdgeCases(t *testing.T) {
	creatorID := uuid.MustParse("0192e2b4-7b9c-7000-8000-000000000006")
	scopePrefix := `.creator-theme-scope[data-creator-id="0192e2b4-7b9c-7000-8000-000000000006"]`

	t.Run("obfuscated expression with comment", func(t *testing.T) {
		raw := `.box { width: exp/*foo*/ression(alert(1)); height: 20px; }`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.NotContains(t, out, "expression")
		assert.NotContains(t, out, "alert")
		assert.Contains(t, out, "height: 20px;")
	})

	t.Run("obfuscated javascript with newline", func(t *testing.T) {
		raw := `.bg { background: url("java\nscript:alert(1)"); color: white; }`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.NotContains(t, out, "javascript")
		assert.Contains(t, out, "color: white;")
	})

	t.Run("selector with comma inside attribute", func(t *testing.T) {
		raw := `a[data-attr="foo,bar"] { color: purple; }`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.Contains(t, out, scopePrefix+` a[data-attr="foo,bar"]`)
		assert.Contains(t, out, "color: purple;")
	})

	t.Run("declaration without trailing semicolon", func(t *testing.T) {
		raw := `.btn { color: red; background: yellow }`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.Contains(t, out, "color: red;")
		assert.Contains(t, out, "background: yellow;")
	})

	t.Run("malformed css with stray closing braces", func(t *testing.T) {
		raw := `} } .btn { color: red; } }`
		out, err := user_theme.SanitizeAndScopeCSS(raw, creatorID)
		require.NoError(t, err)
		assert.Contains(t, out, scopePrefix+` .btn`)
		assert.Contains(t, out, "color: red;")
	})
}

