package langfuse

import "time"

// IngestionEvent Langfuse ingestion API 事件
type IngestionEvent struct {
	ID   string      `json:"id"`
	Type string      `json:"type"` // trace-create, generation-create, span-create, span-update, score-create, generation-update
	Body interface{} `json:"body"`
	Time time.Time   `json:"timestamp"`
}

// IngestionBatch 批量 ingestion 请求
type IngestionBatch struct {
	Batch []*IngestionEvent `json:"batch"`
}

// TraceBody Trace 事件体
type TraceBody struct {
	ID        string            `json:"id"`
	Name      string            `json:"name,omitempty"`
	UserID    string            `json:"userId,omitempty"`
	SessionID string            `json:"sessionId,omitempty"`
	Input     interface{}       `json:"input,omitempty"`
	Output    interface{}       `json:"output,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// SpanBody Span 事件体
type SpanBody struct {
	ID                  string            `json:"id"`
	TraceID             string            `json:"traceId"`
	ParentObservationID string            `json:"parentObservationId,omitempty"`
	Name                string            `json:"name,omitempty"`
	Input               interface{}       `json:"input,omitempty"`
	Output              interface{}       `json:"output,omitempty"`
	Metadata            map[string]string `json:"metadata,omitempty"`
	StartTime           time.Time         `json:"startTime"`
	EndTime             *time.Time        `json:"endTime,omitempty"`
	StatusMessage       string            `json:"statusMessage,omitempty"`
	Level               string            `json:"level,omitempty"` // DEBUG, DEFAULT, WARNING, ERROR
}

// GenerationBody Generation 事件体
type GenerationBody struct {
	ID                  string            `json:"id"`
	TraceID             string            `json:"traceId"`
	ParentObservationID string            `json:"parentObservationId,omitempty"`
	Name                string            `json:"name,omitempty"`
	Model               string            `json:"model,omitempty"`
	ModelParameters     map[string]string `json:"modelParameters,omitempty"`
	Input               interface{}       `json:"input,omitempty"`
	Output              interface{}       `json:"output,omitempty"`
	Metadata            map[string]string `json:"metadata,omitempty"`
	StartTime           time.Time         `json:"startTime"`
	EndTime             *time.Time        `json:"endTime,omitempty"`
	Usage               *UsageData        `json:"usage,omitempty"`
	Level               string            `json:"level,omitempty"`
	StatusMessage       string            `json:"statusMessage,omitempty"`
	PromptName          string            `json:"promptName,omitempty"`
	PromptVersion       int               `json:"promptVersion,omitempty"`
}

// UsageData token 用量
type UsageData struct {
	Input  int `json:"input,omitempty"`
	Output int `json:"output,omitempty"`
	Total  int `json:"total,omitempty"`
	// CachedInput is the prompt-cache HIT subset of Input (Batch A auto-caching).
	// omitempty ⇒ absent when 0, so non-cache generations serialize identically
	// to pre-cache behavior. Channel A of the dual-channel observability scheme;
	// channel B (output.metadata.cached_input_tokens) is the guaranteed-visible
	// fallback for Langfuse versions that do not parse this usage field.
	CachedInput int `json:"cached_input,omitempty"`
}

// ScoreBody 评分事件体
type ScoreBody struct {
	ID            string  `json:"id"`
	TraceID       string  `json:"traceId"`
	ObservationID string  `json:"observationId,omitempty"`
	Name          string  `json:"name"`
	Value         float64 `json:"value"`
	Comment       string  `json:"comment,omitempty"`
	DataType      string  `json:"dataType,omitempty"` // NUMERIC, BOOLEAN, CATEGORICAL
}

// PromptResponse Langfuse Prompt API 响应
type PromptResponse struct {
	Name    string      `json:"name"`
	Version int         `json:"version"`
	Prompt  string      `json:"prompt"`
	Config  interface{} `json:"config"`
	Labels  []string    `json:"labels"`
	Tags    []string    `json:"tags"`
}
