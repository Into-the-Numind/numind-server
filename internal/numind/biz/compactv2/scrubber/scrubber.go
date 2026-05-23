package scrubber

import (
	"strings"

	"numind-server/internal/pkg/log"
)

// scrubberState 是 StreamScrubber 的三态状态机标识。
type scrubberState int

const (
	// stateOutside：当前 buffer 前缀不在任何 scrub 段内。扫描下一个 "<" 或 "["
	// 作为可能的 scrub 起点。
	stateOutside scrubberState = iota

	// stateMaybeTag：buffer 以 "<" 或 "[" 开头但尚未确认是否真的需要 scrub。
	// 需要更多字符来匹配 openTagRegex / inline pattern / block pattern 决定。
	stateMaybeTag

	// stateInsideTag：已确认进入 scrub 段（XML 开标签匹配 + 白名单通过）。
	// 持续缓冲直到见到 </tagName>，整段丢弃。
	stateInsideTag
)

const (
	// MaxBufferSize 是 buffer 上限（字节）。
	//
	// 设计动机：防恶意输入构造无闭合标签把 buffer 撑爆。超过此值时：
	//   - stateInsideTag：当前在 scrub 段内 → 整段 buffer 当普通文本 flush + warn log
	//     （视为 LLM 异常输出，宁可泄露也不 OOM）
	//   - stateMaybeTag：当前在悬而未决标签里 → 同样 flush + warn log
	//
	// 取 8KB 与 spec §核心算法 §关键参数 表保持一致。
	MaxBufferSize = 8 * 1024

	// ShortCircuitThreshold 是短输出 short-circuit 的建议阈值（仅文档值，不被
	// scrubber 直接使用）。caller 可判断 stream 总长 < 5KB 时跳过 scrubber，
	// stream 收完后一次性 regex.ReplaceAll。本包不强制此优化。
	ShortCircuitThreshold = 5 * 1024
)

// StreamScrubber 是 token-level streaming filter，按 chunk 接收 LLM 输出，
// 在 LLM stream pipeline 与前端 emit 之间起到"漏斗"作用：
//
//	chunk → Push() → 安全文本（可能为 ""）→ 前端
//	stream 结束 → Flush() → buffer 剩余的安全文本 → 前端
//
// **关键不变式**：单次 Push 返回的文本一定不含任何待 scrub 的内容。未决定的
// 尾部留在 buffer 等下次 Push / Flush。
//
// **并发性**：本类型非线程安全。每个 stream 实例独占一个 StreamScrubber。
//
// 详见 spec task-05 §设计要点 §算法。
type StreamScrubber struct {
	// buffer 缓存当前 chunk 还没有决定要 emit / drop 的尾部。
	// 不变式：buffer 永远以 "<" 或 "[" 开头（stateMaybeTag / stateInsideTag）
	// 或为空（stateOutside 扫到末尾）。
	buffer strings.Builder

	// state 是当前状态机标识。
	state scrubberState

	// tagName 在 stateInsideTag 时记录当前 scrub 段的标签名，
	// 用于查找对应的 </tagName> 闭合。
	tagName string
}

// NewStreamScrubber 构造一个新 scrubber。每个 stream 一个实例。
func NewStreamScrubber() *StreamScrubber {
	return &StreamScrubber{state: stateOutside}
}

// Reset 清空 scrubber 状态。允许复用同一实例处理新 stream（避免重新分配）。
func (s *StreamScrubber) Reset() {
	s.buffer.Reset()
	s.state = stateOutside
	s.tagName = ""
}

// Push 接收一个 chunk，返回应输出给前端的安全文本（可能为 ""）。
//
// 内部可能保留尾部到 buffer 等待下一个 chunk 拼合（跨 chunk 标签场景）。
// 空 chunk 也安全：直接返回当前 buffer 中已确定可 emit 的部分（通常为 ""）。
func (s *StreamScrubber) Push(chunk string) string {
	if chunk != "" {
		s.buffer.WriteString(chunk)
	}
	return s.process(false)
}

// Flush 流结束时调用，返回 buffer 中剩余的安全文本。
//
// 行为约定：
//   - stateOutside：剩余 buffer 全部 emit（可能含未完成的悬空 "<" / "["，
//     这是合法的——LLM 真的就输出了这些字符 EOF）
//   - stateMaybeTag：buffer 不可能匹配任何 scrub pattern（否则会切到
//     stateInsideTag）→ 安全 emit
//   - stateInsideTag：未闭合 scrub 段流结束 → 整段丢弃（spec 边界 case 13）
func (s *StreamScrubber) Flush() string {
	return s.process(true)
}

