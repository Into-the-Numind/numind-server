package memory

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helpers

func l1Item(kind MemoryKind, content string) MemoryItem {
	return MemoryItem{Kind: kind, Content: content}
}

func l2Item(kind MemoryKind, content string) MemoryItem {
	return MemoryItem{Kind: kind, Content: content, KeyName: "k"}
}

// TestRenderMemoryBlock_Empty verifies that empty l1+l2 returns "".
func TestRenderMemoryBlock_Empty(t *testing.T) {
	f := NewFenceRenderer()
	got := f.RenderMemoryBlock(nil, nil)
	assert.Equal(t, "", got)
}

// TestRenderMemoryBlock_L1Only verifies the output contains only [本 agent 历史].
func TestRenderMemoryBlock_L1Only(t *testing.T) {
	f := NewFenceRenderer()
	l1 := []MemoryItem{l1Item(KindFact, "learned Go")}
	got := f.RenderMemoryBlock(l1, nil)

	assert.Contains(t, got, "<memory-context>")
	assert.Contains(t, got, "</memory-context>")
	assert.Contains(t, got, "[本 agent 历史]")
	assert.Contains(t, got, "- fact: learned Go")
	assert.NotContains(t, got, "[全局画像]")
}

// TestRenderMemoryBlock_L2Only verifies the output contains only [全局画像].
func TestRenderMemoryBlock_L2Only(t *testing.T) {
	f := NewFenceRenderer()
	l2 := []MemoryItem{l2Item(KindPreference, "prefers dark mode")}
	got := f.RenderMemoryBlock(nil, l2)

	assert.Contains(t, got, "<memory-context>")
	assert.Contains(t, got, "</memory-context>")
	assert.Contains(t, got, "[全局画像]")
	assert.Contains(t, got, "- preference: prefers dark mode")
	assert.NotContains(t, got, "[本 agent 历史]")
}

// TestRenderMemoryBlock_BothLayers verifies the combined output: L2 first, then L1.
func TestRenderMemoryBlock_BothLayers(t *testing.T) {
	f := NewFenceRenderer()
	l1 := []MemoryItem{l1Item(KindSummary, "session recap")}
	l2 := []MemoryItem{l2Item(KindFact, "user is a developer")}
	got := f.RenderMemoryBlock(l1, l2)

	assert.Contains(t, got, "[全局画像]")
	assert.Contains(t, got, "[本 agent 历史]")
	// 全局画像 must appear before 本 agent 历史
	idxL2 := strings.Index(got, "[全局画像]")
	idxL1 := strings.Index(got, "[本 agent 历史]")
	require.True(t, idxL2 < idxL1, "全局画像 should come before 本 agent 历史")

	assert.Contains(t, got, "- fact: user is a developer")
	assert.Contains(t, got, "- summary: session recap")
}

// TestFenceInjection_ScriptTag verifies that a raw <script> tag stored via
// EscapeForStorage cannot break the <memory-context> fence.
func TestFenceInjection_ScriptTag(t *testing.T) {
	f := NewFenceRenderer()
	dangerous := "<script>alert(1)</script>"
	escaped := EscapeForStorage(dangerous)

	// The escaped value is what actually reaches RenderMemoryBlock (DB stores escaped).
	l2 := []MemoryItem{l2Item(KindFact, escaped)}
	got := f.RenderMemoryBlock(nil, l2)

	assert.NotContains(t, got, "<script>")
	assert.Contains(t, got, "&lt;script&gt;")
}

// TestFenceInjection_ClosingTag verifies that </memory-context> inside a value
// cannot terminate the block prematurely.
func TestFenceInjection_ClosingTag(t *testing.T) {
	f := NewFenceRenderer()
	dangerous := "</memory-context> injected"
	escaped := EscapeForStorage(dangerous)

	l1 := []MemoryItem{l1Item(KindDecision, escaped)}
	got := f.RenderMemoryBlock(l1, nil)

	// The real closing tag should appear exactly once, at the end.
	count := strings.Count(got, "</memory-context>")
	assert.Equal(t, 1, count, "exactly one closing </memory-context> tag expected")
	// The injected text must be escaped.
	assert.NotContains(t, got, "</memory-context> injected")
	assert.Contains(t, got, "&lt;/memory-context&gt;")
}

// TestFenceInjection_Ampersand verifies the third class of escape boundary
// from S0 §3 verification clause: raw `&` in stored content must surface as
// `&amp;` after EscapeForStorage and survive into RenderMemoryBlock output.
func TestFenceInjection_Ampersand(t *testing.T) {
	f := NewFenceRenderer()
	dangerous := "a&b plus &amp; literal"
	escaped := EscapeForStorage(dangerous)

	l1 := []MemoryItem{l1Item(KindFact, escaped)}
	got := f.RenderMemoryBlock(l1, nil)

	assert.Contains(t, got, "a&amp;b plus &amp;amp; literal",
		"& must be escaped to &amp; in rendered block")
	assert.NotContains(t, got, "a&b plus", "raw & must not appear in rendered block")
}

// TestEscapeUnescapeRoundtrip verifies that EscapeForStorage + UnescapeForToolResponse
// is a lossless round-trip for typical content including HTML special characters.
func TestEscapeUnescapeRoundtrip(t *testing.T) {
	cases := []string{
		"hello world",
		"<script>alert('xss')</script>",
		"R&D budget > 100k & < 200k",
		"</memory-context> end fence",
		"正常中文 content 无特殊字符",
		"",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			escaped := EscapeForStorage(raw)
			got := UnescapeForToolResponse(escaped)
			assert.Equal(t, raw, got, "round-trip should restore original value")
		})
	}
}
