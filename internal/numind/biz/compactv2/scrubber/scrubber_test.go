package scrubber

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStreamScrubber_TableDriven 覆盖 spec task-05 §验证策略 §单元测试 case 列表
// 的全部 18 个 case。每个 case 模拟分 chunk Push 后 Flush，断言累积输出。
func TestStreamScrubber_TableDriven(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   string
	}{
		// ─── XML 标签 scrub ────────────────────────────────────────────────────
		// case 1: 单 chunk 完整 <memory data-internal="true">...</memory> → scrubbed
		{
			name:   "case01_single_chunk_memory",
			chunks: []string{`<memory data-internal="true">x</memory>foo`},
			want:   "foo",
		},

		// case 2: 跨 2 chunk 标签拆分 → scrubbed
		{
			name:   "case02_cross_2_chunk_memory",
			chunks: []string{`<mem`, `ory data-internal="true">x</memory>foo`},
			want:   "foo",
		},

		// case 3: 跨 5+ chunk 极端拆分（每 chunk 1-2 字符）→ scrubbed
		// 把整段 `<memory data-internal="true">x</memory>tail` 按 2 字符一刀切分。
		{
			name: "case03_cross_many_chunks_extreme",
			chunks: chunkSplit(
				`<memory data-internal="true">x</memory>tail`, 2,
			),
			want: "tail",
		},

		// case 4: 用户输入裸 <memory> 不带 data-internal → 不剥
		{
			name:   "case04_user_literal_memory_no_attr",
			chunks: []string{`<memory>what is this</memory>`},
			want:   `<memory>what is this</memory>`,
		},

		// case 5: 单 chunk <reference-only data-internal="true">...</reference-only> → scrubbed (D5)
		{
			name:   "case05_reference_only_single_chunk",
			chunks: []string{`<reference-only data-internal="true">12-section summary</reference-only>foo`},
			want:   "foo",
		},

		// case 6: 跨 chunk <reference-only ...> (chunk1) + content (chunk2..N) + </reference-only> (chunkN+1)
		{
			name: "case06_reference_only_cross_chunk",
			chunks: []string{
				`<reference-only data-internal="true">`,
				`## 1. Active Task\n## 2. Goal\n`,
				`</reference-only>foo`,
			},
			want: "foo",
		},

		// case 7: 用户裸 <reference-only> 不带 data-internal → 不剥 (D5 白名单)
		{
			name:   "case07_reference_only_user_literal_no_attr",
			chunks: []string{`<reference-only>user wrote this</reference-only>`},
			want:   `<reference-only>user wrote this</reference-only>`,
		},

		// ─── Block-level marker (legacy fallback) ─────────────────────────────
		// case 8: [CONTEXT COMPACTION — REFERENCE ONLY]\n...\n\nfoo → scrub 到 \n\n
		{
			name:   "case08_context_compaction_block_boundary",
			chunks: []string{"[CONTEXT COMPACTION — REFERENCE ONLY]\nbody body\n\nfoo"},
			want:   "foo",
		},

		// case 9: [REFERENCE ONLY] 流末尾无 \n\n → 用 \z 边界（终止时）
		{
			name:   "case09_reference_only_eos_boundary",
			chunks: []string{"[REFERENCE ONLY]some text without double newline"},
			want:   "", // Flush 时 \z 边界触发 scrub，整段丢弃
		},

		// ─── 基础 passthrough / 控制流 ─────────────────────────────────────────
		// case 10: 纯文本 → 原样
		{
			name:   "case10_plain_text_passthrough",
			chunks: []string{"hello world\nthis is plain text"},
			want:   "hello world\nthis is plain text",
		},

		// case 11: 多个连续标签 → 都剥
		{
			name:   "case11_multiple_consecutive_tags",
			chunks: []string{`<memory data-internal="true">a</memory>middle<context data-internal="true">b</context>tail`},
			want:   "middletail",
		},

		// case 12: 嵌套标签（外层带 data-internal）→ 整段剥
		{
			name:   "case12_nested_tags",
			chunks: []string{`<memory data-internal="true">outer<context data-internal="true">inner</context>more</memory>foo`},
			want:   "foo",
		},

		// case 13: 未闭合标签流结束 → Flush 返回 ""
		{
			name:   "case13_unclosed_tag_flush_empty",
			chunks: []string{`prefix<memory data-internal="true">unclosed content`},
			want:   "prefix",
		},

		// case 14（separately tested below: TestStreamScrubber_BufferOverflow）

		// case 15: 空 chunk / Push("") → 不出错，原样
		{
			name:   "case15_empty_chunks",
			chunks: []string{"", "hello", "", "world", ""},
			want:   "helloworld",
		},

		// ─── Inline marker ─────────────────────────────────────────────────────
		// case 16: [Personal Memory: xxx] inline → 剥
		{
			name:   "case16_inline_personal_memory",
			chunks: []string{"before [Personal Memory: user is impatient] after"},
			want:   "before  after",
		},

		// case 17: <system-reminder>x</system-reminder> → 剥（无需 data-internal）
		{
			name:   "case17_system_reminder_always_scrub",
			chunks: []string{`info<system-reminder>internal note</system-reminder>more`},
			want:   "infomore",
		},

		// case 18: <persisted-output ref="abc"/> self-closing → 剥
		{
			name:   "case18_persisted_output_self_closing",
			chunks: []string{`see this <persisted-output ref="abc" size="123"/> then continue`},
			want:   "see this  then continue",
		},

		// ─── 额外的回归 case ────────────────────────────────────────────────────
		// inline [Context:xxx]
		{
			name:   "case_inline_context",
			chunks: []string{`prefix [Context: budget mode] suffix`},
			want:   "prefix  suffix",
		},

		// open-tag 跨极端 chunk 拆分（按 1 字符）
		{
			name:   "case_one_byte_chunks",
			chunks: chunkSplit(`<memory data-internal="true">x</memory>z`, 1),
			want:   "z",
		},

		// reference-only 闭标签跨 chunk
		{
			name: "case_reference_only_close_tag_split",
			chunks: []string{
				`<reference-only data-internal="true">body</refer`,
				`ence-only>tail`,
			},
			want: "tail",
		},

		// <persisted-output ...></persisted-output>（spec 边界 case 18 退化变体：
		// 非自闭合形式。由于 persisted-output 是 task 2.2 内部生成、必然 self-closing，
		// scrubber 把任何 <persisted-output ...> 都当 self-closing 处理。退化变体的
		// 显式闭标签会被当作下一段普通文本输出。）
		{
			name:   "case_persisted_output_non_self_closing",
			chunks: []string{`<persisted-output ref="abc">content`},
			want:   "content",
		},

		// 用户裸输入的 [Hello] 不在 inline pattern → 原样
		{
			name:   "case_user_literal_bracket",
			chunks: []string{`hello [Hello world] more`},
			want:   `hello [Hello world] more`,
		},

		// 不在 ScrubTagNames 的 XML 标签 → 原样
		{
			name:   "case_unknown_tag_passthrough",
			chunks: []string{`<foo data-internal="true">bar</foo>baz`},
			want:   `<foo data-internal="true">bar</foo>baz`,
		},

		// 仅 "<" 字符流末尾 → Flush 时 emit
		{
			name:   "case_lone_less_than_at_eos",
			chunks: []string{"text<"},
			want:   "text<",
		},

		// 流中 "<3 emoji" 不是合法 tag → 原样
		{
			name:   "case_invalid_xml_lt_then_gt",
			chunks: []string{`heart <3 emoji`},
			want:   `heart <3 emoji`, // first "<" emit 后 "3 emoji" passthrough
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := NewStreamScrubber()
			var out strings.Builder
			for _, chunk := range tc.chunks {
				out.WriteString(s.Push(chunk))
			}
			out.WriteString(s.Flush())
			assert.Equal(t, tc.want, out.String(),
				"name=%s chunks=%q", tc.name, tc.chunks)
		})
	}
}