// process 是核心状态机驱动循环。
//
// terminal=true 表示 Flush 调用（流结束）：MAYBE_TAG 状态下不再等待更多数据，
// 把无法匹配 pattern 的 buffer 安全 emit；INSIDE_TAG 状态丢弃 buffer。
func (s *StreamScrubber) process(terminal bool) string {
	var out strings.Builder

	for {
		buf := s.buffer.String()

		switch s.state {
		case stateOutside:
			emitted, consumed, transition := processOutside(buf)
			out.WriteString(emitted)
			s.advance(consumed)
			if !transition {
				// 扫到末尾仍 OUTSIDE，buffer 已全部 emit → 退出。
				return out.String()
			}
			// 切换到 MAYBE_TAG，循环继续。
			s.state = stateMaybeTag

		case stateMaybeTag:
			emitted, consumed, advance, newState, newTag := s.processMaybeTag(buf, terminal)
			out.WriteString(emitted)
			s.advance(consumed)
			s.state = newState
			s.tagName = newTag
			if !advance {
				// 数据不足以决策。**Buffer overflow 保护**：若 buffer 已撑到
				// MaxBufferSize 仍无法决策（极端 LLM 异常输出 / 攻击载荷），
				// 降级当普通文本 flush + warn log，宁可泄露也不 OOM。
				if len(buf) > MaxBufferSize {
					s.flushOverflow(&out, buf, stateMaybeTag)
					return out.String()
				}
				// 否则等下一次 Push。
				return out.String()
			}
			// 否则循环继续（已切到 OUTSIDE 或 INSIDE_TAG）。

		case stateInsideTag:
			consumed, advance := s.processInsideTag(buf, terminal)
			s.advance(consumed)
			if !advance {
				// 同上的 buffer overflow 保护，作用于 INSIDE_TAG 卡死场景
				// （持续等待 </tagName> 永不到来）。
				if len(buf) > MaxBufferSize {
					s.flushOverflow(&out, buf, stateInsideTag)
					return out.String()
				}
				return out.String()
			}
			s.state = stateOutside
			s.tagName = ""
			// 循环继续：可能 buffer 里有后续内容要处理。
		}
	}
}

// flushOverflow 在 buffer overflow 时把 buffer 全部当普通文本 emit + 打 warning
// + 重置 scrubber 状态到 OUTSIDE。
func (s *StreamScrubber) flushOverflow(out *strings.Builder, buf string, st scrubberState) {
	log.Warnw("scrubber buffer overflow, flushing as plain text",
		"buffer_size", len(buf),
		"state", st,
		"tag_name", s.tagName,
	)
	out.WriteString(buf)
	s.buffer.Reset()
	s.state = stateOutside
	s.tagName = ""
}

// advance 从 buffer 头部消耗 n 个字节。
//
// 实现注意：strings.Builder 不支持随机访问写入。这里通过 String() + Reset() +
// WriteString() 重写尾部。这是 O(buffer_size) 的操作，但因为 buffer 上限 8KB
// 且每次 chunk 处理不会重复调用太多次，可接受。
func (s *StreamScrubber) advance(n int) {
	if n <= 0 {
		return
	}
	buf := s.buffer.String()
	if n >= len(buf) {
		s.buffer.Reset()
		return
	}
	rest := buf[n:]
	s.buffer.Reset()
	s.buffer.WriteString(rest)
}

// processOutside 在 OUTSIDE 状态下扫描 buffer，找下一个可能的 scrub 起点
// （"<" 或 "["）。
//
// 返回：
//   - emitted：可以立刻吐出去给前端的安全文本
//   - consumed：从 buffer 消耗多少字节（emit + 跳过的部分）
//   - transition：是否切换到 MAYBE_TAG 状态（true = "<" 或 "[" 出现，
//     true 时不在本函数切换 state，由 caller 切换）
//
// **Fast path**：若 buffer 完全不含 "<" / "[" → 全部 emit + transition=false。
// 这是常见情况（纯文本），O(n) 单次扫描即返回。
//
// 纯函数（无 receiver），不修改 scrubber 状态——状态切换由 caller 负责。
func processOutside(buf string) (emitted string, consumed int, transition bool) {
	// 同时找 "<" 和 "[" 的最早位置。
	idxLT := strings.IndexByte(buf, '<')
	idxLB := strings.IndexByte(buf, '[')

	var idx int
	switch {
	case idxLT < 0 && idxLB < 0:
		// 都没找到 → fast path，全部 emit。
		return buf, len(buf), false
	case idxLT < 0:
		idx = idxLB
	case idxLB < 0:
		idx = idxLT
	default:
		if idxLT < idxLB {
			idx = idxLT
		} else {
			idx = idxLB
		}
	}

	// emit "<"/"[" 之前的纯文本，提示 caller 切到 MAYBE_TAG（"<"/"[" 留在 buffer）。
	emitted = buf[:idx]
	return emitted, idx, true
}

