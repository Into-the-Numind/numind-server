package memory

import (
	"strings"
	"unicode"

	"github.com/spf13/viper"
)

// TrivialMaxCharsDefault is the rune-length cap above which inputs are never
// considered trivial. Spec §关键参数 default = 8 runes.
const TrivialMaxCharsDefault = 8

// trivialExactRuneLimit caps the rune count for the exact-match path. Set
// generously so the longest map key ("thanks" = 6 runes) plus a small safety
// margin still hits — anything longer than this is presumed substantive and
// skips the map lookup entirely. Defended by TestTrivialExactMembership which
// fails the build if a future key exceeds this limit.
const trivialExactRuneLimit = 8

// trivialExact is the canonical set of post-normalize trivial tokens.
//
// Layer A V1.5 § "中英文 trivial token 完全匹配集". All keys must already be
// lowercased and punctuation-stripped — normalizeTrivial guarantees both.
//
// To extend (e.g. add dialectical "中"/"嘛" if observation shows them frequent),
// add the entry below and add a corresponding TestIsTrivial case.
var trivialExact = map[string]bool{
	// English affirmation / acknowledgement
	"ok": true, "okay": true, "yes": true, "no": true, "yep": true, "nope": true,
	"thanks": true, "thank": true, "thx": true, "ty": true,
	"haha": true, "lol": true, "k": true,
	// Chinese affirmation
	"好": true, "好的": true, "好啊": true, "可以": true, "嗯": true, "嗯嗯": true,
	"对": true, "对的": true, "是": true, "是的": true, "收到": true, "明白": true,
	// Chinese thanks / laughter
	"谢谢": true, "感谢": true, "辛苦": true, "辛苦了": true,
	"哈": true, "哈哈": true, "哈哈哈": true,
	// Single emoji
	"👍": true, "🙏": true, "👌": true, "✅": true, "🎉": true,
}

// trivialPunctRunes documents (non-exhaustively) the code-point classes we
// strip during normalize: ASCII / Chinese punctuation, smiley building blocks
// (`:)`), and ZWJ/ZWNJ joiners. The function uses unicode.IsPunct +
// isAsciiSymbolNoise for breadth — this slice is only a documentation hint
// for readers, not consulted at runtime.
//
//nolint:unused // documentation reference, runtime path uses unicode.IsPunct
var trivialPunctRunes = []rune{
	'!', '?', ',', '.', ';', ':', '\'', '"', '(', ')', '[', ']', '{', '}', '-', '_',
	'！', '？', '，', '。', '；', '：', '“', '”', '‘', '’', '（', '）', '【', '】',
	'~', '`', '@', '#', '$', '%', '^', '&', '*', '/', '\\', '|', '<', '>',
}

// normalizeTrivial returns a comparable form of s suitable for exact lookup
// into trivialExact. Steps (order matters):
//
//  1. Trim leading/trailing whitespace.
//  2. Lowercase ASCII letters (Chinese unaffected).
//  3. Strip Unicode punctuation (unicode.IsPunct + symbol-only ASCII like `~`)
//     and whitespace runes.
//
// Note: emoji runes are NOT stripped — they are valid trivial tokens
// (`"👍"`). The empty string is allowed; callers handle that branch.
func normalizeTrivial(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			// drop
		case unicode.IsPunct(r):
			// drop (covers . , ; : ! ? '"' [] (){} etc., both ASCII and CJK)
		case isAsciiSymbolNoise(r):
			// drop ASCII symbols treated as noise (`~`, `^`, `*`, etc.)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isAsciiSymbolNoise reports whether r is one of the ASCII-range symbols we
// treat as decorative noise. unicode.IsPunct already covers most marks but
// misses some math/symbol code-points (e.g. `~` `^` `*`); enumerate the rest.
func isAsciiSymbolNoise(r rune) bool {
	switch r {
	case '~', '`', '^', '*', '_', '+', '=', '|', '<', '>', '@', '#', '$', '%', '&', '/', '\\':
		return true
	}
	return false
}

// isEmojiRune reports whether r belongs to one of the canonical emoji
// Unicode blocks. We are intentionally inclusive — the cost of a false
// positive (treating a CJK symbol as emoji) is low because isAllEmojiOrCommon
// is only consulted when the rune count is already inside trivialMaxChars.
//
// Blocks covered (per Unicode 15):
//
//	0x1F000–0x1FFFF — Supplementary Symbols and Pictographs
//	0x2600–0x27BF   — Miscellaneous Symbols & Dingbats (e.g. ✅, ☀, ✔)
//	0x2300–0x23FF   — Misc Technical (e.g. ⏰)
//	0xFE0F          — Variation Selector-16 (emoji presentation modifier)
//	0x200D          — Zero Width Joiner (compound emoji like 👨‍👩‍👧)
//	0x1F1E6–0x1F1FF — Regional Indicator Symbols (flags)
func isEmojiRune(r rune) bool {
	if r >= 0x1F000 && r <= 0x1FFFF {
		return true
	}
	if r >= 0x2600 && r <= 0x27BF {
		return true
	}
	if r >= 0x2300 && r <= 0x23FF {
		return true
	}
	if r == 0xFE0F || r == 0x200D {
		return true
	}
	return false
}

// isAllEmojiOrCommon reports whether s consists entirely of emoji code points
// (including joiners / variation selectors). Empty s returns false — callers
// should already have short-circuited on empty.
//
// Used by IsTrivial after normalize: when normalize stripped all letters
// but left emoji runes, the input is "all emoji" trivial.
func isAllEmojiOrCommon(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isEmojiRune(r) {
			return false
		}
	}
	return true
}

// runeLen returns the rune (not byte) length of s. Avoids importing utf8.
func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// IsTrivial reports whether userInput should bypass the memory pipeline —
// no Memories section injected, no extraction enqueued (so cheap, no LLM
// cost, no DB churn for "ok"/"👍"/"thanks!" turns).
//
// Algorithm (spec §设计要点):
//
//  1. Trim → if empty, trivial.
//  2. normalizeTrivial(s) (lower + strip punct + strip whitespace).
//  3. After normalize, if rune count ≤ 5 → look up trivialExact map.
//  4. If rune count ≤ trivial_max_chars (default 8) AND consists entirely of
//     emoji / joiners → trivial.
//  5. Else → non-trivial.
//
// Boundary rule (critical): the WHOLE input must reduce to a trivial token.
// "ok 帮我写客户开场白" normalizes to "ok帮我写客户开场白" (Chinese chars survive)
// which is neither in trivialExact nor all-emoji → non-trivial. This prevents
// prefix-trivial inputs from accidentally short-circuiting real requests.
//
// Pure function: no DB, no allocation beyond the small string builder.
// Safe to call on the request hot path.
func IsTrivial(userInput string) bool {
	// Step 1: trim + early-empty short-circuit.
	trimmed := strings.TrimSpace(userInput)
	if trimmed == "" {
		return true
	}

	// Step 2: normalize.
	norm := normalizeTrivial(trimmed)
	if norm == "" {
		// All chars stripped (pure punctuation / whitespace) → trivial.
		return true
	}

	n := runeLen(norm)

	// Step 3: short tokens → look up exact match.
	if n <= trivialExactRuneLimit && trivialExact[norm] {
		return true
	}

	// Step 4: short all-emoji input (≤ trivial_max_chars) → trivial.
	maxChars := TrivialMaxCharsDefault
	if v := viper.GetInt("agent.memory.trivial_max_chars"); v > 0 {
		maxChars = v
	}
	if n <= maxChars && isAllEmojiOrCommon(norm) {
		return true
	}

	return false
}
