package permission

import (
	"context"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// dbAuditLogger 是默认 AuditLogger 实现，写 agent_permission_decision_log 表。
type dbAuditLogger struct {
	store store.IAgentPermissionStore
}

func newDBAuditLogger(s store.IAgentPermissionStore) AuditLogger {
	return &dbAuditLogger{store: s}
}

// Log 写一条决策日志；错误仅 zap.Warn 不抛出（异步 goroutine 已无法回传错误给业务路径）。
func (l *dbAuditLogger) Log(ctx context.Context, entry AuditEntry) {
	toolName := ""
	if entry.Req.Tool != nil {
		toolName = entry.Req.Tool.Name()
	}
	row := &model.AgentPermissionDecisionLog{
		AgentRunID:        entry.Req.AgentRunID,
		UserID:            entry.Req.UserID,
		ParentUserID:      entry.Req.ParentUserID,
		AgentDefinitionID: entry.Req.AgentDefinitionID,
		ToolName:          toolName,
		ToolInputDigest:   Digest(entry.Req.InputJSON),
		Behavior:          entry.Result.Behavior,
		DecisionReason:    string(entry.Result.DecisionReason),
		ValidatorID:       entry.Result.ValidatorID,
		Message:           entry.Result.Message,
		LatencyMs:         entry.LatencyMs,
	}
	if err := l.store.CreateDecisionLog(ctx, row); err != nil {
		log.Warnw("AuditLogger.Log: CreateDecisionLog failed",
			"agent_run_id", entry.Req.AgentRunID,
			"tool", toolName,
			"behavior", entry.Result.Behavior,
			"error", err)
	}
}
