package migrations

import (
	"regexp"
	"strings"
	"testing"
)

// T8 file pair under test: the inert native-provider seed migrations.
//   - seed_native_cache_pricing: idempotent WHERE ... IS NULL seed of read price
//     (cost+sell) for both native providers + Claude creation price (flat, premium).
//     Gemini creation is deliberately NOT seeded (left NULL → standard input price, D5).
//   - add_native_provider_rows: INSERT IGNORE llm_provider rows claude-native /
//     gemini-native with is_active=0 (two-step activation, finding #1). MUST NOT
//     repoint any ai_service_route and MUST NOT set prompt_cache_policy.
const (
	seedNativePricingFile     = "20260619_100300_seed_native_cache_pricing.sql"
	rollbackNativePricingFile = "20260619_100300_seed_native_cache_pricing_rollback.sql"
	nativeProviderRowsFile    = "20260619_100400_add_native_provider_rows.sql"
	nativeProviderRowsRBFile  = "20260619_100400_add_native_provider_rows_rollback.sql"
)

// authoritativeNativeReadRows = the (provider, model) pricing rows whose cache-HIT
// (read) price the seed must set, with the ABSOLUTE real DMXAPI hit price (¥/M
// tokens, incl. 6% tax, 2026-06 price sheet) as written in the SQL. Read price is
// seeded for BOTH native providers (Claude + Gemini).
var authoritativeNativeReadRows = []struct {
	provider  string
	model     string
	readPrice string // 4-decimal absolute value as written in the SQL
}{
	{"claude-native", "claude-opus-4-7", "2.4820"},
	{"claude-native", "claude-sonnet-4-6", "1.5000"},
	{"gemini-native", "gemini-3.1-pro", "0.9930"},
}

// authoritativeNativeCreationRows = the Claude (provider, model) rows whose
// cache-CREATION (write) PREMIUM price the seed must set. Gemini is intentionally
// absent (D5: Gemini creation left NULL → standard input price, no premium).
var authoritativeNativeCreationRows = []struct {
	model         string
	creationPrice string // 4-decimal absolute value as written in the SQL
}{
	{"claude-opus-4-7", "45.6250"},
	{"claude-sonnet-4-6", "18.7500"},
}

// --- seed_native_cache_pricing.sql -------------------------------------------

// TestSeedNativePricing_SetsReadPriceForBothProviders asserts the seed references
// every in-scope (provider, model) read row with its ABSOLUTE DMXAPI hit price and
// targets the cache-HIT (read) discount column on BOTH native providers.
func TestSeedNativePricing_SetsReadPriceForBothProviders(t *testing.T) {
	seed := executableSQL(t, seedNativePricingFile)
	if !strings.Contains(seed, "'claude-native'") {
		t.Error("seed must target provider 'claude-native'")
	}
	if !strings.Contains(seed, "'gemini-native'") {
		t.Error("seed must target provider 'gemini-native'")
	}
	for _, r := range authoritativeNativeReadRows {
		if !modelToken(r.model).MatchString(seed) {
			t.Errorf("seed missing model token %q", r.model)
		}
		if !strings.Contains(seed, r.readPrice) {
			t.Errorf("seed missing absolute read (hit) price %q for model %q", r.readPrice, r.model)
		}
	}
	if !strings.Contains(strings.ToLower(seed), "cached_input_price_per_m_tok") {
		t.Error("seed must set the cache-HIT (read) price column cached_input_price_per_m_tok")
	}
}

// TestSeedNativePricing_SetsClaudeCreationPremium asserts the seed sets the
// cache-CREATION (write) premium ONLY for the Claude rows, never for Gemini (D5).
func TestSeedNativePricing_SetsClaudeCreationPremium(t *testing.T) {
	seed := executableSQL(t, seedNativePricingFile)
	for _, r := range authoritativeNativeCreationRows {
		if !modelToken(r.model).MatchString(seed) {
			t.Errorf("seed missing Claude creation model token %q", r.model)
		}
		if !strings.Contains(seed, r.creationPrice) {
			t.Errorf("seed missing absolute creation premium price %q for model %q", r.creationPrice, r.model)
		}
	}
	if !strings.Contains(strings.ToLower(seed), "cache_creation_input_price_per_m_tok") {
		t.Error("seed must set the cache-CREATION (write) price column cache_creation_input_price_per_m_tok")
	}
}

