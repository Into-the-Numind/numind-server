package migrations

import (
	"regexp"
	"strings"
	"testing"
)

const (
	tokenProfileSeedFile     = "20260630_003900_seed_token_estimation_profiles.sql"
	tokenProfileRollbackFile = "20260630_003900_seed_token_estimation_profiles_rollback.sql"
	tokenProfileMigrationBy  = "migration:20260630_token_estimation_profiles"
)

var authoritativeTokenProfileRows = []struct {
	provider    string
	model       string
	calibration string
	samples     string
}{
	{"dmxapi", "deepseek-v4-pro", "1.4800", "3050"},
	{"aihubmix", "deepseek-v4-pro", "1.4800", "3050"},
	{"dmxapi", "deepseek-v3.2-thinking", "1.6700", "1102"},
	{"aihubmix", "deepseek-v3.2-thinking", "1.6700", "1102"},
	{"dmxapi", "claude-opus-4-7", "2.1900", "98"},
	{"claude-native", "claude-opus-4-7", "2.1900", "98"},
	{"dmxapi", "claude-sonnet-4-6-thinking", "2.8100", "49"},
	{"aihubmix", "claude-sonnet-4-6-thinking", "2.8100", "49"},
	{"dmxapi", "claude-opus-4-6", "2.6300", "24"},
	{"claude-native", "claude-opus-4-6", "2.6300", "24"},
	{"dmxapi", "gpt-5.5", "1.2900", "16"},
	{"agnes-ai", "agnes-2.0-flash", "1.4800", "65"},
	{"dmxapi", "gemini-3.1-pro-preview", "1.6700", "14"},
	{"aihubmix", "gemini-3.1-pro-preview", "1.6700", "14"},
	{"dmxapi", "gpt-5.4", "1.3100", "0"},
	{"aihubmix", "gpt-5.4", "1.3100", "0"},
	{"ali-dashscope", "qwen-turbo", "1.3000", "0"},
}

func TestTokenEstimationProfileSeed_TargetsProdP95Rows(t *testing.T) {
	seed := executableSQL(t, tokenProfileSeedFile)
	if !strings.Contains(seed, tokenProfileMigrationBy) {
		t.Fatalf("seed must tag rows with updated_by=%q", tokenProfileMigrationBy)
	}
	if !strings.Contains(strings.ToLower(seed), "insert into token_estimation_profile") {
		t.Fatal("seed must insert token_estimation_profile rows")
	}

	for _, r := range authoritativeTokenProfileRows {
		if !strings.Contains(seed, "'"+r.provider+"'") {
			t.Errorf("seed missing provider %q", r.provider)
		}
		if !modelToken(r.model).MatchString(seed) {
			t.Errorf("seed missing model %q", r.model)
		}
		rowPattern := regexp.MustCompile(
			`(?s)'` + regexp.QuoteMeta(r.provider) + `'\s*,\s*'` + regexp.QuoteMeta(r.model) + `'.*?1\.0000\s*,\s*` + regexp.QuoteMeta(r.calibration) + `\s*,\s*` + regexp.QuoteMeta(r.samples),
		)
		if !rowPattern.MatchString(seed) {
			t.Errorf("seed row %s/%s must use safety=1.0000, calibration=%s, samples=%s",
				r.provider, r.model, r.calibration, r.samples)
		}
	}
}

func TestTokenEstimationProfileSeed_DeactivatesPriorExactRows(t *testing.T) {
	seed := strings.ToLower(executableSQL(t, tokenProfileSeedFile))
	if !regexp.MustCompile(`update\s+token_estimation_profile`).MatchString(seed) {
		t.Fatal("seed must deactivate prior active exact profiles before inserting calibrated rows")
	}
	for _, required := range []string{
		"is_active = 0",
		"service_type = 'llm_chat'",
		"is_fallback = 0",
		"is_active = 1",
	} {
		if !strings.Contains(seed, required) {
			t.Errorf("seed deactivation guard missing %q", required)
		}
	}
	if strings.Contains(seed, "is_fallback = 1") {
		t.Error("seed must not deactivate fallback profiles")
	}
}

func TestTokenEstimationProfileSeed_ProfileJSONIncludesEstimatorClasses(t *testing.T) {
	seed := executableSQL(t, tokenProfileSeedFile)
	for _, key := range []string{
		"'method'",
		"'prod-bucketed-p95-20260630'",
		"'message_overhead_tokens'",
		"'fragment_overhead_tokens'",
		"'classes'",
		"'markdown_table'",
		"'mixed'",
		"'calibration_buckets'",
		`"max_raw_tokens"`,
		`"multiplier"`,
	} {
		if !strings.Contains(seed, key) {
			t.Errorf("profile_json missing %s", key)
		}
	}
}

func TestTokenEstimationProfileRollbackScopesToMigrationRows(t *testing.T) {
	rb := executableSQL(t, tokenProfileRollbackFile)
	low := strings.ToLower(rb)
	if !strings.Contains(low, "delete tep") || !strings.Contains(low, "from token_estimation_profile tep") {
		t.Fatal("rollback must delete from token_estimation_profile")
	}
	if !strings.Contains(rb, tokenProfileMigrationBy) {
		t.Fatalf("rollback must scope deletes to updated_by=%q", tokenProfileMigrationBy)
	}
	if !strings.Contains(low, "max(") || !strings.Contains(low, "set tep.is_active = 1") {
		t.Error("rollback should reactivate the newest prior non-migration profile for each affected key")
	}
	for _, r := range authoritativeTokenProfileRows {
		if !strings.Contains(rb, "'"+r.provider+"'") || !modelToken(r.model).MatchString(rb) {
			t.Errorf("rollback missing affected key %s/%s", r.provider, r.model)
		}
	}
}
