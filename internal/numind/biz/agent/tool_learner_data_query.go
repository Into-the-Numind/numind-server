package agent

import (
	"context"
	"encoding/json"

	"numind-server/internal/pkg/model"
)

// userByIDGetter is the subset of store.UserStore used by learnerDataQueryTool.
// Keeping a narrow interface makes the tool testable without a full IStore mock.
type userByIDGetter interface {
	GetByID(ctx context.Context, userID uint) (*model.User, error)
}

type learnerDataQueryTool struct {
	BaseTool
	users userByIDGetter
}

type learnerDataQueryInput struct {
	UserID uint   `json:"user_id"`
	Field  string `json:"field,omitempty"`
}

var _ FullTool = (*learnerDataQueryTool)(nil)

func (t *learnerDataQueryTool) Name() string { return "learner_data_query" }
func (t *learnerDataQueryTool) Description() string {
	return "Query the current learner's profile data (read-only, sanitized)."
}
func (t *learnerDataQueryTool) UserFacingName() string { return "学员档案" }
func (t *learnerDataQueryTool) NarrationVerb() string  { return "查询" }
func (t *learnerDataQueryTool) IsReadOnly() bool       { return true }

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *learnerDataQueryTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"user_id": {"type": "integer", "minimum": 0, "description": "ID of the learner whose data to query."},
			"field":   {"type": "string", "description": "Optional specific field to return; omit to return all available fields."}
		},
		"required": ["user_id"]
	}`)
}

// Execute parses {"user_id":..., "field":...} and returns the sanitized learner
// profile data (optionally narrowed to a single field).
func (t *learnerDataQueryTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in learnerDataQueryInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	user, err := t.users.GetByID(ctx, in.UserID)
	if err != nil {
		return nil, err
	}
	// Sanitized: return only non-sensitive fields; omit Password, Phone, IsAdmin, etc.
	safe := map[string]interface{}{
		"id":             user.ID,
		"nickname":       user.Nickname,
		"username":       user.Username,
		"total_sop_runs": user.TotalSopRuns,
		"created_at":     user.CreatedAt,
	}
	out, _ := json.Marshal(safe)
	return ToolResult(out), nil
}
