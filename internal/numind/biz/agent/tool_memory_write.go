package agent

import (
	"context"
	"encoding/json"

	"numind-server/internal/numind/biz/memory"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// memoryWriteToolInput is the LLM-facing input schema for memory_write.
// P1-1 决议：v1 source_type 固定为 SourceAgentTool，不暴露给 LLM；v2 可扩展。
type memoryWriteToolInput struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// memoryWriteTool implements FullTool for the "memory_write" agent tool.
// It writes a single L2 (user_global_memory) entry via the Notepad biz interface.
type memoryWriteTool struct {
	BaseTool
	notepad memory.Notepad
}

var _ FullTool = (*memoryWriteTool)(nil)

// NewMemoryWriteTool constructs a memory_write FullTool backed by the given Notepad.
func NewMemoryWriteTool(np memory.Notepad) FullTool {
	return &memoryWriteTool{notepad: np}
}

// Name returns the tool identifier used by the LLM.
func (t *memoryWriteTool) Name() string { return "memory_write" }

// Description returns the LLM-facing description of the tool.
func (t *memoryWriteTool) Description() string {
	return "Save a long-term preference / fact / learning into the learner's global memory. " +
		"Same key overwrites. Use only when the learner explicitly expresses a preference or decision. " +
		"Input: { kind: 'learning'|'decision'|'issue'|'fact'|'preference', key: string(<=100), value: string(<=1024) }."
}

// UserFacingName returns the human-readable tool name shown in the UI.
func (t *memoryWriteTool) UserFacingName() string { return "记忆写入" }

// NarrationVerb returns the verb used in run narration messages.
func (t *memoryWriteTool) NarrationVerb() string { return "记忆" }

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *memoryWriteTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"kind":  {"type": "string", "enum": ["learning", "decision", "issue", "fact", "preference"], "description": "Memory kind/category bucket. Must be one of the five canonical kinds."},
			"key":   {"type": "string", "description": "Unique key identifying this memory entry (upsert by key)."},
			"value": {"type": "string", "description": "The value to remember."}
		},
		"required": ["kind", "key", "value"]
	}`)
}

// Execute validates the input, extracts userID + agentDefID from context, and
// calls Notepad.Write to upsert the L2 memory entry.
func (t *memoryWriteTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in memoryWriteToolInput
	// Model-input and recoverable failures stay soft: a non-nil Go error is a
	// NodeRunError that kills the whole agent run (tool-soft-error-sweep).
	if err := json.Unmarshal(input, &in); err != nil {
		return softToolError("memory_write", "invalid input: %v", err)
	}

	userID, ok := middleware.UserIDFromCtx(ctx)
	if !ok {
		// System wiring gap (runner did not inject the user) — still soft: a
		// missing memory write must not abort the whole research run. Warn so
		// the wiring bug stays visible to ops (T3 review P1).
		log.Warnw("memory_write: no user in context — runner wiring bug")
		return softToolError("memory_write", "memory unavailable: no user in context")
	}

	// #7 memory-system: read source_agent_definition_id from context.
	// Injected by runner.go Step 4 (NewContextWithAgentDefinitionID) when AgentDefinitionID>0.
	agentDefID, _ := middleware.AgentDefinitionIDFromCtx(ctx)
	var sourceAgentDefID *uint64
	if agentDefID > 0 {
		sourceAgentDefID = &agentDefID
	}

	if err := t.notepad.Write(ctx, userID, memory.MemoryKind(in.Kind), in.Key, in.Value, memory.WriteOpts{
		SourceType:              memory.SourceAgentTool,
		SourceAgentDefinitionID: sourceAgentDefID,
	}); err != nil {
		// Includes Notepad input validation (bad kind / oversized key) and
		// transient store failures — both recoverable for the LLM.
		log.Warnw("memory_write: notepad write failed", "error", err)
		return softToolError("memory_write", "write failed: %v", err)
	}

	return ToolResult(`{"ok": true}`), nil
}

// IsReadOnly returns false — memory_write mutates the L2 store.
func (t *memoryWriteTool) IsReadOnly() bool { return false }

// IsDestructive returns false — upsert semantics, not deletion.
func (t *memoryWriteTool) IsDestructive() bool { return false }

// AlwaysLoad returns true so the tool is always available to the agent.
func (t *memoryWriteTool) AlwaysLoad() bool { return true }