// processMaybeTag 在 MAYBE_TAG 状态下尝试识别 buffer 头部的 marker。
//
// 返回：
//   - emitted：决定不 scrub 时，emit 的字符（例如非匹配的 <foo> 当普通文本）
//   - consumed：从 buffer 消耗多少字节
//   - advance：是否完成此次决策（true = 已切到 OUTSIDE 或 INSIDE_TAG，可继续；
//     false = 数据不足，需要等下一次 Push）
//   - newState：切换后的状态
//   - newTag：若切到 INSIDE_TAG，记录的标签名
//
// terminal=true（Flush）：数据不足时也要做最终决策——不可能匹配的当普通文本 emit。
func (s *StreamScrubber) processMaybeTag(buf string, terminal bool) (
	emitted string, consumed int, advance bool, newState scrubberState, newTag string,
) {
	if buf == "" {
		// 不可能出现：MAYBE_TAG 时 buffer 至少有 "<" 或 "["。保险归位。
		return "", 0, true, stateOutside, ""
	}

	first := buf[0]
	switch first {
	case '<':
		return s.processMaybeTagXML(buf, terminal)
	case '[':
		return s.processMaybeTagBracket(buf, terminal)
	default:
		// 不可能出现：MAYBE_TAG 一定从 "<" 或 "[" 开始。保险归位。
		return string(first), 1, true, stateOutside, ""
	}
}

// processMaybeTagXML 处理 buffer 以 "<" 开头的 MAYBE_TAG 决策。
func (s *StreamScrubber) processMaybeTagXML(buf string, terminal bool) (
	emitted string, consumed int, advance bool, newState scrubberState, newTag string,
) {
	// 尝试匹配完整开标签 <tag attrs...> 或 <tag attrs.../>
	loc := openTagRegex.FindStringSubmatchIndex(buf)
	if loc == nil {
		// 还没收到 ">" → 看是否可能是一个未完成的开标签。
		// 若 buffer 含 ">" 但 regex 不匹配 → 这"<...>"不是合法标签格式，
		// 当普通文本 emit。
		gt := strings.IndexByte(buf, '>')
		if gt >= 0 {
			// 异常 "<" 开头但不是合法 tag 格式（例如 "<3" / "<= " / "<<a>"）。
			// 安全做法：emit 第一个 "<" 字符，回 OUTSIDE 重新扫描剩余。
			return "<", 1, true, stateOutside, ""
		}
		// 还没看到 ">"：
		//   - terminal=false：buffer 可能是 "<m" / "<memo" 等 partial，等下一次 Push。
		//   - terminal=true（Flush）：永远拿不到 ">" 了，直接 emit buffer 当普通文本。
		if terminal {
			emitted = buf
			return emitted, len(buf), true, stateOutside, ""
		}
		return "", 0, false, stateMaybeTag, ""
	}

	// loc 是完整开标签的匹配位置 [start, end, name_start, name_end, attrs_start, attrs_end, sc_start, sc_end]
	// regex 锚 ^，start 一定是 0。
	tagFullEnd := loc[1]
	tagName := buf[loc[2]:loc[3]]
	attrs := ""
	if loc[4] >= 0 {
		attrs = buf[loc[4]:loc[5]]
	}
	selfClose := false
	if loc[6] >= 0 && loc[7] > loc[6] {
		selfClose = (buf[loc[6]:loc[7]] == "/")
	}

	// 1. 检查标签名是否在 scrub 集合里。
	shouldScrub := false
	if alwaysScrubTags[tagName] {
		shouldScrub = true
	} else if requiresDataInternalTags[tagName] {
		shouldScrub = dataInternalAttrRegex.MatchString(attrs)
	}

	if !shouldScrub {
		// 不需要 scrub 的标签 → 当普通文本 emit 整个开标签 + 回 OUTSIDE。
		return buf[:tagFullEnd], tagFullEnd, true, stateOutside, ""
	}

	// 2. 需要 scrub。
	if selfClose {
		// <persisted-output .../>：自闭合，无 INSIDE_TAG 内容，直接消耗整个开
		// 标签 + 回 OUTSIDE。
		return "", tagFullEnd, true, stateOutside, ""
	}

	// **特殊处理 persisted-output**：task 2.2 写盘后 placeholder 一定是
	// 自闭合形式 <persisted-output ref="..."/>，但 spec 边界 case 18 指出
	// 也可能出现非自闭合的退化形式 `<persisted-output ref="abc">`。无论哪种
	// 形式都不应该等待 </persisted-output>（task 2.2 不会生成闭合标签），
	// 直接当 self-closing 处理，丢弃开标签后回 OUTSIDE。
	if tagName == "persisted-output" {
		return "", tagFullEnd, true, stateOutside, ""
	}

	// 否则切到 INSIDE_TAG，记录 tagName，从开标签后开始等闭标签。
	return "", tagFullEnd, true, stateInsideTag, tagName
}

