package langfuse

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TraceID 生成新的 trace ID
func TraceID() string {
	return uuid.New().String()
}

// SpanID 生成新的 span/observation ID
func SpanID() string {
	return uuid.New().String()
}

// --- Trace ---

// CreateTrace 创建 Trace 事件
func CreateTrace(traceID, name string, opts ...TraceOption) {
	if C == nil || !C.enabled {
		return
	}
	body := &TraceBody{
		ID:        traceID,
		Name:      name,
		Timestamp: time.Now(),
	}
	for _, opt := range opts {
		opt(body)
	}
	C.Enqueue(&IngestionEvent{
		ID:   uuid.New().String(),
		Type: "trace-create",
		Body: body,
		Time: time.Now(),
	})
}

// TraceOption Trace 配置选项
type TraceOption func(*TraceBody)

// WithUserID 设置 trace 的 user ID
func WithUserID(id uint) TraceOption {
	return func(t *TraceBody) {
		t.UserID = fmt.Sprintf("%d", id)
	}
}

// WithSessionID 设置 trace 的 session ID
func WithSessionID(id string) TraceOption {
	return func(t *TraceBody) {
		t.SessionID = id
	}
}

// WithTraceInput 设置 trace 输入
func WithTraceInput(input interface{}) TraceOption {
	return func(t *TraceBody) {
		t.Input = input
	}
}

// WithTraceOutput 设置 trace 输出
func WithTraceOutput(output interface{}) TraceOption {
	return func(t *TraceBody) {
		t.Output = output
	}
}

// WithTraceMeta 设置 trace 元数据
func WithTraceMeta(meta map[string]string) TraceOption {
	return func(t *TraceBody) {
		t.Metadata = meta
	}
}

// WithTraceTags 设置 trace 标签
func WithTraceTags(tags ...string) TraceOption {
	return func(t *TraceBody) {
		t.Tags = tags
	}
}

// UpdateTraceMetadata 追加 / 更新 trace-level metadata after creation.
// Langfuse's ingestion API accepts a "trace-create" event for the same trace
// ID twice; the second call merges fields. We re-use the same event type so
// downstream processing is unchanged.
//
// Used by credits-system Task C.8 (spec §5.1.5) to stamp billing_mode,
// deducted_from, and credit_balance_at_start onto the existing SOP/SalesRAG
// trace root WITHOUT requiring the original trace-create call site to know
// about credits.
func UpdateTraceMetadata(traceID string, metadata map[string]string) {
	if C == nil || !C.enabled || traceID == "" || len(metadata) == 0 {
		return
	}
	body := &TraceBody{
		ID:        traceID,
		Metadata:  metadata,
		Timestamp: time.Now(),
	}
	C.Enqueue(&IngestionEvent{
		ID:   uuid.New().String(),
		Type: "trace-create", // Langfuse merges same-ID traces; see API docs.
		Body: body,
		Time: time.Now(),
	})
}

// --- Span ---

// CreateSpan 创建 Span 事件
func CreateSpan(traceID, spanID, name string, opts ...SpanOption) {
	if C == nil || !C.enabled {
		return
	}
	body := &SpanBody{
		ID:        spanID,
		TraceID:   traceID,
		Name:      name,
		StartTime: time.Now(),
	}
	for _, opt := range opts {
		opt(body)
	}
	C.Enqueue(&IngestionEvent{
		ID:   uuid.New().String(),
		Type: "span-create",
		Body: body,
		Time: time.Now(),
	})
}

// EndSpan 结束 Span（发送 span-update）.
// traceID must be the non-empty TraceID of the owning trace — Langfuse
// ingestion rejects span-update events with an empty traceId (400
// "Too small: expected string to have >=1 characters").
func EndSpan(traceID, spanID string, opts ...SpanOption) {
	if C == nil || !C.enabled {
		return
	}
	now := time.Now()
	body := &SpanBody{
		ID:      spanID,
		TraceID: traceID,
		EndTime: &now,
	}
	for _, opt := range opts {
		opt(body)
	}
	C.Enqueue(&IngestionEvent{
		ID:   uuid.New().String(),
		Type: "span-update",
		Body: body,
		Time: time.Now(),
	})
}

// SpanOption Span 配置选项
type SpanOption func(*SpanBody)

// WithSpanParent 设置 span 的父 observation ID
func WithSpanParent(parentID string) SpanOption {
	return func(s *SpanBody) {
		s.ParentObservationID = parentID
	}
}

// WithSpanInput 设置 span 输入
func WithSpanInput(input interface{}) SpanOption {
	return func(s *SpanBody) {
		s.Input = input
	}
}

// WithSpanOutput 设置 span 输出
func WithSpanOutput(output interface{}) SpanOption {
	return func(s *SpanBody) {
		s.Output = output
	}
}

