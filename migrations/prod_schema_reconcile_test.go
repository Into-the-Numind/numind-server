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

const protectedRolloutTablePattern = `user|subscription|trial_grant|credit_account|credit_cycle|user_booster_balance|credit_reservation|credit_reservation_item|credit_transaction|membership_event|sop_run|sop_node_run|chatbot_message|chatbot_session|sales_message|sales_session|agent_run|agent_attachment`

func protectedDMLPatterns(tablePattern string) []*regexp.Regexp {
	qualifiedTable := `(?:` + "`?" + `[a-z_][a-z0-9_]*` + "`?" + `\s*\.\s*)?` +
		"`?" + `(?:` + tablePattern + `)\b` + "`?"
	return []*regexp.Regexp{
		regexp.MustCompile(`(?is)\bupdate\s+(?:(?:low_priority|ignore)\s+)*` + qualifiedTable),
		regexp.MustCompile(`(?is)\binsert\s+(?:(?:low_priority|delayed|high_priority|ignore)\s+)*(?:into\s+)?` + qualifiedTable),
		regexp.MustCompile(`(?is)\breplace\s+(?:(?:low_priority|delayed)\s+)*(?:into\s+)?` + qualifiedTable),
		regexp.MustCompile(`(?is)\bdelete\s+(?:(?:low_priority|quick|ignore)\s+)*[^;]*?` + qualifiedTable),
	}
}

func containsProtectedDML(sql, tablePattern string) bool {
	for _, pattern := range protectedDMLPatterns(tablePattern) {
		if pattern.MatchString(sql) {
			return true
		}
	}
	return false
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

func TestProdSchemaReconcileRunbookRecordsCurrentMySQL8PassMarker(t *testing.T) {
	const marker = "PASS: MySQL 8 exact, partial, negative-preflight, double-apply, constraints, and protected-data checks"
	runner := readRequiredRolloutFile(t, filepath.Join(prodSchemaReconcileDir, "test-mysql8.sh"))
	runbook := readRequiredRolloutFile(t, filepath.Join(prodSchemaReconcileDir, "README.md"))
	if !strings.Contains(runner, marker) {
		t.Fatalf("MySQL 8 runner must emit the documented pass marker")
	}
	if !strings.Contains(runbook, marker) {
		t.Fatalf("runbook must record the current MySQL 8 pass marker")
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
		"ext_resume:",
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

	if containsProtectedDML(sql, protectedRolloutTablePattern) {
		t.Error("migration must not write protected customer, subscription, credit, attachment, or history tables")
	}
	if regexp.MustCompile(`(?i)'(?:sk-|lark_cli_|feishu_)[a-z0-9_-]{12,}'`).MatchString(sql) {
		t.Error("migration appears to contain a hard-coded credential")
	}
}

func TestProdSchemaReconcileNeverWritesSubscriptionRows(t *testing.T) {
	sql := strings.ToLower(stripSQLComments(readRequiredRolloutFile(t, prodSchemaReconcileMigration)))
	if containsProtectedDML(sql, "subscription") {
		t.Error("subscription rollout must be additive schema only and must not write historical rows")
	}
}

func TestProdSchemaReconcileProtectedDMLGuardCatchesMySQLWriteForms(t *testing.T) {
	for _, sql := range []string{
		"UPDATE subscription s JOIN user u ON u.id=s.user_id SET s.user_id=9",
		"UPDATE LOW_PRIORITY IGNORE `prod`.`user` AS u SET u.username='changed'",
		"INSERT IGNORE INTO prod.trial_grant (id) VALUES (1)",
		"REPLACE LOW_PRIORITY prod.membership_event (id) VALUES (1)",
		"DELETE LOW_PRIORITY QUICK IGNORE s FROM prod.subscription AS s JOIN prod.user u ON u.id=s.user_id",
	} {
		if !containsProtectedDML(strings.ToLower(sql), protectedRolloutTablePattern) {
			t.Fatalf("protected DML guard missed write: %s", sql)
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

func TestProdSchemaReconcilePreflightCoversFailClosedContracts(t *testing.T) {
	sql := strings.ToLower(readRequiredRolloutFile(
		t,
		filepath.Join(prodSchemaReconcileDir, "00-preflight.sql"),
	))
	for _, required := range []string{
		"_schema_contract",
		"ac58e234470d95c46cbefe91cb49a4ea7cdcac1c9391242884638839cadbf112",
		"feishu_proof_fk_contract",
		"duplicate_announcement_read_user_pair",
		"duplicate_survey_response_user_pair",
		"agent_state_reason_upgradeable",
		"checksum table",
		"agent_attachment_protected_projection",
		"agent_run_protected_projection",
		"trial_grant",
		"user_booster_balance",
		"membership_event",
		"ai_service_model_key_unique_contract",
		"_column_compatibility",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("preflight missing fail-closed contract %q", required)
		}
	}
}

func TestProdSchemaReconcileCoversDevHistoricalCompatibilityContracts(t *testing.T) {
	preflight := strings.ToLower(readRequiredRolloutFile(
		t,
		filepath.Join(prodSchemaReconcileDir, "00-preflight.sql"),
	))
	verify := strings.ToLower(readRequiredRolloutFile(
		t,
		filepath.Join(prodSchemaReconcileDir, "02-verify.sql"),
	))
	migration := strings.ToLower(readRequiredRolloutFile(t, prodSchemaReconcileMigration))

	for _, required := range []string{
		"agent_attachment_complete_projection",
		"feishu_proof_business_projection",
		"character_set_name",
		"collation_name",
		"is_visible",
		"expression",
		"legacy_complete",
		"octet_length",
		"zombie_cleanup_2026_05_28",
	} {
		if !strings.Contains(preflight, required) {
			t.Errorf("preflight missing Dev compatibility contract %q", required)
		}
		if !strings.Contains(verify, required) {
			t.Errorf("verify missing Dev compatibility contract %q", required)
		}
	}

	for _, required := range []string{
		"zombie_cleanup_2026_05_28",
		"status` = 'running'",
		"is_deleted` = 1",
		"_prod_schema_reconcile_proof_fk",
		"alter table `feishu_operation_proof_consumption`",
	} {
		if !strings.Contains(migration, required) {
			t.Errorf("migration missing Dev compatibility repair %q", required)
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
