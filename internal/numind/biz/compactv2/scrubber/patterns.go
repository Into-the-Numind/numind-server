// Package scrubber 提供 token-level streaming filter，用于过滤掉 LLM stream
// 输出中的内部注入标签（板块 2 task 2.4 的 <reference-only data-internal="true">
// 与板块 3 task-07 的 <memory data-internal="true">），防止泄露给用户。
//
// 核心难点：跨 chunk 边界的标签（例如 "<mem" + "ory>..."）。使用缓冲式状态机
// 在 OUTSIDE / MAYBE_TAG / INSIDE_TAG 三态之间切换，保证单次 Push 返回的文本
// 一定不含任何待 scrub 的内容。
//
// 包定位（D3 平行重做）：本包是独立的子包，不依赖父包 compactv2，原则上可被
// V1 与 V2 共用，但 V1.5 仅在 V2 路径（agent mode）激活。
package scrubber

import "regexp"

// ScrubTagNames 是需要被 scrub 的 XML 标签名集合。
//
// **白名单语义**（D5 决策 + 跨板块约定）：
//   - 板块 3 task-07 注入 memory 时统一加 data-internal="true" 属性
//   - 板块 2 task 2.4 autocompact summary 整段用
//     <reference-only data-internal="true">...</reference-only> 包裹
//   - 用户在 prompt 里裸写 <memory> / <reference-only> 不带 data-internal 属性 → 不 scrub
//
// **例外**（永远 scrub，无需 data-internal）：
//   - system-reminder：系统注入的提示
//   - persisted-output：task 2.2 写盘后的 placeholder（self-closing 形式
//     <persisted-output ref="..."/>）
//
// 详见 spec README §D5 / §D6 与本包 README。
var ScrubTagNames = []string{
	"memory",
	"personal_context",
	"context",
	"reference-only", // D5 task 2.4 autocompact summary 包裹标签
	"system-reminder",
	"persisted-output",
}

// alwaysScrubTags 列出无需 data-internal="true" 属性即可 scrub 的标签。
// 这些标签的开标签在 LLM 输出里出现就一定是内部生成（用户不会裸写）。
var alwaysScrubTags = map[string]bool{
	"system-reminder":  true,
	"persisted-output": true,
}

// requiresDataInternalTags 列出必须带 data-internal="true" 属性才 scrub 的标签。
// 用户可能合法裸写这些标签（例如讨论 memory 含义），所以仅 scrub 内部注入的版本。
var requiresDataInternalTags = map[string]bool{
	"memory":           true,
	"personal_context": true,
	"context":          true,
	"reference-only":   true,
}

// init 在包加载时校验 ScrubTagNames 与两个 map 的一致性，防止未来添加 / 修改
// 标签时只改 slice 忘改 map（导致静默 no-op）。任何不一致直接 panic — 启动期
// 失败比线上漏剥强。
func init() {
	for _, name := range ScrubTagNames {
		_, isAlways := alwaysScrubTags[name]
		_, requires := requiresDataInternalTags[name]
		if isAlways == requires {
			// 必须分入恰好一个 map（XOR 关系）
			panic("scrubber: ScrubTagNames inconsistent with alwaysScrubTags/requiresDataInternalTags for tag=" + name +
				" — please update patterns.go so each tag appears in exactly one bucket")
		}
	}
	if len(alwaysScrubTags)+len(requiresDataInternalTags) != len(ScrubTagNames) {
		panic("scrubber: tag count mismatch — ScrubTagNames must equal alwaysScrubTags + requiresDataInternalTags")
	}
}