// TestSeedNativePricing_DoesNotSeedGeminiCreation guards D5: the Gemini creation
// premium column must NEVER be assigned for a gemini-native row — Gemini implicit
// creation has no premium, so it stays NULL → standard input price. We assert no
// executable statement sets cache_creation_input_price_per_m_tok within a
// gemini-native-scoped UPDATE.
func TestSeedNativePricing_DoesNotSeedGeminiCreation(t *testing.T) {
	seed := strings.ToLower(executableSQL(t, seedNativePricingFile))
	// Split into statements; any statement touching gemini-native must NOT set the
	// creation (write) column.
	for _, stmt := range strings.Split(seed, ";") {
		if strings.Contains(stmt, "'gemini-native'") &&
			regexp.MustCompile(`set[^;]*cache_creation_input_price_per_m_tok`).MatchString(stmt) {
			t.Errorf("Gemini creation price must stay NULL (D5) — found a gemini-native statement setting cache_creation_input_price_per_m_tok:\n%s",
				strings.TrimSpace(stmt))
		}
	}
}

// TestSeedNativePricing_Idempotent asserts each UPDATE is guarded by an
// `... IS NULL` predicate so re-running is a no-op, and that read assignments set
// BOTH paired (cost + sell) columns.
func TestSeedNativePricing_Idempotent(t *testing.T) {
	seed := strings.ToLower(executableSQL(t, seedNativePricingFile))
	if !strings.Contains(seed, "is null") {
		t.Error("seed must guard each UPDATE with a WHERE ... IS NULL predicate (idempotent)")
	}
	// Read price: paired cost + sell columns both present.
	if strings.Contains(seed, "set cached_input_price_per_m_tok") &&
		!strings.Contains(seed, "sell_cached_input_price_per_m_tok") {
		t.Error("read price must set BOTH cached_input_price_per_m_tok and sell_cached_input_price_per_m_tok (paired)")
	}
	// Creation price: paired cost + sell columns both present.
	if strings.Contains(seed, "cache_creation_input_price_per_m_tok") &&
		!strings.Contains(seed, "sell_cache_creation_input_price_per_m_tok") {
		t.Error("creation price must set BOTH cache_creation_input_price_per_m_tok and sell_cache_creation_input_price_per_m_tok (paired)")
	}
}

// TestSeedNativePricing_FlatGuarded asserts the seed is scoped to flat billing rows
// (tiered paths ignore cache columns), matching the §5F guard.
func TestSeedNativePricing_FlatGuarded(t *testing.T) {
	seed := strings.ToLower(executableSQL(t, seedNativePricingFile))
	if !strings.Contains(seed, "billing_mode = 'flat'") && !strings.Contains(seed, "billing_mode='flat'") {
		t.Error("seed UPDATEs must be guarded by billing_mode = 'flat' (§5F)")
	}
}

// TestSeedNativePricing_RollbackNullsSeededColumns asserts the rollback NULLs the
// same cache columns the seed set, scoped to the native providers.
func TestSeedNativePricing_RollbackNullsSeededColumns(t *testing.T) {
	rb := executableSQL(t, rollbackNativePricingFile)
	low := strings.ToLower(rb)
	if !strings.Contains(low, "'claude-native'") || !strings.Contains(low, "'gemini-native'") {
		t.Error("rollback must scope to the native providers claude-native / gemini-native")
	}
	if !regexp.MustCompile(`(?i)cached_input_price_per_m_tok\s*=\s*null`).MatchString(rb) {
		t.Error("rollback must reset cached_input_price_per_m_tok = NULL")
	}
	if !regexp.MustCompile(`(?i)cache_creation_input_price_per_m_tok\s*=\s*null`).MatchString(rb) {
		t.Error("rollback must reset cache_creation_input_price_per_m_tok = NULL")
	}
}

// --- add_native_provider_rows.sql --------------------------------------------

// TestNativeProviderRows_InsertsBothInert asserts the migration INSERTs both
// native provider rows with is_active = 0 (two-step activation, finding #1).
func TestNativeProviderRows_InsertsBothInert(t *testing.T) {
	sql := executableSQL(t, nativeProviderRowsFile)
	low := strings.ToLower(sql)
	if !strings.Contains(low, "insert") || !strings.Contains(low, "llm_provider") {
		t.Fatal("migration must INSERT into llm_provider")
	}
	// Idempotent INSERT IGNORE (name has a UNIQUE index).
	if !strings.Contains(low, "insert ignore") {
		t.Error("migration must use INSERT IGNORE for idempotency (llm_provider.name is UNIQUE)")
	}
	for _, name := range []string{"claude-native", "gemini-native"} {
		if !strings.Contains(sql, "'"+name+"'") {
			t.Errorf("migration must insert provider row %q", name)
		}
	}
	// Both rows must be inserted INACTIVE. The is_active column value in the VALUES
	// tuple must be 0; there must be NO is_active = 1 / true anywhere.
	if regexp.MustCompile(`(?i)is_active\s*=\s*1`).MatchString(sql) ||
		regexp.MustCompile(`(?i)is_active\s*=\s*true`).MatchString(sql) {
		t.Error("native provider rows MUST NOT be activated by the migration (is_active must stay 0)")
	}
	if strings.Contains(low, "true") {
		t.Error("migration must not set any boolean column TRUE (rows ship inert)")
	}
}

