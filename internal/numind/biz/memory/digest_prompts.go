package memory

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/datatypes"

	"numind-server/internal/pkg/model"
)

// ─── Daily digest prompt ─────────────────────────────────────────────────────

// SessionBrief is one digest input slice — a compact representation of an
// agent_run distilled to two messages (first user / last assistant).
//
// The cron generator constructs these from agent_run.messages JSON; each
// content field is truncated to digestSessionContentRunes to bound prompt
// size (200 chinese chars ≈ 600 tokens; 5 sessions × 2 msgs × 600 ≈ 6K tokens —
// well under qwen-plus 128K).
type SessionBrief struct {
	SessionID string `json:"session_id"`
	StartedAt string `json:"started_at"` // formatted "HH:MM"
	FirstUser string `json:"first_user"`
	LastAssis string `json:"last_assistant"`
}

// digestSessionContentRunes is the per-message rune cap (truncation guards
// against pathological tool-result blobs blowing up the prompt).
const digestSessionContentRunes = 200

// dailyDigestPromptTemplate is the user-side prompt for the daily digest LLM call.
// Output is strict JSON: {"summary": "...", "key_topics": [...]} — no markdown
// fence, no prose. fallback summary "（无 substantive 活动）" is documented in
// the prompt so the LLM uses the exact string we expect on zero-fact days.
const dailyDigestPromptTemplate = `你是用户活动总结器。请基于以下元数据 + 会话摘要, 为该用户生成当日 agent mode 活动总结.

【元数据】
- 日期: %s (Asia/Shanghai)
- agent_run 数: %d
- messages 总数: %d
- 当日新增 facts: %d

【会话摘要 (按时间序)】
%s

【要求】
1. 100-200 字总结 (第三人称 "用户"); 不堆砌 session 流水, 提炼"用户做了什么"
2. 3-5 个关键主题 (每个 ≤ 6 字)
3. 严格 JSON 输出: {"summary":"...","key_topics":["...","..."]}
4. 无 substantive 活动 (全 trivial / 全报错 / 内容空) → summary="（无 substantive 活动）", key_topics=[]
5. 不要 markdown 围栏, 不要解释, 不要 prose. 直接输出 JSON.`

// BuildDailyDigestPrompt assembles the daily digest user prompt.
func BuildDailyDigestPrompt(
	date time.Time,
	runCount, messageCount, factsCount int,
	sessions []SessionBrief,
) string {
	dateStr := date.Format("2006-01-02")
	sessionsStr := formatSessionsBrief(sessions)
	return fmt.Sprintf(dailyDigestPromptTemplate,
		dateStr, runCount, messageCount, factsCount, sessionsStr)
}

// formatSessionsBrief renders SessionBrief slice into a human-readable bullet list.
// Each session: `- HH:MM session<sessionID-prefix>: 用户问"…" → 助手"…"`
func formatSessionsBrief(sessions []SessionBrief) string {
	if len(sessions) == 0 {
		return "(当日无 substantive 会话)"
	}
	var b strings.Builder
	b.Grow(len(sessions) * 256)
	for i, s := range sessions {
		if i > 0 {
			b.WriteString("\n")
		}
		// Session ID shown as 6-char prefix to keep prompt compact.
		sidPrefix := s.SessionID
		if len(sidPrefix) > 6 {
			sidPrefix = sidPrefix[:6]
		}
		b.WriteString("- ")
		b.WriteString(s.StartedAt)
		b.WriteString(" sess[")
		b.WriteString(sidPrefix)
		b.WriteString("]: 用户问\"")
		b.WriteString(truncateRunes(s.FirstUser, digestSessionContentRunes))
		b.WriteString("\" → 助手\"")
		b.WriteString(truncateRunes(s.LastAssis, digestSessionContentRunes))
		b.WriteString("\"")
	}
	return b.String()
}

// ─── Weekly / Monthly / Quarterly prompts ────────────────────────────────────
//
// Higher-level granularities take the lower-level digest list as input (not raw
// messages). Same JSON output contract; same fallback semantics.

