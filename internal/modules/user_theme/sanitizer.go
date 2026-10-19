package user_theme

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// MaxCSSSizeBytes is the maximum allowed size for custom CSS (64KB).
const MaxCSSSizeBytes = 64 * 1024

var (
	// ErrCSSSizeExceeded is returned when the raw CSS exceeds MaxCSSSizeBytes.
	ErrCSSSizeExceeded = errors.New("raw CSS exceeds maximum allowed size of 64KB")
	// ErrNilCreatorID is returned when the creator ID is nil.
	ErrNilCreatorID = errors.New("creatorID cannot be nil")
)

var (
	reExpression = regexp.MustCompile(`(?i)\bexpression\s*\(`)
	reJS         = regexp.MustCompile(`(?i)javascript\s*:`)
	reVBS        = regexp.MustCompile(`(?i)vbscript\s*:`)
)

// SanitizeAndScopeCSS filters out dangerous CSS directives and prefixes all selectors
// with `.creator-theme-scope[data-creator-id="<UUID>"]`.
func SanitizeAndScopeCSS(rawCSS string, creatorID uuid.UUID) (string, error) {
	if len(rawCSS) > MaxCSSSizeBytes {
		return "", ErrCSSSizeExceeded
	}

	if creatorID == uuid.Nil {
		return "", ErrNilCreatorID
	}

	trimmed := strings.TrimSpace(rawCSS)
	if trimmed == "" {
		return "", nil
	}

	scopePrefix := fmt.Sprintf(`.creator-theme-scope[data-creator-id="%s"]`, creatorID.String())

	// Step 1: Strip comments
	noComments := stripCSSComments(trimmed)

	// Step 2: Strip @import directives
	noImports := stripImportDirectives(noComments)

	// Step 3: Parse and process rules
	rules := parseCSSRules(noImports)
	result := processCSSRules(rules, scopePrefix)

	return strings.TrimSpace(result), nil
}

// stripCSSComments removes /* ... */ comments from CSS, preserving quotes.
func stripCSSComments(input string) string {
	var out strings.Builder
	out.Grow(len(input))
	n := len(input)
	inSingleQuote := false
	inDoubleQuote := false

	i := 0
	for i < n {
		c := input[i]
		if !inSingleQuote && !inDoubleQuote {
			if c == '\'' {
				inSingleQuote = true
				out.WriteByte(c)
				i++
				continue
			}
			if c == '"' {
				inDoubleQuote = true
				out.WriteByte(c)
				i++
				continue
			}
			if c == '/' && i+1 < n && input[i+1] == '*' {
				i += 2
				for i < n {
					if input[i] == '*' && i+1 < n && input[i+1] == '/' {
						i += 2
						break
					}
					i++
				}
				continue
			}
			out.WriteByte(c)
			i++
		} else if inSingleQuote {
			out.WriteByte(c)
			if c == '\\' && i+1 < n {
				i++
				out.WriteByte(input[i])
			} else if c == '\'' {
				inSingleQuote = false
			}
			i++
		} else if inDoubleQuote {
			out.WriteByte(c)
			if c == '\\' && i+1 < n {
				i++
				out.WriteByte(input[i])
			} else if c == '"' {
				inDoubleQuote = false
			}
			i++
		}
	}
	return out.String()
}

// stripImportDirectives removes all @import statements from CSS.
func stripImportDirectives(css string) string {
	var out strings.Builder
	out.Grow(len(css))
	n := len(css)
	i := 0
	inSingleQuote := false
	inDoubleQuote := false

	for i < n {
		c := css[i]
		if !inSingleQuote && !inDoubleQuote {
			if c == '\'' {
				inSingleQuote = true
				out.WriteByte(c)
				i++
				continue
			}
			if c == '"' {
				inDoubleQuote = true
				out.WriteByte(c)
				i++
				continue
			}
			if c == '@' && i+7 <= n && strings.EqualFold(css[i:i+7], "@import") {
				if i+7 == n || unicode.IsSpace(rune(css[i+7])) || css[i+7] == '"' || css[i+7] == '\'' || css[i+7] == ';' {
					i += 7
					importInSingle := false
					importInDouble := false
					for i < n {
						ic := css[i]
						if !importInSingle && !importInDouble {
							if ic == '\'' {
								importInSingle = true
							} else if ic == '"' {
								importInDouble = true
							} else if ic == ';' {
								i++
								break
							} else if ic == '{' {
								break
							}
						} else if importInSingle {
							if ic == '\\' && i+1 < n {
								i++
							} else if ic == '\'' {
								importInSingle = false
							}
						} else if importInDouble {
							if ic == '\\' && i+1 < n {
								i++
							} else if ic == '"' {
								importInDouble = false
							}
						}
						i++
					}
					continue
				}
			}
			out.WriteByte(c)
			i++
		} else if inSingleQuote {
			out.WriteByte(c)
			if c == '\\' && i+1 < n {
				i++
				out.WriteByte(css[i])
			} else if c == '\'' {
				inSingleQuote = false
			}
			i++
		} else if inDoubleQuote {
			out.WriteByte(c)
			if c == '\\' && i+1 < n {
				i++
				out.WriteByte(css[i])
			} else if c == '"' {
				inDoubleQuote = false
			}
			i++
		}
	}
	return out.String()
}