// TestStreamScrubber_BufferOverflow 覆盖 spec case 14：buffer 超 MaxBufferSize
// 未闭合 → 降级当普通文本 flush + log warning（warning 通过日志 sink 校验本测试不强
// 检；行为是不 OOM + 至少 emit 出大块内容）。
func TestStreamScrubber_BufferOverflow(t *testing.T) {
	s := NewStreamScrubber()

	// 构造一个 unclosed scrub 段 + 9000 字符 body（超过 MaxBufferSize=8192）。
	chunk := `<memory data-internal="true">` + strings.Repeat("x", 9000)

	pushed := s.Push(chunk)
	flushed := s.Flush()
	combined := pushed + flushed

	// overflow 行为：整段当普通文本 emit（含开标签 + body）。具体边界由实现决定，
	// 关键 invariant：emit 的内容**至少**包含 body 的绝大部分（不是丢弃整段）。
	assert.GreaterOrEqual(t, len(combined), 8000,
		"buffer overflow should flush as plain text, not silently drop")
	assert.Contains(t, combined, "xxxx",
		"emit content should contain the body chars")
}

// TestStreamScrubber_Reset 验证 Reset 复用同一实例处理新 stream。
func TestStreamScrubber_Reset(t *testing.T) {
	s := NewStreamScrubber()

	// 第一轮：处理含 scrub 的 stream。
	out1 := s.Push(`<memory data-internal="true">a</memory>hello`) + s.Flush()
	assert.Equal(t, "hello", out1)

	// Reset 后处理另一段 stream。
	s.Reset()
	out2 := s.Push("plain text") + s.Flush()
	assert.Equal(t, "plain text", out2)

	// 验证 Reset 后未闭合 buffer 不会影响新 stream。
	s.Reset()
	s.Push(`<memory data-internal="true">unclosed`)
	s.Reset()
	out3 := s.Push("fresh") + s.Flush()
	assert.Equal(t, "fresh", out3)
}

// TestStreamScrubber_FastPath 验证纯文本场景 Push 一次扫描即 emit（覆盖
// processOutside 的 fast path 分支）。
func TestStreamScrubber_FastPath(t *testing.T) {
	s := NewStreamScrubber()
	chunk := strings.Repeat("plain text without any markers ", 100)
	out := s.Push(chunk)
	assert.Equal(t, chunk, out, "fast path: no '<' or '[' → direct emit")
	assert.Equal(t, "", s.Flush(), "after fast path, buffer should be empty")
}

// TestStreamScrubber_MultipleScrubsAndPassthroughInterleaved 多次穿插的回归测试。
func TestStreamScrubber_MultipleScrubsAndPassthroughInterleaved(t *testing.T) {
	s := NewStreamScrubber()
	chunks := []string{
		"alpha ",
		`<memory data-internal="true">m1</memory>`,
		" beta ",
		"[Personal Memory: stuff]",
		" gamma ",
		`<system-reminder>note</system-reminder>`,
		" delta ",
		`<persisted-output ref="r1"/>`,
		" epsilon",
	}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(s.Push(c))
	}
	out.WriteString(s.Flush())
	// 所有 scrub 段应剥离，passthrough 段保留。
	assert.Equal(t, "alpha  beta  gamma  delta  epsilon", out.String())
}

// chunkSplit 把 s 按 size 字节切分为多个子串，模拟极端 chunk 拆分。
func chunkSplit(s string, size int) []string {
	if size <= 0 {
		return []string{s}
	}
	var out []string
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}