const aggregateDigestPromptTemplate = `你是%s总结器. 下面是该用户的多个%s总结. 综合归纳出"主线".

【输入 digest 列表】
%s

【要求】
1. 200-300 字综合归纳 (第三人称 "用户"); 突出: 模式 / 趋势 / 重要客户 / 关键决策
2. 5-10 个关键主题 (每个 ≤ 6 字)
3. 严格 JSON 输出: {"summary":"...","key_topics":["...","..."]}
4. 输入全为"无活动" / 空内容 → summary="（本%s无 substantive 活动）", key_topics=[]
5. 不要 markdown 围栏, 不要解释, 不要 prose. 直接输出 JSON.`

// lowerDigestItem is a single entry in the aggregate prompt — abstracts over
// daily / weekly / monthly digest rows to a uniform {label, summary, topics} tuple.
type lowerDigestItem struct {
	Label   string // "2026-05-22" / "W21" / "May"
	Summary string
	Topics  []string
}

// BuildWeeklyDigestPrompt builds the weekly aggregation prompt.
func BuildWeeklyDigestPrompt(items []lowerDigestItem) string {
	return buildAggregatePrompt("周", "日级", "周", items)
}

// BuildMonthlyDigestPrompt builds the monthly aggregation prompt.
func BuildMonthlyDigestPrompt(items []lowerDigestItem) string {
	return buildAggregatePrompt("月", "周级", "月", items)
}

// BuildQuarterlyDigestPrompt builds the quarterly aggregation prompt.
func BuildQuarterlyDigestPrompt(items []lowerDigestItem) string {
	return buildAggregatePrompt("季", "月级", "季", items)
}

func buildAggregatePrompt(role, lowerName, fallbackLabel string, items []lowerDigestItem) string {
	listStr := formatLowerDigestList(items)
	return fmt.Sprintf(aggregateDigestPromptTemplate, role, lowerName, listStr, fallbackLabel)
}

func formatLowerDigestList(items []lowerDigestItem) string {
	if len(items) == 0 {
		return "(无下层 digest 数据)"
	}
	var b strings.Builder
	b.Grow(len(items) * 200)
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("[")
		b.WriteString(it.Label)
		b.WriteString("] ")
		b.WriteString(strings.TrimSpace(it.Summary))
		if len(it.Topics) > 0 {
			b.WriteString("\n  关键主题: ")
			b.WriteString(strings.Join(it.Topics, ", "))
		}
	}
	return b.String()
}

// ─── LLM-output parser ───────────────────────────────────────────────────────

// digestLLMOutput mirrors the JSON shape we ask the LLM to produce.
type digestLLMOutput struct {
	Summary   string   `json:"summary"`
	KeyTopics []string `json:"key_topics"`
}

// digestParseFallback is the canned text used when the LLM returns malformed
// JSON twice in a row. Kept short + recognisable so admins can grep logs.
const digestParseFallback = "（LLM 解析失败）"

// parseDigestLLMOutput attempts to parse the LLM response into a digestLLMOutput.
// Tolerant of common LLM glitches:
//   - leading/trailing whitespace
//   - markdown fence (```json ... ```) — stripped before unmarshal
//   - leading/trailing prose surrounding a valid {} block — extracts the
//     largest balanced JSON object
//
// Returns the parsed output + nil on success; an empty (zero-value) output +
// error on parse failure. Callers should retry on error, then fall back to
// digestParseFallback after the second attempt fails.
func parseDigestLLMOutput(raw string) (digestLLMOutput, error) {
	if raw == "" {
		return digestLLMOutput{}, fmt.Errorf("empty LLM output")
	}
	cleaned := stripMarkdownFenceAndProse(raw)
	var out digestLLMOutput
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return digestLLMOutput{}, fmt.Errorf("json.Unmarshal: %w", err)
	}
	if out.KeyTopics == nil {
		out.KeyTopics = []string{} // never nil so JSON-encoding the model never produces null
	}
	return out, nil
}

