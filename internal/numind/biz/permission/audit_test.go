package permission

import (
	"context"
	"errors"
	"sync"
	"testing"

	"numind-server/internal/pkg/model"
)

// fakeAuditStore — 不依赖 GORM；仅记录调用次数 + payload。
type fakeAuditStore struct {
	mu   sync.Mutex
	rows []model.AgentPermissionDecisionLog
	err  error
}

func (f *fakeAuditStore) ListActiveByParent(_ context.Context, _ uint) ([]model.AgentPermissionConfig, error) {
	return nil, nil
}
func (f *fakeAuditStore) CreateRule(_ context.Context, _ *model.AgentPermissionConfig) error {
	return nil
}
func (f *fakeAuditStore) CreateDecisionLog(_ context.Context, row *model.AgentPermissionDecisionLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, *row)
	return nil
}

func (f *fakeAuditStore) Snapshot() []model.AgentPermissionDecisionLog {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.AgentPermissionDecisionLog, len(f.rows))
	copy(out, f.rows)
	return out
}

func TestDBAuditLogger_Log_Success(t *testing.T) {
	store := &fakeAuditStore{}
	logger := newDBAuditLogger(store)
	entry := AuditEntry{
		Req: PermissionRequest{
			AgentRunID:        99,
			UserID:            5,
			ParentUserID:      1,
			AgentDefinitionID: 42,
			InputJSON:         `{"q":"hi"}`,
			// Tool nil 测试 nil-safe 路径
		},
		Result: PermissionResult{
			Behavior:       BehaviorDeny,
			DecisionReason: DecisionReasonRule,
			ValidatorID:    "Tenant:tool_blacklist",
			Message:        "denied",
		},
		LatencyMs: 5,
	}
	logger.Log(context.Background(), entry)
	rows := store.Snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Behavior != BehaviorDeny || r.ValidatorID != "Tenant:tool_blacklist" {
		t.Errorf("row fields mismatch: %+v", r)
	}
	if r.ToolInputDigest == "" || len(r.ToolInputDigest) != 64 {
		t.Errorf("ToolInputDigest invalid: %q", r.ToolInputDigest)
	}
	if r.LatencyMs != 5 {
		t.Errorf("LatencyMs = %d, want 5", r.LatencyMs)
	}
}

func TestDBAuditLogger_Log_StoreError_NoPanic(t *testing.T) {
	store := &fakeAuditStore{err: errors.New("DB down")}
	logger := newDBAuditLogger(store)
	entry := AuditEntry{
		Req:    PermissionRequest{AgentRunID: 1},
		Result: PermissionResult{Behavior: BehaviorAllow},
	}
	// Should not panic, just warn
	logger.Log(context.Background(), entry)
}
