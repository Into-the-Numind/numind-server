package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Seed/rollback file pair under test (Task 4 — seed cached pricing for the
// Batch A DeepSeek / OpenAI GPT flat pricing_rule rows).
const (
	seedCachedPricingFile     = "20260609_121500_seed_cached_input_pricing.sql"
	rollbackCachedPricingFile = "20260609_121500_seed_cached_input_pricing_rollback.sql"
)

// authoritativeCachedSeedRows is the EXACT set of live flat (provider, model)
// pricing_rule rows the cached-input seed must touch. Derived from the actual
// migration history (the single source of truth for what rows exist), NOT from
// any single migration's comment:
//
//	volc-ark / deepseek-v3-2-251201    seed_pricing_rules.sql:18  (¥1.2184, SOP 主力, optional)
//	dmxapi   / deepseek-v3-2-251201    seed_pricing_rules.sql:24  (¥1.00,   SalesRAG)
//	dmxapi   / deepseek-v4-pro         20260424_204500:114        (¥14.00)
//	aihubmix / deepseek-v4-pro         20260424_204500:120        (¥14.00)
//	dmxapi   / DeepSeek-V3.2           20260419_170000:38         (¥2.16)
//	dmxapi   / DeepSeek-V3.2-Thinking  20260419_170000:40         (¥2.16)
//	aihubmix / deepseek-v3.2           20260416_100000:139        (¥2.16)
//	aihubmix / deepseek-v3.2-thinking  20260420_030000:43         (¥2.16)
//	dmxapi-ssvip / deepseek-v3.2       20260418_170000:91         (¥2.16, copied from aihubmix)
//	dmxapi   / gpt-5.4                 20260419_170000:46         (¥10.00)
//
// All are billing_mode='flat'. tiered_token GPT rows (aihubmix/gpt-5.4[-thinking],
// dmxapi-ssvip/gpt-5.4) are intentionally OUT (the tier path never reads the cached
// column → full price → zero regression).
var authoritativeCachedSeedRows = []struct {
	provider string
	model    string
}{
	{"volc-ark", "deepseek-v3-2-251201"},
	{"dmxapi", "deepseek-v3-2-251201"},
	{"dmxapi", "deepseek-v4-pro"},
	{"aihubmix", "deepseek-v4-pro"},
	{"dmxapi", "DeepSeek-V3.2"},
	{"dmxapi", "DeepSeek-V3.2-Thinking"},
	{"aihubmix", "deepseek-v3.2"},
	{"aihubmix", "deepseek-v3.2-thinking"},
	{"dmxapi-ssvip", "deepseek-v3.2"},
	{"dmxapi", "gpt-5.4"},
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// executableSQL strips full-line and trailing `--` comments so phantom-row
// checks inspect only the actual statements, not documentation prose (which may
// legitimately mention out-of-scope models to explain WHY they are excluded).
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

// modelToken builds a regex that matches a quoted model literal as a whole
// token, so model='deepseek-v3.2' does not accidentally satisfy a check for
// 'deepseek-v3.2-thinking' (or vice-versa).
func modelToken(model string) *regexp.Regexp {
	return regexp.MustCompile(`'` + regexp.QuoteMeta(model) + `'`)
}

// TestSeedCachedPricing_TargetsAllAuthoritativeRows asserts the seed file
// references every in-scope live flat row by its EXACT (provider, model)
// strings. This is the regression guard against the prior draft that targeted
// phantom rows (lowercase dmxapi deepseek-v3.2, a non-existent dmxapi/gpt-5.5)
// and silently missed the production SOP/SalesRAG DeepSeek rows.
func TestSeedCachedPricing_TargetsAllAuthoritativeRows(t *testing.T) {
	seed := executableSQL(t, seedCachedPricingFile)
	for _, r := range authoritativeCachedSeedRows {
		if !strings.Contains(seed, "'"+r.provider+"'") {
			t.Errorf("seed missing provider %q (model %q)", r.provider, r.model)
		}
		if !modelToken(r.model).MatchString(seed) {
			t.Errorf("seed missing model token %q (provider %q)", r.model, r.provider)
		}
	}
}

// TestSeedCachedPricing_RollbackMatchesSeed asserts the rollback resets the
// exact same (provider, model) tuple set the seed touches.
func TestSeedCachedPricing_RollbackMatchesSeed(t *testing.T) {
	rb := executableSQL(t, rollbackCachedPricingFile)
	for _, r := range authoritativeCachedSeedRows {
		if !strings.Contains(rb, "'"+r.provider+"'") {
			t.Errorf("rollback missing provider %q (model %q)", r.provider, r.model)
		}
		if !modelToken(r.model).MatchString(rb) {
			t.Errorf("rollback missing model token %q (provider %q)", r.model, r.provider)
		}
	}
}

// TestSeedCachedPricing_NoPhantomRows asserts the seed/rollback never reference
// models that have NO live flat pricing_rule row, which would make the WHERE
// match zero rows and silently no-op:
//   - gpt-5.5: test fixture only (completion_estimate_test.go), no pricing_rule.
//   - gpt-5.4 tiered rows live under aihubmix / dmxapi-ssvip, but those are
//     tiered_token; only dmxapi/gpt-5.4 is flat. We assert no GPT model other
//     than gpt-5.4 is referenced.
func TestSeedCachedPricing_NoPhantomRows(t *testing.T) {
	for _, f := range []string{seedCachedPricingFile, rollbackCachedPricingFile} {
		body := strings.ToLower(executableSQL(t, f))
		if strings.Contains(body, "gpt-5.5") {
			t.Errorf("%s references gpt-5.5 which has no live pricing_rule row", f)
		}
		// Any gpt-5.x token other than gpt-5.4 is a phantom for this seed.
		for _, m := range regexp.MustCompile(`gpt-5\.\d+`).FindAllString(body, -1) {
			if m != "gpt-5.4" {
				t.Errorf("%s references unexpected GPT model %q (only flat dmxapi/gpt-5.4 is in scope)", f, m)
			}
		}
	}
}

// TestSeedCachedPricing_RatioOfActualPrice asserts the seed derives the cached
// price as a RATIO of the row's own stored price (ROUND(input_price_per_m_tok *
// <ratio>, ...)) rather than hardcoding absolute values — so it stays correct
// whatever the live base price is, and uses the DeepSeek 0.1× / GPT 0.5× ratios.
func TestSeedCachedPricing_RatioOfActualPrice(t *testing.T) {
	seed := executableSQL(t, seedCachedPricingFile)
	lower := strings.ToLower(seed)
	if !regexp.MustCompile(`round\(\s*input_price_per_m_tok\s*\*`).MatchString(lower) {
		t.Error("seed must derive cost cached price as ROUND(input_price_per_m_tok * ratio, ...)")
	}
	if !regexp.MustCompile(`round\(\s*sell_input_price_per_m_tok\s*\*`).MatchString(lower) {
		t.Error("seed must derive sell cached price as ROUND(sell_input_price_per_m_tok * ratio, ...)")
	}
	if !strings.Contains(lower, "* 0.1") {
		t.Error("seed must use the 0.1× DeepSeek cache-hit ratio")
	}
	if !strings.Contains(lower, "* 0.5") {
		t.Error("seed must use the 0.5× OpenAI GPT cached-input ratio")
	}
}

// TestSeedCachedPricing_Idempotent asserts the seed only writes rows whose
// cached price is still NULL (never overwrites operator edits / safe re-run)
// and always sets BOTH paired columns (cost + sell) so a partial-set can never
// occur.
func TestSeedCachedPricing_Idempotent(t *testing.T) {
	seed := executableSQL(t, seedCachedPricingFile)
	lower := strings.ToLower(seed)
	if !strings.Contains(lower, "cached_input_price_per_m_tok is null") {
		t.Error("seed must guard each UPDATE with WHERE cached_input_price_per_m_tok IS NULL (idempotent)")
	}
	// Paired columns: every `SET cached_input_price_per_m_tok` (one per UPDATE)
	// must be accompanied by an assignment of the sell column, so a cost-only
	// (partial) set can never occur.
	setCount := strings.Count(lower, "set cached_input_price_per_m_tok")
	if setCount == 0 {
		t.Fatal("seed must contain at least one UPDATE ... SET cached_input_price_per_m_tok")
	}
	if got := strings.Count(lower, "sell_cached_input_price_per_m_tok"); got < setCount {
		t.Errorf("seed must set the paired sell_cached_input_price_per_m_tok in every UPDATE: %d cost-sets but %d sell assignments", setCount, got)
	}
}