// InlineScrubPatterns 是行内 marker（非 XML 格式）的正则表达式。
// 这些 pattern 总是 scrub（spec 明确要求，不需要 data-internal 属性）。
//
// 性能：包级 var + regexp.MustCompile 在包 init 阶段一次性编译，
// 避免每次 Push 重复编译。
var InlineScrubPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\[Personal Memory:[^\]]*\]`),
	regexp.MustCompile(`\[Context:[^\]]*\]`),
}

// BlockScrubPatterns 是 block-level marker 的正则表达式。
//
// 注：[CONTEXT COMPACTION — REFERENCE ONLY] / [REFERENCE ONLY] 文本前缀已被
// D5 决策替换为 XML 标签 <reference-only data-internal="true">。但保留这两条
// 兜底 pattern 处理早期版本数据 / LLM 偶发漏标签的场景。
//
// 边界 `(?:\n\n|\z)`：以 `\n\n` 结束 OR 字符串末尾。
var BlockScrubPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\[CONTEXT COMPACTION — REFERENCE ONLY\].*?(?:\n\n|\z)`),
	regexp.MustCompile(`(?s)\[REFERENCE ONLY\].*?(?:\n\n|\z)`),
}

// openTagRegex 匹配一个完整的 XML 开标签（含可选属性 + 可选 self-closing）。
// 提取出标签名（group 1）、属性串（group 2）以及是否 self-closing（group 3 = "/"）。
//
// 例：
//
//	<memory data-internal="true" id="x">  → name=memory, attrs=` data-internal="true" id="x"`, sc=""
//	<persisted-output ref="abc"/>          → name=persisted-output, attrs=` ref="abc"`, sc="/"
//	<reference-only data-internal="true">  → name=reference-only, attrs=` data-internal="true"`, sc=""
//	<system-reminder>                      → name=system-reminder, attrs="", sc=""
var openTagRegex = regexp.MustCompile(`^<([a-zA-Z][a-zA-Z0-9_-]*)([^>]*?)(/?)>`)

// dataInternalAttrRegex 检测属性串里是否包含 data-internal="true" 或 data-internal='true'。
// 双引号 / 单引号都接受 — 注入侧（板块 2 task 2.4 + 板块 3 task-07）惯用双引号，但
// 单引号在 HTML 属性里也合法；只匹配一种引号格式有安全风险（注入侧未来若改单引号，
// scrubbing 静默失败 → 内部标签泄露用户）。
var dataInternalAttrRegex = regexp.MustCompile(`\bdata-internal\s*=\s*["']true["']`)

// inlineStartPrefixes 用于在 MAYBE_TAG 状态判断 "[xxx" 开头是否可能匹配
// InlineScrubPatterns 或 BlockScrubPatterns。如果 buffer 当前内容是这些前缀的
// **真前缀**（partial match），需要继续缓冲等待更多数据；如果不可能匹配，
// 立刻吐出 "[" 字符回 OUTSIDE。
//
// 维护时与 InlineScrubPatterns / BlockScrubPatterns 同步。
var inlineStartPrefixes = []string{
	"[Personal Memory:",
	"[Context:",
	"[CONTEXT COMPACTION — REFERENCE ONLY]",
	"[REFERENCE ONLY]",
}

// mightMatchInlineOrBlock 判断给定 buffer（必然以 "[" 开头）是否仍有可能在加更多
// 字符后匹配 InlineScrubPatterns / BlockScrubPatterns 之一。
//
// 两种情况返回 true（"还有戏，继续缓冲"）：
//
//  1. buffer 是某 fixed prefix 的真前缀（buffer 还没收齐固定前缀）
//     例：buffer="[Pers"，candidate="[Personal Memory:" → buffer 是 candidate 的 prefix
//
//  2. buffer 完整收到某 fixed prefix，但变量部分（pattern 后半的 `[^\]]*\]` 或
//     `.*?\n\n`）还没收齐 — 也需要继续缓冲
//     例：buffer="[Personal Memory: some content"，candidate="[Personal Memory:"
//     → buffer 以 candidate 开头但还没有 "]"
//
// 否则返回 false（"没戏，emit '[' 字符回 OUTSIDE 继续扫描"）。
func mightMatchInlineOrBlock(buf string) bool {
	for _, c := range inlineStartPrefixes {
		// case 1: buffer 是 candidate 的真前缀（buffer 还没收齐 candidate）
		if len(buf) < len(c) && c[:len(buf)] == buf {
			return true
		}
		// case 2: buffer 已收齐 candidate 作为开头，变量部分可能未到齐
		if len(buf) >= len(c) && buf[:len(c)] == c {
			return true
		}
	}
	return false
}
