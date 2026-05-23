package search

import (
	"html"
	"strings"
	"unicode/utf8"
)

const (
	// snippetWindow is the character window on each side of the first match.
	// 50 chars before + 50 chars after = roughly 100-char snippet plus markers.
	snippetWindow = 50
	// ngramSize matches MySQL ngram_token_size (n=2) so client snippet algorithm
	// uses the same tokens FULLTEXT MATCH AGAINST does.
	ngramSize = 2
)

// makeSnippet builds an HTML-safe snippet for content matching query.
//
// Algorithm (spec §Snippet 高亮):
//  1. Split query into ngram tokens (n=2). Short queries (<= 1 char) keep the
//     whole query as a single token.
//  2. Find the first ngram token's position in content.
//  3. Slice content[max(0, pos-50) : min(len, pos+50)].
//  4. HTML-escape the slice, then wrap each token occurrence in <mark>...</mark>.
//  5. Add "..." prefix/suffix to indicate truncation.
//
// Critical: content is HTML-escaped BEFORE inserting <mark> tags. The <mark>
// markup itself is the only allowed HTML in the result — v-html consumers
// receive a trusted snippet.
func makeSnippet(content, query string) string {
	query = strings.TrimSpace(query)
	if content == "" || query == "" {
		return html.EscapeString(content)
	}

	tokens := ngramTokens(query, ngramSize)
	if len(tokens) == 0 {
		// content has no matching ngram tokens — just return escaped content
		// truncated to a reasonable length so callers always get *something*.
		return truncateRunes(html.EscapeString(content), 2*snippetWindow)
	}

	// Find earliest match position (rune index) across tokens.
	contentRunes := []rune(content)
	earliestPos := -1
	for _, tok := range tokens {
		tokRunes := []rune(tok)
		if len(tokRunes) == 0 {
			continue
		}
		// Search rune-by-rune so positions align with rune slicing.
		p := indexRunes(contentRunes, tokRunes)
		if p >= 0 && (earliestPos == -1 || p < earliestPos) {
			earliestPos = p
		}
	}

	// No token matched — return truncated escaped content.
	if earliestPos == -1 {
		return truncateRunes(html.EscapeString(content), 2*snippetWindow)
	}

	// Slice window around earliestPos.
	start := earliestPos - snippetWindow
	prefixEllipsis := true
	if start <= 0 {
		start = 0
		prefixEllipsis = false
	}
	end := earliestPos + snippetWindow
	suffixEllipsis := true
	if end >= len(contentRunes) {
		end = len(contentRunes)
		suffixEllipsis = false
	}
	window := string(contentRunes[start:end])

	// HTML-escape window first, then insert <mark> around tokens. We do the
	// insertion AFTER escape so that <mark> markup itself stays intact while
	// any '<' / '>' / '&' / '"' / '\'' in user content gets escaped.
	escaped := html.EscapeString(window)

	// Wrap each ngram token. Escape token before matching against escaped text
	// so that special-char tokens (rare for Chinese) still match.
	for _, tok := range tokens {
		escTok := html.EscapeString(tok)
		if escTok == "" {
			continue
		}
		// Case-sensitive replace — Chinese has no case so it's fine; ASCII
		// queries lose case-insensitive match but that's acceptable for V1.5.
		escaped = strings.ReplaceAll(escaped, escTok, "<mark>"+escTok+"</mark>")
	}

	if prefixEllipsis {
		escaped = "..." + escaped
	}
	if suffixEllipsis {
		escaped = escaped + "..."
	}
	return escaped
}

// ngramTokens splits s into all ngram-sized substrings (n=2 for MySQL ngram).
// Short queries (<= 1 rune) return the whole query as a single token.
// Tokens are de-duplicated to avoid redundant replacements.
func ngramTokens(s string, n int) []string {
	runes := []rune(s)
	if len(runes) <= 1 {
		if len(runes) == 0 {
			return nil
		}
		return []string{s}
	}
	if n < 2 {
		n = 2
	}
	if len(runes) < n {
		return []string{s}
	}
	seen := make(map[string]struct{}, len(runes)-n+1)
	tokens := make([]string, 0, len(runes)-n+1)
	for i := 0; i <= len(runes)-n; i++ {
		tok := string(runes[i : i+n])
		// skip whitespace-only tokens
		if strings.TrimSpace(tok) == "" {
			continue
		}
		if _, ok := seen[tok]; ok {
			continue
		}
		seen[tok] = struct{}{}
		tokens = append(tokens, tok)
	}
	return tokens
}

// indexRunes returns the first index in haystack where needle occurs, or -1.
// Uses rune slicing so the returned index aligns with rune-based slicing.
func indexRunes(haystack, needle []rune) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return -1
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// truncateRunes returns s shortened to at most n runes plus "..." suffix
// if truncation actually happened.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "..."
}
