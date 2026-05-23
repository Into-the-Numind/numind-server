package memory

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsTrivial covers the spec §验证策略 case table for the trivial-prompt
// short-circuit. The classifier is a pure function with no DB / network
// dependency — all branches are exercised here.
func TestIsTrivial(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		// ── spec §验证策略 §TestIsTrivial ────────────────────────────────────
		{name: "EmptyString", input: "", want: true},
		{name: "Whitespace", input: "   ", want: true},
		{name: "ChineseShort", input: "好的", want: true},
		{name: "EnglishWithPunct", input: "thanks!", want: true},
		{name: "SingleEmoji", input: "👍", want: true},
		{name: "MultiEmoji", input: "👍🎉", want: true},
		{name: "TrivialPrefixWithContent", input: "ok 帮我写客户开场白", want: false},
		{name: "ChineseLong", input: "我是医疗器械销售", want: false},
		{name: "EmojiPlusContent", input: ":) 怎么办", want: false},
		{
			// Spec §决策项:保守判 false。两个 trivial token 合起来是混合用语，
			// 避免误短路；后续可观察实际频率再加白名单。
			name:  "MixedLanguageTrivial",
			input: "ok 好的",
			want:  false,
		},

		// ── Additional boundary coverage ────────────────────────────────────
		// "ok"(2) + 7 emoji 远超 trivial_max_chars=8 — 即便全部是 trivial token
		// 单独看，整体仍超阈值；按"全 input ≤ 阈值"规则非 trivial。
		{name: "VeryLongEmojiString", input: "👍👍👍👍👍👍👍👍👍👍👍👍", want: false},
		// Whitespace 内嵌单 emoji — normalize 去空白后变成 "👍"，trivial。
		{name: "EmojiWrappedInSpace", input: "  👍  ", want: true},
		// 多 ASCII 标点装饰 "thanks" → normalize → "thanks" → trivialExact 命中。
		{name: "ThanksHeavilyPunctuated", input: "...thanks!!!", want: true},
		// Single Chinese full-width period only — normalize 后变空串 → trivial。
		{name: "ChinesePunctOnly", input: "。。。", want: true},
		// 单字 "好" 在 trivialExact (rune ≤ 5) → trivial。
		{name: "SingleChineseHao", input: "好", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTrivial(tt.input)
			assert.Equal(t, tt.want, got,
				"IsTrivial(%q) = %v; want %v", tt.input, got, tt.want)
		})
	}
}

// TestNormalizeTrivial pins the normalize helper's contract — keep this in
// sync with trivialExact map: every key in the map must round-trip through
// normalizeTrivial unchanged (otherwise the exact-match path is unreachable).
func TestNormalizeTrivial(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "AlreadyClean", in: "ok", want: "ok"},
		{name: "Trim", in: "  ok  ", want: "ok"},
		{name: "LowercaseAscii", in: "OK", want: "ok"},
		{name: "StripAsciiPunct", in: "thanks!", want: "thanks"},
		{name: "StripChinesePunct", in: "好的。", want: "好的"},
		{name: "MixedPunctAndCase", in: "Thanks, !", want: "thanks"},
		{name: "AllPunct", in: "...", want: ""},
		{name: "EmojiSurvives", in: "👍 ", want: "👍"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeTrivial(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestTrivialExactMembership asserts every key in trivialExact survives
// normalizeTrivial — otherwise the exact-match branch silently misses them.
// Also caps rune length so the exact-match path's rune-limit guard
// (trivialExactRuneLimit) never silently excludes a map key.
func TestTrivialExactMembership(t *testing.T) {
	for k := range trivialExact {
		normalized := normalizeTrivial(k)
		assert.Equalf(t, k, normalized,
			"trivialExact key %q does not survive normalizeTrivial (got %q) — fix the entry or the normalizer",
			k, normalized)
		assert.LessOrEqualf(t, runeLen(k), trivialExactRuneLimit,
			"trivialExact key %q has %d runes > trivialExactRuneLimit=%d — exact-match path would miss it",
			k, runeLen(k), trivialExactRuneLimit)
	}
}

// TestIsAllEmojiOrCommon covers the emoji-only branch isolation.
func TestIsAllEmojiOrCommon(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"👍", true},
		{"👍🎉", true},
		{"✅", true},
		{"hello", false},
		{"", false},
		{"👍a", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			assert.Equalf(t, c.want, isAllEmojiOrCommon(c.in), "input=%q", c.in)
		})
	}
}
