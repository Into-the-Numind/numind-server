package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Seed/rollback file pair under test — cached-input pricing for the Batch A
// DMXAPI flat rows. REWRITTEN 2026-06-10: the seed now uses ABSOLUTE real DMXAPI
// prices (透传 pass-through), not a ratio-of-base formula, and targets the rows
// that actually exist (verified against the live dev DB + DMXAPI price sheet).
const (
	seedCachedPricingFile     = "20260609_121500_seed_cached_input_pricing.sql"
	rollbackCachedPricingFile = "20260609_121500_seed_cached_input_pricing_rollback.sql"
)

// authoritativeCachedSeedRows = the EXACT DMXAPI flat Batch-A (provider, model)
// rows the seed must touch, with the ABSOLUTE DMXAPI cache-HIT price (¥/M tokens)
// as it appears in the SQL. Verified against live dev DB + DMXAPI price sheet
// (2026-06). Provider is always 'dmxapi' (aihubmix has no price sheet → left
// NULL → full price → zero regression). gpt-5.4 is flat-guarded (dev has it
// tiered → won't match; the tier path ignores the cached column anyway).
var authoritativeCachedSeedRows = []struct {
	model    string
	hitPrice string // 4-decimal absolute value as written in the SQL
}{
	{"deepseek-v4-pro", "0.0250"},
	{"deepseek-v3.2", "0.2000"},
	{"deepseek-v3.2-thinking", "0.2000"},
	{"gpt-5.5", "2.4820"},
	{"gpt-5.4", "1.2500"},
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// executableSQL strips `--` comments so checks inspect only real statements, not
// documentation prose (which may legitimately mention out-of-scope rows to
// explain WHY they are excluded, e.g. aihubmix).
func executableSQL(t *testing.T, name string) string {
	t.Helper()
	var b strings.Builder
	for _, line := range strings.Split(readMigration(t, name), "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// modelToken matches a quoted model literal as a whole token, so 'deepseek-v3.2'
// does not accidentally satisfy a check for 'deepseek-v3.2-thinking'.
func modelToken(model string) *regexp.Regexp {
	return regexp.MustCompile(`'` + regexp.QuoteMeta(model) + `'`)
}

// TestSeedCachedPricing_TargetsRealRowsWithAbsolutePrices asserts the seed
// targets provider 'dmxapi' and references every in-scope model token together
// with its ABSOLUTE real DMXAPI hit price.
func TestSeedCachedPricing_TargetsRealRowsWithAbsolutePrices(t *testing.T) {
	seed := executableSQL(t, seedCachedPricingFile)
	if !strings.Contains(seed, "'dmxapi'") {
		t.Error("seed must target provider 'dmxapi'")
	}
	for _, r := range authoritativeCachedSeedRows {
		if !modelToken(r.model).MatchString(seed) {
			t.Errorf("seed missing model token %q", r.model)
		}
		if !strings.Contains(seed, r.hitPrice) {
			t.Errorf("seed missing absolute hit price %q for model %q", r.hitPrice, r.model)
		}
	}
}

// TestSeedCachedPricing_TransparentNotRatio asserts the seed uses absolute DMXAPI
// prices (透传), NOT the prior buggy ratio-of-base formula. Each model's cache
// price differs and is NOT a fixed multiple of input.
func TestSeedCachedPricing_TransparentNotRatio(t *testing.T) {
	seed := strings.ToLower(executableSQL(t, seedCachedPricingFile))
	if regexp.MustCompile(`round\(\s*input_price_per_m_tok\s*\*`).MatchString(seed) ||
		regexp.MustCompile(`round\(\s*sell_input_price_per_m_tok\s*\*`).MatchString(seed) {
		t.Error("seed must NOT derive cached price as a ratio of base price (use absolute DMXAPI 透传 prices)")
	}
}

// TestSeedCachedPricing_DoesNotTouchAihubmixOrBase guards scope: aihubmix routes
// stay NULL (no price sheet), and the base input/sell price columns are never
// modified (base 不碰).
func TestSeedCachedPricing_DoesNotTouchAihubmixOrBase(t *testing.T) {
	for _, f := range []string{seedCachedPricingFile, rollbackCachedPricingFile} {
		body := strings.ToLower(executableSQL(t, f))
		if strings.Contains(body, "aihubmix") {
			t.Errorf("%s must NOT touch aihubmix routes (no price sheet → leave NULL/full price)", f)
		}
		if regexp.MustCompile(`set\s+input_price_per_m_tok`).MatchString(body) ||
			regexp.MustCompile(`set\s+sell_input_price_per_m_tok`).MatchString(body) {
			t.Errorf("%s must NOT modify base input/sell price columns (base 不碰)", f)
		}
	}
}

// TestSeedCachedPricing_Idempotent asserts each UPDATE is guarded by
// WHERE cached_input_price_per_m_tok IS NULL and always sets BOTH paired columns.
func TestSeedCachedPricing_Idempotent(t *testing.T) {
	seed := strings.ToLower(executableSQL(t, seedCachedPricingFile))
	if !strings.Contains(seed, "cached_input_price_per_m_tok is null") {
		t.Error("seed must guard each UPDATE with WHERE cached_input_price_per_m_tok IS NULL (idempotent)")
	}
	setCount := strings.Count(seed, "set cached_input_price_per_m_tok")
	if setCount == 0 {
		t.Fatal("seed must contain at least one UPDATE ... SET cached_input_price_per_m_tok")
	}
	if got := strings.Count(seed, "sell_cached_input_price_per_m_tok"); got < setCount {
		t.Errorf("paired columns: %d cost-sets but only %d sell assignments", setCount, got)
	}
}

// TestSeedCachedPricing_RollbackMatchesSeed asserts the rollback NULLs the same
// model set the seed touches.
func TestSeedCachedPricing_RollbackMatchesSeed(t *testing.T) {
	rb := executableSQL(t, rollbackCachedPricingFile)
	for _, r := range authoritativeCachedSeedRows {
		if !modelToken(r.model).MatchString(rb) {
			t.Errorf("rollback missing model token %q", r.model)
		}
	}
	if !regexp.MustCompile(`(?i)cached_input_price_per_m_tok\s*=\s*null`).MatchString(rb) {
		t.Error("rollback must reset cached_input_price_per_m_tok = NULL")
	}
}
