package build

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// ─── HTML Minifier ───────────────────────────────────────────

var (
	reHTMLComment = regexp.MustCompile(`(?s)<!--[\s\S]*?-->`)
	reMultiSpace  = regexp.MustCompile(`\s{2,}`)
	reTagGap      = regexp.MustCompile(`>\s{2,}<`)
	reBoolAttrs   = regexp.MustCompile(`(?i)\s(hidden|disabled|checked|readonly|required|autofocus|autoplay|controls|loop|muted|defer|async|nomodule|novalidate|formnovalidate|open|inert)="[^"]*"`)
	reTypeScript  = regexp.MustCompile(`(?i)\s+type="text/(?:java)?script"`)
	reTypeCSS     = regexp.MustCompile(`(?i)\s+type="text/css"`)
	reVoidSlash   = regexp.MustCompile(`(?i)<(area|base|br|col|embed|hr|img|input|link|meta|source|track|wbr)\s([^>]*?)\s*/>`)
)

var blockElements = func() *regexp.Regexp {
	tags := "html|head|body|div|section|article|aside|header|footer|main|nav|ul|ol|li|p|table|tr|td|th|thead|tbody|form|fieldset|figure|figcaption|details|summary|dialog|template|blockquote|dl|dt|dd|meta|link|title|base"
	return regexp.MustCompile(`(?i)\s+(</?(?:` + tags + `)[\s>/])`)
}()

var preservedTags = func() []*regexp.Regexp {
	tags := []string{"pre", "code", "script", "style", "textarea"}
	res := make([]*regexp.Regexp, len(tags))
	for i, tag := range tags {
		res[i] = regexp.MustCompile(`(?is)<` + tag + `[\s>].*?</` + tag + `>`)
	}
	return res
}()

// MinifyHTML removes comments, collapses whitespace, and strips
// unnecessary attributes while preserving content in pre/code/script/style/textarea.
func MinifyHTML(html string) string {
	var preserved []string
	for _, re := range preservedTags {
		html = re.ReplaceAllStringFunc(html, func(match string) string {
			preserved = append(preserved, match)
			return fmt.Sprintf("\x00WL%d\x00", len(preserved)-1)
		})
	}

	html = reHTMLComment.ReplaceAllStringFunc(html, func(m string) string {
		if strings.HasPrefix(m, "<!--[if") {
			return m
		}
		return ""
	})
	html = reTagGap.ReplaceAllString(html, "> <")
	html = reMultiSpace.ReplaceAllString(html, " ")
	html = blockElements.ReplaceAllString(html, "$1")
	html = reBoolAttrs.ReplaceAllString(html, " $1")
	html = reTypeScript.ReplaceAllString(html, "")
	html = reTypeCSS.ReplaceAllString(html, "")
	html = reVoidSlash.ReplaceAllString(html, "<$1 $2>")

	for i, block := range preserved {
		html = strings.Replace(html, fmt.Sprintf("\x00WL%d\x00", i), block, 1)
	}

	var lines []string
	for _, line := range strings.Split(html, "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			lines = append(lines, t)
		}
	}
	return strings.Join(lines, "\n")
}

// ─── CSS Minifier ────────────────────────────────────────────

var (
	reCSSComment = regexp.MustCompile(`(?s)/\*[\s\S]*?\*/`)
	reWS         = regexp.MustCompile(`\s+`)
	reCSSSyntax  = regexp.MustCompile(`\s*([{}:;,>~+])\s*`)
	reCSSParens  = regexp.MustCompile(`\s*([()])\s*`)
)

// MinifyCSS removes comments, collapses whitespace, and removes
// unnecessary characters from CSS.
func MinifyCSS(css string) string {
	css = reCSSComment.ReplaceAllString(css, "")
	css = reWS.ReplaceAllString(css, " ")
	css = reCSSSyntax.ReplaceAllString(css, "$1")
	css = reCSSParens.ReplaceAllString(css, "$1")
	css = strings.ReplaceAll(css, ";}", "}")
	return strings.TrimSpace(css)
}

// ─── JavaScript Minifier ─────────────────────────────────────

var (
	reJSWhitespace = regexp.MustCompile(`\s+`)
	reJSSyntax     = regexp.MustCompile(` *([{}();,=<>!&|+\-*/?:\[\]~^%]) *`)
)

var jsKeywords = []string{
	"instanceof", "function", "continue", "extends", "default", "typeof",
	"return", "import", "export", "delete", "static", "switch",
	"yield", "throw", "await", "async", "class", "const", "break",
	"while", "catch", "super", "case", "else", "from", "void",
	"new", "let", "var", "get", "set", "for", "try",
	"of", "in",
}

// MinifyJS strips comments, collapses whitespace, and restores required keyword spacing.
func MinifyJS(js string) string {
	stripped := stripJSComments(js)
	result := collapseJSWhitespace(stripped)
	return strings.TrimSpace(result)
}

