package compliance

import (
	"strings"

	"gorm.io/gorm"

	"numind-server/internal/pkg/compliance_scope"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// scopeWhitelistTables — agent-mode 7 表 opt-in 监控
// （S3 plan §3 M13 修正：之前散文误写"6 表"，实际 7 项）
var scopeWhitelistTables = map[string]bool{
	"agent_run":            true,
	"agent_session":        true,
	"agent_session_memory": true,
	"user_global_memory":   true,
	"agent_definition":     true,
	"compliance_rule":      true,
	"compliance_audit_log": true,
}

// ScopeValidator — GORM Before-Query hook 强制 parent_user_id / user_id filter
type ScopeValidator struct {
	audit *AuditLogger
}

// NewScopeValidator constructs a validator that emits audit entries on the
// given logger. Caller must Install() the validator on a *gorm.DB to register
// the callback.
func NewScopeValidator(a *AuditLogger) *ScopeValidator {
	return &ScopeValidator{audit: a}
}

// Install registers the Before-Query callback on the given DB. Safe to call
// once at biz.Init time. Returns error if callback registration fails.
func (v *ScopeValidator) Install(db *gorm.DB) error {
	return db.Callback().Query().Before("gorm:query").Register("compliance:scope_check", v.beforeQuery)
}

// beforeQuery is invoked by GORM before every Query. Whitelist-only.
// v1 fail-open: log warn + audit deny, do not abort the query.
// v2 (#14) will upgrade to db.AddError(ErrComplianceScopeViolation).
func (v *ScopeValidator) beforeQuery(db *gorm.DB) {
	table := db.Statement.Table
	if !scopeWhitelistTables[table] {
		return
	}
	ctx := db.Statement.Context
	if reason, ok := compliance_scope.SkipScopeFromCtx(ctx); ok {
		v.writeAudit(table, model.DecisionPassthrough, "skip:"+reason)
		return
	}

	// db.Statement.SQL is empty in Before("gorm:query") because gorm:query
	// itself builds the SQL. Build just the WHERE clause fragment to inspect
	// whether a parent_user_id / user_id predicate is present.
	whereFrag := buildWhereFragment(db)
	if !hasScopeFilter(whereFrag) {
		log.Warnw("scope_validator: query missing parent_user_id/user_id filter",
			"table", table, "where", whereFrag)
		v.writeAudit(table, model.DecisionDeny,
			"v1 fail-open warn only: "+truncate(whereFrag, 500))
		// v1: do NOT db.AddError() — fail-open. v2 #14 升级硬阻断。
	}
}

// buildWhereFragment builds just the WHERE fragment from the statement's
// current Clauses map, without consuming or mutating the main SQL buffer.
// Used to inspect filter presence before gorm:query builds the full SQL.
func buildWhereFragment(db *gorm.DB) string {
	// Clone statement to avoid mutating the live SQL buffer.
	// We only need the string representation of the WHERE clause.
	c, ok := db.Statement.Clauses["WHERE"]
	if !ok || c.Expression == nil {
		return ""
	}
	// Use fmt-based inspection: marshal the WHERE expression via a throw-away
	// statement builder to get the SQL fragment with actual column names.
	var buf strings.Builder
	stmt := db.Statement
	tmpStmt := &gorm.Statement{
		DB:       db,
		ConnPool: db.ConnPool,
		Schema:   stmt.Schema,
		Clauses:  stmt.Clauses,
	}
	tmpStmt.SQL = buf
	tmpStmt.Build("WHERE")
	return tmpStmt.SQL.String()
}

func (v *ScopeValidator) writeAudit(table, decision, reason string) {
	if v.audit == nil {
		return
	}
	v.audit.Write(&model.ComplianceAuditLog{
		RuleLayer: model.RuleLayerScope,
		Decision:  decision,
		Reason:    "table=" + table + " " + reason,
	})
}

// hasScopeFilter — SQL 字符串含 parent_user_id / user_id 谓词
// 覆盖 GORM 各种 quoting 变体（S1 reviewer P2-5 决策）
func hasScopeFilter(sql string) bool {
	lower := strings.ToLower(sql)
	patterns := []string{
		"parent_user_id",
		"`parent_user_id`",
		`"parent_user_id"`,
		"user_id",
		"`user_id`",
		`"user_id"`,
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