// stripMarkdownFenceAndProse extracts the first balanced top-level {…} block.
// If no balanced block found, returns the input trimmed.
func stripMarkdownFenceAndProse(raw string) string {
	s := strings.TrimSpace(raw)
	// Drop ```json … ``` fence pair if present.
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i > 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j > 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	// Find first balanced {…} block.
	start := strings.Index(s, "{")
	if start < 0 {
		return s
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s
}

// digestFallbackOutput returns a digestLLMOutput populated with the
// digestParseFallback summary + empty key_topics. Used when both LLM attempts
// fail to parse.
func digestFallbackOutput() digestLLMOutput {
	return digestLLMOutput{
		Summary:   digestParseFallback,
		KeyTopics: []string{},
	}
}

// keyTopicsToJSON marshals the parsed []string into datatypes.JSON for DB write.
// Always emits a JSON array — never null — so downstream JSON parsing is safe.
func keyTopicsToJSON(topics []string) datatypes.JSON {
	if topics == nil {
		topics = []string{}
	}
	b, err := json.Marshal(topics)
	if err != nil {
		// Should never fail for []string; defensive fallback to empty array.
		return datatypes.JSON([]byte("[]"))
	}
	return datatypes.JSON(b)
}

// parseKeyTopicsList unmarshals datatypes.JSON back to []string for display
// (called by temporal block formatter). Returns empty slice on any parse error.
func parseKeyTopicsList(j datatypes.JSON) []string {
	if len(j) == 0 || string(j) == "null" || string(j) == "[]" {
		return nil
	}
	var topics []string
	if err := json.Unmarshal(j, &topics); err != nil {
		return nil
	}
	return topics
}

// ─── Compaction helpers for daily-prompt building ────────────────────────────

// sessionsFromAgentRuns derives []SessionBrief from a slice of agent_run rows.
// For each run:
//   - extract first user message + last assistant message from messages JSON
//   - truncate both to digestSessionContentRunes
//   - format started_at as "HH:MM" in Asia/Shanghai
//
// Runs with no parseable messages are silently dropped (don't pollute prompt).
func sessionsFromAgentRuns(runs []*model.AgentRun) []SessionBrief {
	if len(runs) == 0 {
		return nil
	}
	out := make([]SessionBrief, 0, len(runs))
	for _, r := range runs {
		if r == nil {
			continue
		}
		first, last, ok := extractFirstUserLastAssistant(r.Messages)
		if !ok {
			continue // skip runs with no usable content
		}
		startedAt := r.StartedAt.In(shanghaiLoc).Format("15:04")
		out = append(out, SessionBrief{
			SessionID: r.SessionID,
			StartedAt: startedAt,
			FirstUser: first,
			LastAssis: last,
		})
	}
	return out
}

// extractFirstUserLastAssistant unmarshals agent_run.messages JSON and returns
// (first user content, last assistant content, ok). Tolerant of:
//   - missing role field (assumed "user")
//   - empty content
//   - non-array JSON (returns ok=false)
//
// The MessageContent field is a string for the wire-level run.messages array
// (per agent_run.messages format documented in CLAUDE.md). We extract just the
// "content" key as a string; if it's an object/array, we fall through.
func extractFirstUserLastAssistant(raw datatypes.JSON) (first, last string, ok bool) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		return "", "", false
	}
	var msgs []map[string]any
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return "", "", false
	}
	for _, m := range msgs {
		role, _ := m["role"].(string)
		content := stringifyContent(m["content"])
		if content == "" {
			continue
		}
		switch role {
		case "", "user":
			if first == "" {
				first = content
			}
		case "assistant":
			last = content
		}
	}
	if first == "" && last == "" {
		return "", "", false
	}
	return first, last, true
}

// stringifyContent best-effort renders a messages[].content field into a string.
// Handles: plain string, missing/nil, []any (multi-part) by joining .text fields.
func stringifyContent(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, part := range c {
			if pm, ok := part.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok && t != "" {
					if b.Len() > 0 {
						b.WriteString(" ")
					}
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}
