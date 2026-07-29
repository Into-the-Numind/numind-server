package migrations

import (
	"crypto/sha256"
	"fmt"
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

func TestProdSchemaReconcileRunbookPinsCurrentMigrationSHA(t *testing.T) {
	migration := readRequiredRolloutFile(t, prodSchemaReconcileMigration)
	runbook := readRequiredRolloutFile(t, filepath.Join(prodSchemaReconcileDir, "README.md"))
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(migration)))
	if !strings.Contains(runbook, want) {
		t.Fatalf("runbook must pin current migration SHA256 %s", want)
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
		"chk_ar_state_reason",
		"external_resume_ready",
		"ext_resume:%",
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

	if regexp.MustCompile(`(?i)\b(delete|update)\s+` + "`?" + `(user|credit_account|credit_cycle|credit_reservation|credit_reservation_item|credit_transaction|sop_run|sop_node_run|chatbot_message|chatbot_session|sales_message|sales_session|agent_run|agent_attachment)` + "`?" + `\b`).MatchString(sql) {
		t.Error("migration must not DELETE/UPDATE protected customer, credit, attachment, or history tables")
	}
	if regexp.MustCompile(`(?i)'(?:sk-|lark_cli_|feishu_)[a-z0-9_-]{12,}'`).MatchString(sql) {
		t.Error("migration appears to contain a hard-coded credential")
	}
}

func subscriptionUpdateAssignments(sql string) []string {
	update := regexp.MustCompile(
		`(?is)\bupdate\s+` + "`?" + `subscription` + "`?" +
			`(?:\s+(?:as\s+)?[a-z_][a-z0-9_]*)?\s+set\s+(.+?)(?:\bwhere\b|$)`,
	)
	assignment := regexp.MustCompile(
		`(?i)(?:^|,)\s*(?:[a-z_][a-z0-9_]*\.)?` + "`?" +
			`([a-z_][a-z0-9_]*)` + "`?" + `\s*=`,
	)
	var columns []string
	for _, statement := range strings.Split(sql, ";") {
		matches := update.FindStringSubmatch(statement)
		if len(matches) != 2 {
			continue
		}
		for _, found := range assignment.FindAllStringSubmatch(matches[1], -1) {
			if len(found) == 2 {
				columns = append(columns, strings.ToLower(found[1]))
			}
		}
	}
	return columns
}

func TestProdSchemaReconcileSubscriptionWritesOnlyNewColumns(t *testing.T) {
	sql := strings.ToLower(stripSQLComments(readRequiredRolloutFile(t, prodSchemaReconcileMigration)))
	allowed := map[string]bool{"plan_type": true, "cycle_credits": true}
	for _, column := range subscriptionUpdateAssignments(sql) {
		if !allowed[column] {
			t.Errorf("subscription UPDATE assigns non-rollout column %s", column)
		}
	}
}

func TestSubscriptionUpdateAssignmentScannerRejectsOldOrUnknownColumns(t *testing.T) {
	sql := `
		UPDATE subscription SET plan_type='monthly', user_id=9 WHERE id=1;
		UPDATE subscription AS s SET s.cycle_credits=2000, s.unreviewed_column=1;
	`
	got := subscriptionUpdateAssignments(strings.ToLower(sql))
	want := []string{"plan_type", "user_id", "cycle_credits", "unreviewed_column"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("assignment scanner got %v, want %v", got, want)
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

func TestProdSchemaReconcilePreflightCoversFailClosedContracts(t *testing.T) {
	sql := strings.ToLower(readRequiredRolloutFile(
		t,
		filepath.Join(prodSchemaReconcileDir, "00-preflight.sql"),
	))
	for _, required := range []string{
		"_schema_contract",
		"a33468f2c8055a11a306b7d90fcc3cc44c94f60d9ec08ee2bdbfb2378f8c37ef",
		"feishu_proof_fk_contract",
		"duplicate_announcement_read_user_pair",
		"duplicate_survey_response_user_pair",
		"agent_state_reason_upgradeable",
		"checksum table",
		"agent_attachment_protected_projection",
		"agent_run_protected_projection",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("preflight missing fail-closed contract %q", required)
		}
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
