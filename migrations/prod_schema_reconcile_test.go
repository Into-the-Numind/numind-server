package migrations

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	prodSchemaReconcileMigration = "20260730_120000_prod_schema_reconcile.sql"
	prodSchemaReconcileDir       = "../scripts/2026-07-30-prod-schema-reconcile"
)

func readRequiredRolloutFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read required rollout artifact %s: %v", path, err)
	}
	return string(data)
}

func stripSQLComments(sql string) string {
	var kept []string
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func TestProdSchemaReconcileArtifactsExist(t *testing.T) {
	required := []string{
		prodSchemaReconcileMigration,
		filepath.Join(prodSchemaReconcileDir, "00-preflight.sql"),
		filepath.Join(prodSchemaReconcileDir, "02-verify.sql"),
		filepath.Join(prodSchemaReconcileDir, "README.md"),
		filepath.Join(prodSchemaReconcileDir, "test-mysql8.sh"),
		filepath.Join(prodSchemaReconcileDir, "testdata", "prod-partial-baseline.sql"),
	}
	for _, path := range required {
		if info, err := os.Stat(path); err != nil {
			t.Errorf("required rollout artifact missing: %s: %v", path, err)
		} else if info.Size() == 0 {
			t.Errorf("required rollout artifact is empty: %s", path)
		}
	}
}

func TestProdSchemaReconcileContainsFinalProductSchema(t *testing.T) {
	sql := strings.ToLower(readRequiredRolloutFile(t, prodSchemaReconcileMigration))

	for _, table := range []string{
		"document",
		"user_third_party_account",
		"feishu_cli_vault",
		"feishu_auth_session",
		"feishu_operation",
		"feishu_operation_proof_consumption",
		"feishu_operation_execution_gate",
	} {
		if !strings.Contains(sql, "create table if not exists `"+table+"`") {
			t.Errorf("migration must create final-state table %s idempotently", table)
		}
	}

	for _, column := range []string{
		"plan_type",
		"cycle_credits",
		"parsed_content",
		"parsed_content_sha256",
		"parsed_content_byte_size",
		"parsed_page_count",
		"parsed_at",
		"pending_external_action_json",
		"pending_external_action_at",
	} {
		if !strings.Contains(sql, column) {
			t.Errorf("migration missing required column contract %s", column)
		}
	}

	for _, stableKey := range []string{
		"qwen3.5-flash",
		"ali-dashscope",
		"attachment.vision_describe",
	} {
		if !strings.Contains(sql, stableKey) {
			t.Errorf("migration missing stable system configuration key %s", stableKey)
		}
	}

	for _, constraint := range []string{
		"fk_annread_announcement",
		"fk_annread_user",
		"fk_sq_announcement",
		"fk_sr_announcement",
		"fk_sr_user",
		"fk_sa_response",
		"fk_sa_question",
		"idx_ar_state_pending",
	} {
		if !strings.Contains(sql, constraint) {
			t.Errorf("migration missing constraint/index %s", constraint)
		}
	}
}

func TestProdSchemaReconcileForbidsDestructiveOrOutOfScopeSQL(t *testing.T) {
	sql := strings.ToLower(stripSQLComments(readRequiredRolloutFile(t, prodSchemaReconcileMigration)))

	for _, forbidden := range []string{
		"drop table",
		"truncate",
		"meeting_session",
		"meeting_segment",
		"meeting_feedback",
		"meeting_preset",
		"chatbot_query_rewrite",
		"universal_rewriter",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration contains forbidden or out-of-scope SQL token %q", forbidden)
		}
	}

	if regexp.MustCompile(`(?i)\b(delete|update)\s+` + "`?" + `(user|credit_account|credit_cycle|credit_reservation|credit_reservation_item|credit_transaction|sop_run|sop_node_run|chatbot_message|chatbot_session|sales_message|sales_session|agent_run)` + "`?" + `\b`).MatchString(sql) {
		t.Error("migration must not DELETE/UPDATE protected customer, credit, or history tables")
	}
	if regexp.MustCompile(`(?i)'(?:sk-|lark_cli_|feishu_)[a-z0-9_-]{12,}'`).MatchString(sql) {
		t.Error("migration appears to contain a hard-coded credential")
	}
}

func TestProdSchemaReconcileSubscriptionWritesOnlyNewColumns(t *testing.T) {
	sql := strings.ToLower(stripSQLComments(readRequiredRolloutFile(t, prodSchemaReconcileMigration)))
	protected := []string{
		"first_started_at",
		"current_started_at",
		"expires_at",
		"total_months_purchased",
		"source",
		"granter_user_id",
		"created_at",
		"updated_at",
	}
	for _, statement := range strings.Split(sql, ";") {
		if !strings.Contains(statement, "update `subscription`") &&
			!strings.Contains(statement, "update subscription") {
			continue
		}
		setAt := strings.Index(statement, "set")
		whereAt := strings.Index(statement, "where")
		if setAt < 0 {
			t.Errorf("subscription UPDATE has no SET clause: %s", strings.TrimSpace(statement))
			continue
		}
		assignments := statement[setAt:]
		if whereAt > setAt {
			assignments = statement[setAt:whereAt]
		}
		for _, column := range protected {
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(column) + `\s*=`).MatchString(assignments) {
				t.Errorf("subscription UPDATE must not assign protected column %s", column)
			}
		}
	}
}

func TestProdSchemaReconcilePreflightIsReadOnly(t *testing.T) {
	sql := strings.ToLower(stripSQLComments(readRequiredRolloutFile(
		t,
		filepath.Join(prodSchemaReconcileDir, "00-preflight.sql"),
	)))
	if regexp.MustCompile(`(?m)\b(insert|update|delete|replace|alter|create|drop|truncate|rename|grant|revoke)\b`).MatchString(sql) {
		t.Error("preflight must contain only read-only SQL")
	}
}

func TestProdSchemaReconcileModelTypesAreExplicit(t *testing.T) {
	attachment := readRequiredRolloutFile(t, "../internal/pkg/model/agent_attachment.go")
	for _, tag := range []string{
		`gorm:"size:71;not null;default:''"`,
		`gorm:"type:bigint;not null;default:0"`,
		`gorm:"type:int;not null;default:0"`,
		`gorm:"type:datetime(3)"`,
	} {
		if !strings.Contains(attachment, tag) {
			t.Errorf("AgentAttachment must contain explicit rollout schema tag %s", tag)
		}
	}

	document := readRequiredRolloutFile(t, "../internal/pkg/model/document.go")
	if strings.Count(document, `gorm:"type:bigint unsigned`) < 2 {
		t.Error("Document user identity fields must explicitly use BIGINT UNSIGNED")
	}
}