// WithSpanLevel 设置 span 级别
func WithSpanLevel(level string) SpanOption {
	return func(s *SpanBody) {
		s.Level = level
	}
}

// WithSpanMetadata 设置 span 元数据
func WithSpanMetadata(meta map[string]string) SpanOption {
	return func(s *SpanBody) {
		s.Metadata = meta
	}
}

// WithSpanError 标记 span 为错误
func WithSpanError(msg string) SpanOption {
	return func(s *SpanBody) {
		s.Level = "ERROR"
		s.StatusMessage = msg
	}
}

// --- Generation ---

// CreateGeneration 创建 Generation 事件
func CreateGeneration(traceID, genID string, opts ...GenOption) {
	if C == nil || !C.enabled {
		return
	}
	body := &GenerationBody{
		ID:        genID,
		TraceID:   traceID,
		StartTime: time.Now(),
	}
	for _, opt := range opts {
		opt(body)
	}
	C.Enqueue(&IngestionEvent{
		ID:   uuid.New().String(),
		Type: "generation-create",
		Body: body,
		Time: time.Now(),
	})
}

// EndGeneration 结束 Generation（发送 generation-update 附加 output/usage）.
// traceID must be the non-empty TraceID of the owning trace — Langfuse
// ingestion rejects generation-update events with an empty traceId (400
// "Too small: expected string to have >=1 characters").
func EndGeneration(traceID, genID string, opts ...GenOption) {
	if C == nil || !C.enabled {
		return
	}
	now := time.Now()
	body := &GenerationBody{
		ID:      genID,
		TraceID: traceID,
		EndTime: &now,
	}
	for _, opt := range opts {
		opt(body)
	}
	C.Enqueue(&IngestionEvent{
		ID:   uuid.New().String(),
		Type: "generation-update",
		Body: body,
		Time: time.Now(),
	})
}

// GenOption Generation 配置选项
type GenOption func(*GenerationBody)

// WithGenParent 设置 generation 的父 observation ID
func WithGenParent(parentID string) GenOption {
	return func(g *GenerationBody) {
		g.ParentObservationID = parentID
	}
}

// WithGenName 设置 generation 名称
func WithGenName(name string) GenOption {
	return func(g *GenerationBody) {
		g.Name = name
	}
}

// WithGenModel 设置 generation 模型
func WithGenModel(model string) GenOption {
	return func(g *GenerationBody) {
		g.Model = model
	}
}

// WithGenInput 设置 generation 输入
func WithGenInput(input interface{}) GenOption {
	return func(g *GenerationBody) {
		g.Input = input
	}
}

// WithGenOutput 设置 generation 输出
func WithGenOutput(output interface{}) GenOption {
	return func(g *GenerationBody) {
		g.Output = output
	}
}

// WithGenUsage 设置 generation token 用量
func WithGenUsage(promptTokens, completionTokens int) GenOption {
	return func(g *GenerationBody) {
		g.Usage = &UsageData{
			Input:  promptTokens,
			Output: completionTokens,
			Total:  promptTokens + completionTokens,
		}
	}
}

// WithGenCachedUsage is WithGenUsage plus the prompt-cache HIT token count
// (Batch A auto-caching). It does NOT change WithGenUsage's signature, so the
// ~12 legacy callers are untouched. cachedTokens is the subset of promptTokens
// served from the provider's prefix cache; 0 leaves CachedInput absent
// (omitempty) ⇒ byte-identical to WithGenUsage.
func WithGenCachedUsage(promptTokens, completionTokens, cachedTokens int) GenOption {
	return func(g *GenerationBody) {
		g.Usage = &UsageData{
			Input:       promptTokens,
			Output:      completionTokens,
			Total:       promptTokens + completionTokens,
			CachedInput: cachedTokens,
		}
	}
}

// WithGenError 标记 generation 为错误
func WithGenError(msg string) GenOption {
	return func(g *GenerationBody) {
		g.Level = "ERROR"
		g.StatusMessage = msg
	}
}

// WithGenPromptName 关联 Langfuse 管理的 prompt
func WithGenPromptName(name string, version int) GenOption {
	return func(g *GenerationBody) {
		g.PromptName = name
		g.PromptVersion = version
	}
}

// --- Score ---

// Score 创建评分事件
func Score(traceID, name string, value float64, comment string) {
	if C == nil || !C.enabled {
		return
	}
	body := &ScoreBody{
		ID:       uuid.New().String(),
		TraceID:  traceID,
		Name:     name,
		Value:    value,
		Comment:  comment,
		DataType: "NUMERIC",
	}
	C.Enqueue(&IngestionEvent{
		ID:   uuid.New().String(),
		Type: "score-create",
		Body: body,
		Time: time.Now(),
	})
}