func stripJSComments(src string) string {
	var buf bytes.Buffer
	b := []byte(src)
	n := len(b)
	i := 0

	for i < n {
		ch := b[i]

		// Single-quoted string
		if ch == '\'' {
			end := i + 1
			for end < n && b[end] != '\'' {
				if b[end] == '\\' {
					end++
				}
				end++
			}
			if end < n {
				end++
			}
			buf.Write(b[i:end])
			i = end
			continue
		}

		// Double-quoted string
		if ch == '"' {
			end := i + 1
			for end < n && b[end] != '"' {
				if b[end] == '\\' {
					end++
				}
				end++
			}
			if end < n {
				end++
			}
			buf.Write(b[i:end])
			i = end
			continue
		}

		// Template literal
		if ch == '`' {
			end := i + 1
			depth := 0
			for end < n {
				if b[end] == '\\' {
					end += 2
					continue
				}
				if b[end] == '`' && depth == 0 {
					break
				}
				if b[end] == '$' && end+1 < n && b[end+1] == '{' {
					depth++
					end += 2
					continue
				}
				if b[end] == '}' && depth > 0 {
					depth--
					end++
					continue
				}
				end++
			}
			if end < n {
				end++
			}
			buf.Write(b[i:end])
			i = end
			continue
		}

		// Block comment
		if ch == '/' && i+1 < n && b[i+1] == '*' {
			isLicense := i+2 < n && b[i+2] == '!'
			closeIdx := bytes.Index(b[i+2:], []byte("*/"))
			if closeIdx == -1 {
				i = n
				continue
			}
			closeIdx += i + 2
			if isLicense {
				buf.Write(b[i : closeIdx+2])
			}
			i = closeIdx + 2
			continue
		}

		// Line comment
		if ch == '/' && i+1 < n && b[i+1] == '/' {
			eol := bytes.IndexByte(b[i:], '\n')
			if eol == -1 {
				i = n
			} else {
				i += eol
			}
			continue
		}

		buf.WriteByte(ch)
		i++
	}
	return buf.String()
}

func collapseJSWhitespace(src string) string {
	var literals []string
	var buf bytes.Buffer
	b := []byte(src)
	n := len(b)
	i := 0

	for i < n {
		ch := b[i]

		if ch == '\'' {
			end := i + 1
			for end < n && b[end] != '\'' {
				if b[end] == '\\' {
					end++
				}
				end++
			}
			if end < n {
				end++
			}
			literals = append(literals, string(b[i:end]))
			buf.WriteString(fmt.Sprintf("\x00SL%d\x00", len(literals)-1))
			i = end
			continue
		}

		if ch == '"' {
			end := i + 1
			for end < n && b[end] != '"' {
				if b[end] == '\\' {
					end++
				}
				end++
			}
			if end < n {
				end++
			}
			literals = append(literals, string(b[i:end]))
			buf.WriteString(fmt.Sprintf("\x00SL%d\x00", len(literals)-1))
			i = end
			continue
		}

		if ch == '`' {
			end := i + 1
			depth := 0
			for end < n {
				if b[end] == '\\' {
					end += 2
					continue
				}
				if b[end] == '`' && depth == 0 {
					break
				}
				if b[end] == '$' && end+1 < n && b[end+1] == '{' {
					depth++
					end += 2
					continue
				}
				if b[end] == '}' && depth > 0 {
					depth--
					end++
					continue
				}
				end++
			}
			if end < n {
				end++
			}
			literals = append(literals, string(b[i:end]))
			buf.WriteString(fmt.Sprintf("\x00SL%d\x00", len(literals)-1))
			i = end
			continue
		}

		buf.WriteByte(ch)
		i++
	}

	result := buf.String()
	result = reJSWhitespace.ReplaceAllString(result, " ")
	result = reJSSyntax.ReplaceAllString(result, "$1")
	result = restoreKeywordSpaces(result)

	for idx, s := range literals {
		result = strings.Replace(result, fmt.Sprintf("\x00SL%d\x00", idx), s, 1)
	}
	return result
}

func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_' || c == '$'
}

func isIdentOrQuote(c byte) bool {
	return isIdentChar(c) || c == '"' || c == '\'' || c == '`'
}

func restoreKeywordSpaces(src string) string {
	var buf bytes.Buffer
	n := len(src)
	i := 0

	for i < n {
		matched := false
		for _, kw := range jsKeywords {
			kwLen := len(kw)
			if i+kwLen > n {
				continue
			}
			if src[i:i+kwLen] != kw {
				continue
			}
			if i > 0 && isIdentChar(src[i-1]) {
				continue
			}
			endPos := i + kwLen
			if endPos < n && isIdentChar(src[endPos]) {
				continue
			}
			buf.WriteString(kw)
			if endPos < n && isIdentOrQuote(src[endPos]) {
				buf.WriteByte(' ')
			}
			i = endPos
			matched = true
			break
		}
		if !matched {
			buf.WriteByte(src[i])
			i++
		}
	}
	return buf.String()
}