type cssRule struct {
	header string
	body   string
}

// parseCSSRules parses top-level CSS rules delimited by curly braces.
func parseCSSRules(input string) []cssRule {
	var rules []cssRule
	n := len(input)
	i := 0
	start := 0
	inSingleQuote := false
	inDoubleQuote := false
	braceDepth := 0
	header := ""

	for i < n {
		c := input[i]
		if !inSingleQuote && !inDoubleQuote {
			if c == '\'' {
				inSingleQuote = true
				i++
				continue
			}
			if c == '"' {
				inDoubleQuote = true
				i++
				continue
			}
			if c == '{' {
				if braceDepth == 0 {
					header = strings.TrimSpace(input[start:i])
					start = i + 1
				}
				braceDepth++
				i++
				continue
			}
			if c == '}' {
				if braceDepth > 0 {
					braceDepth--
					if braceDepth == 0 {
						body := strings.TrimSpace(input[start:i])
						if header != "" || body != "" {
							rules = append(rules, cssRule{
								header: header,
								body:   body,
							})
						}
						header = ""
						start = i + 1
					}
				} else {
					start = i + 1
				}
				i++
				continue
			}
			if c == ';' && braceDepth == 0 {
				start = i + 1
				i++
				continue
			}
			i++
		} else if inSingleQuote {
			if c == '\\' && i+1 < n {
				i += 2
				continue
			}
			if c == '\'' {
				inSingleQuote = false
			}
			i++
		} else if inDoubleQuote {
			if c == '\\' && i+1 < n {
				i += 2
				continue
			}
			if c == '"' {
				inDoubleQuote = false
			}
			i++
		}
	}

	if braceDepth > 0 && header != "" {
		body := strings.TrimSpace(input[start:])
		rules = append(rules, cssRule{
			header: header,
			body:   body,
		})
	}

	return rules
}

// processCSSRules scopes selectors and sanitizes declarations according to rule types.
func processCSSRules(rules []cssRule, scopePrefix string) string {
	var sb strings.Builder
	for _, r := range rules {
		header := strings.TrimSpace(r.header)
		if header == "" {
			continue
		}
		lowerHeader := strings.ToLower(header)

		// Keyframes: preserve inner selectors (from, to, 0%, 100%) without scope prefix
		if strings.HasPrefix(lowerHeader, "@keyframes") ||
			strings.HasPrefix(lowerHeader, "@-webkit-keyframes") ||
			strings.HasPrefix(lowerHeader, "@-moz-keyframes") ||
			strings.HasPrefix(lowerHeader, "@-o-keyframes") {
			innerRules := parseCSSRules(r.body)
			var innerSb strings.Builder
			for _, ir := range innerRules {
				sanDecls := sanitizeDeclarations(ir.body)
				innerSb.WriteString("  ")
				innerSb.WriteString(strings.TrimSpace(ir.header))
				innerSb.WriteString(" {\n")
				for _, line := range strings.Split(sanDecls, "\n") {
					if strings.TrimSpace(line) != "" {
						innerSb.WriteString("    ")
						innerSb.WriteString(strings.TrimSpace(line))
						innerSb.WriteString("\n")
					}
				}
				innerSb.WriteString("  }\n")
			}
			sb.WriteString(header)
			sb.WriteString(" {\n")
			sb.WriteString(innerSb.String())
			sb.WriteString("}\n")
			continue
		}

		// Grouping at-rules: @media, @supports, @container, @layer
		if strings.HasPrefix(lowerHeader, "@media") ||
			strings.HasPrefix(lowerHeader, "@supports") ||
			strings.HasPrefix(lowerHeader, "@container") ||
			strings.HasPrefix(lowerHeader, "@layer") {
			nestedRules := parseCSSRules(r.body)
			nestedProcessed := processCSSRules(nestedRules, scopePrefix)
			if strings.TrimSpace(nestedProcessed) != "" {
				sb.WriteString(header)
				sb.WriteString(" {\n")
				for _, line := range strings.Split(nestedProcessed, "\n") {
					if strings.TrimSpace(line) != "" {
						sb.WriteString("  ")
						sb.WriteString(line)
						sb.WriteString("\n")
					}
				}
				sb.WriteString("}\n")
			}
			continue
		}

		// Other at-rules with block: @font-face, @page
		if strings.HasPrefix(lowerHeader, "@") {
			sanDecls := sanitizeDeclarations(r.body)
			sb.WriteString(header)
			sb.WriteString(" {\n")
			for _, line := range strings.Split(sanDecls, "\n") {
				if strings.TrimSpace(line) != "" {
					sb.WriteString("  ")
					sb.WriteString(strings.TrimSpace(line))
					sb.WriteString("\n")
				}
			}
			sb.WriteString("}\n")
			continue
		}

		// Regular style rule: scope each selector and sanitize declarations
		scopedHeader := scopeSelectorList(header, scopePrefix)
		sanDecls := sanitizeDeclarations(r.body)
		sb.WriteString(scopedHeader)
		sb.WriteString(" {\n")
		for _, line := range strings.Split(sanDecls, "\n") {
			if strings.TrimSpace(line) != "" {
				sb.WriteString("  ")
				sb.WriteString(strings.TrimSpace(line))
				sb.WriteString("\n")
			}
		}
		sb.WriteString("}\n")
	}
	return sb.String()
}

