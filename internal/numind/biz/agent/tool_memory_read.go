package agent

import (
	"context"
	"encoding/json"
	"time"

	"numind-server/internal/numind/biz/memory"
	"numind-server/internal/pkg/middleware"
)

// memoryReadToolInput is the LLM-facing input schema for memory_read.
// Key and Kind are mutually optional: if Key is set, a single entry is returned;
// if Kind is set, a list is returned; if neither is set, an empty array is returned.
type memoryReadToolInput struct {
	Key   string `json:"key,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// memoryReadOutItem is the per-entry shape returned to the LLM.
type memoryReadOutItem struct {
	Kind       string    `json:"kind"`
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	Confidence float64   `json:"confidence"`
	CreatedAt  time.Time `json:"created_at"`
}

// memoryReadTool implements FullTool for the "memory_read" agent tool.
// It reads L2 (user_global_memory) entries via the Notepad biz interface and
// HTML-unescapes values before returning them to the LLM.
type memoryReadTool struct {
	BaseTool
	notepad memory.Notepad
}

var _ FullTool = (*memoryReadTool)(nil)

// NewMemoryReadTool constructs a memory_read FullTool backed by the given Notepad.
func NewMemoryReadTool(np memory.Notepad) FullTool {
	return &memoryReadTool{notepad: np}
}

// Name returns the tool identifier used by the LLM.
func (t *memoryReadTool) Name() string { return "memory_read" }

// Description returns the LLM-facing description of the tool.
func (t *memoryReadTool) Description() string {
	return "Read the learner's long-term memory. Query by exact key or list by kind. " +
		"Input: { key?: string, kind?: 'learning'|'decision'|'issue'|'fact'|'preference', limit?: int(<=50, default 10) }. " +
		"Returns JSON array."
}

// UserFacingName returns the human-readable tool name shown in the UI.
func (t *memoryReadTool) UserFacingName() string { return "记忆读取" }

// NarrationVerb returns the verb used in run narration messages.
func (t *memoryReadTool) NarrationVerb() string { return "查阅" }

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *memoryReadTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"key":   {"type": "string", "description": "Read a single memory entry by exact key. Takes precedence over kind."},
			"kind":  {"type": "string", "enum": ["learning", "decision", "issue", "fact", "preference"], "description": "List memory entries of this kind (used when key is omitted)."},
			"limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Max entries when listing by kind (1-50, default 10)."}
		}
	}`)
}

// Execute parses the input, extracts userID from context, then queries the
// Notepad by key (single entry) or kind (list).  Values are HTML-unescaped
// before being returned so the LLM receives the original content.
func (t *memoryReadTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in memoryReadToolInput
	// Model-input and recoverable failures stay soft (tool-soft-error-sweep).
	if err := json.Unmarshal(input, &in); err != nil {
		return softToolError("memory_read", "invalid input: %v", err)
	}

	// Clamp limit: <=0 or >50 → default 10.
	if in.Limit <= 0 || in.Limit > 50 {
		in.Limit = 10
	}

	userID, ok := middleware.UserIDFromCtx(ctx)
	if !ok {
		return softToolError("memory_read", "memory unavailable: no user in context (system wiring)")
	}

	var items []memory.MemoryItem
	if in.Key != "" {
		item, err := t.notepad.Read(ctx, userID, in.Key)
		if err != nil {
			return softToolError("memory_read", "read failed: %v", err)
		}
		if item != nil {
			items = append(items, *item)
		}
	} else if in.Kind != "" {
		list, err := t.notepad.ListByKind(ctx, userID, memory.MemoryKind(in.Kind), in.Limit)
		if err != nil {
			return softToolError("memory_read", "list failed: %v", err)
		}
		items = list
	}
	// Neither key nor kind → return empty array (not an error).

	// P2-2: HTML-unescape values for LLM consumption (reverse of EscapeForStorage).
	out := make([]memoryReadOutItem, len(items))
	for i, it := range items {
		out[i] = memoryReadOutItem{
			Kind:       string(it.Kind),
			Key:        it.KeyName,
			Value:      memory.UnescapeForToolResponse(it.Content),
			Confidence: it.Confidence,
			CreatedAt:  it.CreatedAt,
		}
	}

	b, _ := json.Marshal(out)
	return ToolResult(b), nil
}

// IsSearchOrReadCommand marks this tool as a read-only search/read command.
func (t *memoryReadTool) IsSearchOrReadCommand() bool { return true }

// AlwaysLoad returns true so the tool is always available to the agent.
func (t *memoryReadTool) AlwaysLoad() bool { return true }
