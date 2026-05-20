package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"numind-server/internal/pkg/model"
)

// mockUserByIDGetter implements the narrow userByIDGetter interface.
type mockUserByIDGetter struct {
	user *model.User
	err  error
}

func (m *mockUserByIDGetter) GetByID(_ context.Context, _ uint) (*model.User, error) {
	return m.user, m.err
}

func TestLearnerDataQueryTool_Execute_SanitizedOutput(t *testing.T) {
	fakeUser := &model.User{
		Nickname:     "Alice",
		Username:     "alice123",
		TotalSopRuns: 5,
	}
	fakeUser.ID = 42
	fakeUser.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tool := &learnerDataQueryTool{users: &mockUserByIDGetter{user: fakeUser}}

	input, _ := json.Marshal(learnerDataQueryInput{UserID: 42})
	result, err := tool.Execute(context.Background(), ToolInput(input))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	var out map[string]interface{}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	// Assert non-sensitive fields are present
	if out["nickname"] != "Alice" {
		t.Errorf("expected nickname 'Alice', got %v", out["nickname"])
	}
	if out["username"] != "alice123" {
		t.Errorf("expected username 'alice123', got %v", out["username"])
	}
	// Numeric field: JSON decodes int → float64 in interface{}
	if v, ok := out["total_sop_runs"].(float64); !ok || v != 5 {
		t.Errorf("expected total_sop_runs=5 (float64), got %v (type %T)", out["total_sop_runs"], out["total_sop_runs"])
	}

	// Assert sensitive fields are absent
	for _, sensitive := range []string{"password", "phone", "is_admin"} {
		if _, ok := out[sensitive]; ok {
			t.Errorf("sensitive field %q should not be in output", sensitive)
		}
	}
}

func TestLearnerDataQueryTool_Execute_PropagatesError(t *testing.T) {
	tool := &learnerDataQueryTool{users: &mockUserByIDGetter{err: errors.New("db error")}}
	input, _ := json.Marshal(learnerDataQueryInput{UserID: 1})
	_, err := tool.Execute(context.Background(), ToolInput(input))
	if err == nil {
		t.Error("expected error to be propagated")
	}
}

func TestLearnerDataQueryTool_Execute_BadJSON(t *testing.T) {
	tool := &learnerDataQueryTool{users: &mockUserByIDGetter{}}
	_, err := tool.Execute(context.Background(), ToolInput([]byte("not-json")))
	if err == nil {
		t.Error("expected JSON unmarshal error")
	}
}

func TestLearnerDataQueryTool_IsReadOnly(t *testing.T) {
	tool := &learnerDataQueryTool{}
	if !tool.IsReadOnly() {
		t.Error("learner_data_query should be read-only")
	}
}