// splitSelectors splits a comma-separated selector string, ignoring commas inside brackets or quotes.
func splitSelectors(s string) []string {
	var selectors []string
	var current strings.Builder
	n := len(s)
	inSingle := false
	inDouble := false
	bracketDepth := 0
	parenDepth := 0

	for i := 0; i < n; i++ {
		c := s[i]
		if !inSingle && !inDouble {
			switch c {
			case '\'':
				inSingle = true
				current.WriteByte(c)
			case '"':
				inDouble = true
				current.WriteByte(c)
			case '[':
				bracketDepth++
				current.WriteByte(c)
			case ']':
				if bracketDepth > 0 {
					bracketDepth--
				}
				current.WriteByte(c)
			case '(':
				parenDepth++
				current.WriteByte(c)
			case ')':
				if parenDepth > 0 {
					parenDepth--
				}
				current.WriteByte(c)
			case ',':
				if bracketDepth == 0 && parenDepth == 0 {
					part := strings.TrimSpace(current.String())
					if part != "" {
						selectors = append(selectors, part)
					}
					current.Reset()
					continue
				}
				current.WriteByte(c)
			default:
				current.WriteByte(c)
			}
		} else if inSingle {
			current.WriteByte(c)
			if c == '\\' && i+1 < n {
				i++
				current.WriteByte(s[i])
			} else if c == '\'' {
				inSingle = false
			}
		} else if inDouble {
			current.WriteByte(c)
			if c == '\\' && i+1 < n {
				i++
				current.WriteByte(s[i])
			} else if c == '"' {
				inDouble = false
			}
		}
	}
	if last := strings.TrimSpace(current.String()); last != "" {
		selectors = append(selectors, last)
	}
	return selectors
}

// scopeSelectorList prefixes all selectors in a comma-separated list.
func scopeSelectorList(header string, scopePrefix string) string {
	parts := splitSelectors(header)
	if len(parts) == 0 {
		return header
	}

	scoped := make([]string, 0, len(parts))
	for _, p := range parts {
		sc := scopeSingleSelector(p, scopePrefix)
		if sc != "" {
			scoped = append(scoped, sc)
		}
	}
	if len(scoped) == 0 {
		return header
	}
	return strings.Join(scoped, ", ")
}