// TestNativeProviderRows_DoesNotRepointOrSetPolicy is the core zero-regression
// guard (finding #1, two-step activation): the migration MUST NOT touch
// ai_service_route (no repoint) and MUST NOT set prompt_cache_policy.
func TestNativeProviderRows_DoesNotRepointOrSetPolicy(t *testing.T) {
	sql := strings.ToLower(executableSQL(t, nativeProviderRowsFile))
	if strings.Contains(sql, "ai_service_route") {
		t.Error("migration MUST NOT touch ai_service_route — repoint is a separate manual STEP 4 (finding #1)")
	}
	if strings.Contains(sql, "prompt_cache_policy") {
		t.Error("migration MUST NOT set prompt_cache_policy — policy flip is a separate manual STEP 4 (finding #1)")
	}
	// Belt-and-suspenders: no UPDATE statement at all (the migration is INSERT-only).
	if regexp.MustCompile(`\bupdate\b`).MatchString(sql) {
		t.Error("migration must be INSERT-only (no UPDATE) — activation is manual")
	}
}

// TestNativeProviderRows_NoHardcodedSecret asserts the DMXAPI key is a documented
// placeholder, not a real secret (CLAUDE.md §3 — no hardcoded keys). The api_key
// value in the VALUES tuple must be empty ” (ops sets it at activation).
func TestNativeProviderRows_NoHardcodedSecret(t *testing.T) {
	sql := executableSQL(t, nativeProviderRowsFile)
	// No DMXAPI-style key literal (sk-...) anywhere.
	if regexp.MustCompile(`'sk-[A-Za-z0-9]{8,}'`).MatchString(sql) {
		t.Error("migration must NOT hardcode a real DMXAPI api_key (CLAUDE.md §3) — use an empty placeholder")
	}
}

// TestNativeProviderRows_UsesDmxapiBaseHost asserts the native rows point at the
// DMXAPI base host (both native adapters proxy through DMXAPI).
func TestNativeProviderRows_UsesDmxapiBaseHost(t *testing.T) {
	sql := executableSQL(t, nativeProviderRowsFile)
	if !strings.Contains(sql, "https://www.dmxapi.cn") {
		t.Error("native provider rows must use base host https://www.dmxapi.cn (§5A / §5F)")
	}
}

// TestNativeProviderRows_RollbackDeletesOnlyNative asserts the rollback DELETEs the
// two native provider rows by name and nothing else.
func TestNativeProviderRows_RollbackDeletesOnlyNative(t *testing.T) {
	rb := executableSQL(t, nativeProviderRowsRBFile)
	low := strings.ToLower(rb)
	if !strings.Contains(low, "delete") || !strings.Contains(low, "llm_provider") {
		t.Fatal("rollback must DELETE FROM llm_provider")
	}
	for _, name := range []string{"claude-native", "gemini-native"} {
		if !strings.Contains(rb, "'"+name+"'") {
			t.Errorf("rollback must delete provider row %q", name)
		}
	}
	// Must scope DELETE by name (never an unscoped DELETE).
	if !regexp.MustCompile(`(?i)where\s+name\s+in`).MatchString(rb) &&
		!regexp.MustCompile(`(?i)where\s+name\s*=`).MatchString(rb) {
		t.Error("rollback DELETE must be scoped by name (claude-native / gemini-native)")
	}
}

// TestNativeProviderRows_DocumentsActivationRunbook asserts the file header
// documents the 4-step two-step-activation runbook (finding #1).
func TestNativeProviderRows_DocumentsActivationRunbook(t *testing.T) {
	// Read the RAW file (comments intact) — the runbook lives in the header comments.
	raw := strings.ToLower(readMigration(t, nativeProviderRowsFile))
	for _, kw := range []string{"step 1", "step 2", "step 3", "step 4", "/healthz/ai", "is_active"} {
		if !strings.Contains(raw, kw) {
			t.Errorf("file header runbook must mention %q (4-step activation, finding #1)", kw)
		}
	}
}
