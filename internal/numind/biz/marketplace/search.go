package marketplace

import "strings"

// booleanModeOperatorCleaner strips MySQL FULLTEXT BOOLEAN MODE special chars
// from user input. Package-level singleton so we don't rebuild the Replacer
// on every List call (low cost but free win — Replacer is immutable +
// goroutine-safe).
var booleanModeOperatorCleaner = strings.NewReplacer(
	"+", "", "-", "", "*", "", `"`, "",
	"(", "", ")", "", "~", "",
	"<", "", ">", "", "@", "",
)

// booleanModeQuery escapes user-typed search input for MySQL FULLTEXT BOOLEAN
// MODE syntax (spec §3.4 D-FT-BOOLEAN). It strips boolean operators (+, -, *,
// ", (, ), ~, <, >, @) from each whitespace-separated token, then wraps each
// surviving token with a leading + (required, AND) and trailing * (prefix
// match). Tokens that become empty after stripping are dropped.
//
// Examples:
//   - "销售 调研"          → "+销售* +调研*"
//   - "+销售 -调研"        → "+销售* +调研*"  (operators stripped, both required)
//   - "\"短语\""           → "+短语*"        (quotes stripped, treated as token)
//   - ""                    → ""              (caller MUST NOT pass empty to MATCH AGAINST)
//   - "   *  "             → ""              (only operators → empty)
//
// Returns empty string when input is empty after escape — callers should
// branch and skip the MATCH AGAINST clause rather than emit it with no
// effective tokens (which BOOLEAN MODE would treat as match-all).
func booleanModeQuery(q string) string {
	if q == "" {
		return ""
	}
	tokens := strings.Fields(q)
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		clean := booleanModeOperatorCleaner.Replace(t)
		if clean == "" {
			continue
		}
		out = append(out, "+"+clean+"*")
	}
	return strings.Join(out, " ")
}