// scopeSingleSelector scopes an individual CSS selector.
func scopeSingleSelector(sel string, scopePrefix string) string {
	s := strings.TrimSpace(sel)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, scopePrefix) {
		return s
	}

	lower := strings.ToLower(s)
	if lower == ":root" || lower == "html" || lower == "body" || lower == ":host" {
		return scopePrefix
	}
	if strings.HasPrefix(lower, ":root.") || strings.HasPrefix(lower, "html.") || strings.HasPrefix(lower, "body.") {
		dotIdx := strings.Index(s, ".")
		return scopePrefix + s[dotIdx:]
	}
	if strings.HasPrefix(lower, ":root[") || strings.HasPrefix(lower, "html[") || strings.HasPrefix(lower, "body[") {
		bracketIdx := strings.Index(s, "[")
		return scopePrefix + s[bracketIdx:]
	}
	if strings.HasPrefix(lower, ":root ") {
		return scopePrefix + " " + strings.TrimSpace(s[5:])
	}
	if strings.HasPrefix(lower, "html ") {
		return scopePrefix + " " + strings.TrimSpace(s[4:])
	}
	if strings.HasPrefix(lower, "body ") {
		return scopePrefix + " " + strings.TrimSpace(s[4:])
	}
	if strings.HasPrefix(lower, ":root >") {
		return scopePrefix + " > " + strings.TrimSpace(s[7:])
	}
	if strings.HasPrefix(lower, "html >") {
		return scopePrefix + " > " + strings.TrimSpace(s[6:])
	}
	if strings.HasPrefix(lower, "body >") {
		return scopePrefix + " > " + strings.TrimSpace(s[6:])
	}
	if strings.HasPrefix(s, "&") {
		return scopePrefix + s[1:]
	}

	return scopePrefix + " " + s
}

// splitDeclarations splits declaration statements by semicolon, respecting strings and parentheses.
func splitDeclarations(body string) []string {
	var decls []string
	var current strings.Builder
	n := len(body)
	inSingle := false
	inDouble := false
	parenDepth := 0

	for i := 0; i < n; i++ {
		c := body[i]
		if !inSingle && !inDouble {
			switch c {
			case '\'':
				inSingle = true
				current.WriteByte(c)
			case '"':
				inDouble = true
				current.WriteByte(c)
			case '(':
				parenDepth++
				current.WriteByte(c)
			case ')':
				if parenDepth > 0 {
					parenDepth--
				}
				current.WriteByte(c)
			case ';':
				if parenDepth == 0 {
					part := strings.TrimSpace(current.String())
					if part != "" {
						decls = append(decls, part)
					}
					current.Reset()
					continue
				}
				current.WriteByte(c)
			default:
				current.WriteByte(c)
			}
		} else if inSingle {
			current.WriteByte(c)
			if c == '\\' && i+1 < n {
				i++
				current.WriteByte(body[i])
			} else if c == '\'' {
				inSingle = false
			}
		} else if inDouble {
			current.WriteByte(c)
			if c == '\\' && i+1 < n {
				i++
				current.WriteByte(body[i])
			} else if c == '"' {
				inDouble = false
			}
		}
	}
	if last := strings.TrimSpace(current.String()); last != "" {
		decls = append(decls, last)
	}
	return decls
}

// sanitizeDeclarations strips dangerous CSS declarations like expression, behavior, or javascript url.
func sanitizeDeclarations(body string) string {
	decls := splitDeclarations(body)
	var validDecls []string

	for _, d := range decls {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		colonIdx := strings.Index(d, ":")
		if colonIdx == -1 {
			continue
		}
		prop := strings.TrimSpace(d[:colonIdx])
		val := strings.TrimSpace(d[colonIdx+1:])
		if prop == "" || val == "" {
			continue
		}

		lowerProp := strings.ToLower(prop)
		if lowerProp == "behavior" || lowerProp == "-ms-behavior" || lowerProp == "-moz-binding" {
			continue
		}

		if isDangerousValue(val) {
			continue
		}

		validDecls = append(validDecls, fmt.Sprintf("%s: %s;", prop, val))
	}

	return strings.Join(validDecls, "\n")
}

// isDangerousValue checks if a CSS declaration value contains unsafe constructs.
func isDangerousValue(val string) bool {
	if reExpression.MatchString(val) {
		return true
	}
	if reJS.MatchString(val) || reVBS.MatchString(val) {
		return true
	}

	var clean strings.Builder
	for _, r := range strings.ToLower(val) {
		if !unicode.IsSpace(r) && r != '\\' && r != '\'' && r != '"' {
			clean.WriteRune(r)
		}
	}
	s := clean.String()
	if strings.Contains(s, "javascript:") ||
		strings.Contains(s, "vbscript:") ||
		strings.Contains(s, "expression(") ||
		strings.Contains(s, "data:text/html") ||
		strings.Contains(s, "data:text/javascript") {
		return true
	}

	return false
}
