package agent

import (
	"numind-server/internal/numind/biz/compactv2/scrubber"
)

// V1.5 板块 2 task 2.5 wiring — Streaming Context Scrubber 接入。
//
// 设计意图：
//   - 防止 LLM 在 final answer 里 echo 内部注入的标签（task 2.4 autocompact 的
//     <reference-only data-internal="true">12 段</reference-only> / 板块 3 memory
//     的 <memory data-internal="true">...</memory> 等）泄露给用户
//   - 也兜底 <system-reminder> / <persisted-output ref=".../>" / [Personal Memory:]
//     / [Context:] 这类非 XML marker
//
// 当前接入点：runner.go 拿到 LLM 的 final answer (output.Content) 之后、写入
// agent_run.messages 之前。Eino 在本项目里用同步 Generate（非 stream），所以
// scrubber 的"streaming"语义降级为单次 Push + Flush — API 设计天然兼容。
//
// 性能：
//   - 用户 90%+ 回答不会含 `<` 或 `[` → scrubber fast path 单次 O(n) 扫描 + 透传
//   - 内部 panic + recover 由 scrubber 自身的状态机 + buffer 上限 8KB 兜底
//
// V1/V2 都过 scrubber：scrubber 本身与 compactv2 主流程解耦，对 V1 路径只有
// "echo 出来的奇怪标签会被吞掉"这一个表现差异；用户裸写不带 data-internal 属性的
// 同名标签不剥（白名单约定，已单测覆盖 18 个 case）。

// scrubFinalAnswer 是 runner.go 调用的薄包装，便于单测在不构造 Scrubber 时直接验证
// runner 行为。每次调用 new 一个 scrubber 实例（无共享状态），并发安全。
func scrubFinalAnswer(raw string) string {
	if raw == "" {
		return raw
	}
	s := scrubber.NewStreamScrubber()
	out := s.Push(raw)
	tail := s.Flush()
	if tail == "" {
		return out
	}
	if out == "" {
		return tail
	}
	return out + tail
}