// processMaybeTagBracket 处理 buffer 以 "[" 开头的 MAYBE_TAG 决策。
//
// 测试顺序：inline patterns（[Personal Memory:…] / [Context:…]）→ block
// patterns（[CONTEXT COMPACTION — REFERENCE ONLY]…\n\n / [REFERENCE ONLY]…\n\n）。
//
// 部分匹配判定：若 buffer 是某 pattern 的真前缀（例如 "[Pers"），需要继续缓冲。
func (s *StreamScrubber) processMaybeTagBracket(buf string, terminal bool) (
	emitted string, consumed int, advance bool, newState scrubberState, newTag string,
) {
	// 1. 尝试 inline patterns。
	for _, re := range InlineScrubPatterns {
		if loc := re.FindStringIndex(buf); loc != nil && loc[0] == 0 {
			// 匹配整段 inline marker，全部丢弃。
			return "", loc[1], true, stateOutside, ""
		}
	}

	// 2. 尝试 block patterns。
	for _, re := range BlockScrubPatterns {
		if loc := re.FindStringIndex(buf); loc != nil && loc[0] == 0 {
			// regex 的 `(?:\n\n|\z)` 用 \z（字符串末尾）兜底。**但在流式场景下**
			// "\z 边界" 只有在 terminal=true 时才安全（Flush 时 buffer 真到末尾）。
			// 非 terminal 时如果只匹配到 \z 而没真正 \n\n，要继续等更多数据看
			// 是否后续有 \n\n。
			if !terminal && !strings.Contains(buf[:loc[1]], "\n\n") {
				// 仅靠 \z 兜底匹配，buffer 不含 \n\n → 可能后续还会扩张匹配，
				// 等下一次 Push。
				return "", 0, false, stateMaybeTag, ""
			}
			return "", loc[1], true, stateOutside, ""
		}
	}

	// 3. Partial-prefix check：buffer 是否还有可能在加更多字符后匹配？
	if mightMatchInlineOrBlock(buf) {
		if terminal {
			// 流结束仍无法补齐 → 当普通文本 emit。
			return buf, len(buf), true, stateOutside, ""
		}
		return "", 0, false, stateMaybeTag, ""
	}

	// 4. 完全不匹配任何 pattern → emit 第一个 "[" 字符，回 OUTSIDE 继续扫描。
	return "[", 1, true, stateOutside, ""
}

// processInsideTag 在 INSIDE_TAG 状态下扫描闭合 </tagName>。
//
// 返回：
//   - consumed：从 buffer 消耗多少字节（找到闭标签时 = 闭标签结束位置；否则 = 0 或 len(buf)）
//   - advance：是否完成此次决策（true = 找到闭标签可继续；false = 等下一次 Push）
//
// terminal=true（Flush）：闭标签未到 → 整段 buffer 丢弃（spec 边界 case 13）。
func (s *StreamScrubber) processInsideTag(buf string, terminal bool) (consumed int, advance bool) {
	closeTag := "</" + s.tagName + ">"
	idx := strings.Index(buf, closeTag)
	if idx < 0 {
		// 还没收到闭标签：
		//   - terminal=false：等下一次 Push（buffer 全部保留）。
		//   - terminal=true（Flush）：未闭合 scrub 段流结束 → 丢弃整个 buffer。
		if terminal {
			return len(buf), true
		}
		return 0, false
	}
	// 找到闭标签，消耗到闭标签末尾（包含闭标签本身）。
	return idx + len(closeTag), true
}
