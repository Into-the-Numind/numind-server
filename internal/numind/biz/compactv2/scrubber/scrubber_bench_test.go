package scrubber

import (
	"strings"
	"testing"
)

// BenchmarkScrubber_FastPath 测纯文本场景（无 "<" 也无 "["，走 processOutside
// fast path 一次扫描即 emit）。
//
// 目标：< 5 μs/op。
func BenchmarkScrubber_FastPath(b *testing.B) {
	chunk := strings.Repeat("hello world, this is plain text without markers. ", 20) // ~1 KB

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s := NewStreamScrubber()
		_ = s.Push(chunk)
		_ = s.Flush()
	}
}

// BenchmarkScrubber_SingleTag 测含一个完整 scrub 标签的 1KB chunk
// （走 processMaybeTagXML + processInsideTag）。
//
// 目标：< 50 μs/op。
func BenchmarkScrubber_SingleTag(b *testing.B) {
	body := strings.Repeat("memory body content ", 40) // ~800 byte
	chunk := `<memory data-internal="true">` + body + `</memory>` +
		"trailing plain text portion to test the post-scrub emit path " +
		strings.Repeat("x", 100)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s := NewStreamScrubber()
		_ = s.Push(chunk)
		_ = s.Flush()
	}
}

// BenchmarkScrubber_CrossChunk 测最坏 case：单个 scrub 标签被拆成 10 个 chunk
// （每个 chunk 触发一次状态机循环）。
//
// 目标：< 200 μs/op。
func BenchmarkScrubber_CrossChunk(b *testing.B) {
	body := strings.Repeat("memory body content ", 40)
	full := `<memory data-internal="true">` + body + `</memory>tail`
	chunks := chunkSplit(full, len(full)/10) // 10 个 chunk

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s := NewStreamScrubber()
		for _, c := range chunks {
			_ = s.Push(c)
		}
		_ = s.Flush()
	}
}

// BenchmarkScrubber_OneBytePerChunk 测极端拆分（每 chunk 1 byte）—— 这是 spec
// §风险 §4「性能 vs 准确性」提到的最坏 case。无硬性 SLA，但作为基准记录。
func BenchmarkScrubber_OneBytePerChunk(b *testing.B) {
	full := `<memory data-internal="true">x</memory>tail`
	chunks := chunkSplit(full, 1)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s := NewStreamScrubber()
		for _, c := range chunks {
			_ = s.Push(c)
		}
		_ = s.Flush()
	}
}
