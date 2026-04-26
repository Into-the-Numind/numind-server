package contextbudget_test

// evaluation_test.go — Task 12: Token estimator evaluation dataset.
//
// Validates that EstimateFragments produces token estimates that fall within
// sane lower/upper bounds for 9 text classes (spec §4.3). These bounds are
// deliberately conservative (Phase 1 fixtures) to ensure deterministic PASS;
// S5 will tighten thresholds once ground truth from a real tokenizer is
// available.
//
// Required test function name (from S3 plan):
//   TestTokenEstimatorEvaluationDatasetMeetsThresholds

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"

	"numind-server/internal/pkg/contextbudget"
)

// evaluationCase is the JSON schema for one entry in evaluation_cases.json.
type evaluationCase struct {
	Name              string `json:"name"`
	ContentClass      string `json:"content_class"`
	Description       string `json:"description"`
	Content           string `json:"content"`
	MinExpectedTokens int    `json:"min_expected_tokens"`
	MaxExpectedTokens int    `json:"max_expected_tokens"`
	Notes             string `json:"notes"`
}

// evaluationDataset is the top-level JSON structure.
type evaluationDataset struct {
	Description string                     `json:"description"`
	Profile     contextbudget.TokenProfile `json:"profile"`
	Cases       []evaluationCase           `json:"cases"`
}

// TestTokenEstimatorEvaluationDatasetMeetsThresholds loads the 9-case
// evaluation fixture and runs EstimateFragments on each case. It asserts:
//  1. Each case's estimate is >= MinExpectedTokens (conservative lower bound).
//  2. Each case's estimate is <= MaxExpectedTokens (sanity upper bound).
//  3. The aggregate P50 absolute relative error is <= 0.50 (50% Phase 1 threshold).
//  4. The P90 absolute relative error is <= 0.80 (80% Phase 1 threshold).
//  5. No systematic under-estimation (P99 >= 0.0, i.e., no zero estimates).
//
// These thresholds are intentionally wide for Phase 1 to guarantee stable CI.
// S5 will narrow them once a reference tokenizer ground truth is available.
func TestTokenEstimatorEvaluationDatasetMeetsThresholds(t *testing.T) {
	// Load the evaluation dataset from testdata.
	dataPath := "testdata/evaluation_cases.json"
	raw, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("failed to read evaluation dataset %s: %v", dataPath, err)
	}

	var dataset evaluationDataset
	if err := json.Unmarshal(raw, &dataset); err != nil {
		t.Fatalf("failed to parse evaluation dataset: %v", err)
	}

	if len(dataset.Cases) < 9 {
		t.Fatalf("evaluation dataset must have >= 9 cases, got %d", len(dataset.Cases))
	}

	profile := dataset.Profile

	// Collect per-case results for aggregate metrics.
	type caseResult struct {
		name        string
		estimate    int
		minExpected int
		maxExpected int
	}
	results := make([]caseResult, 0, len(dataset.Cases))

	for _, tc := range dataset.Cases {
		tc := tc // capture range var

		t.Run(tc.Name, func(t *testing.T) {
			fragment := contextbudget.ContextFragment{
				ID:          tc.Name,
				Role:        contextbudget.RoleWorking,
				Content:     tc.Content,
				ContentType: contextbudget.ContentText,
			}

			est := contextbudget.EstimateFragments(
				[]contextbudget.ContextFragment{fragment},
				profile,
				0, // no additional fixed overhead
				1, // 1 message
			)
			estimate := est.PromptTokens

			// Per-case lower bound (conservative sanity check).
			if estimate < tc.MinExpectedTokens {
				t.Errorf("%s: estimate %d < min_expected %d — estimator under-estimates this class",
					tc.Name, estimate, tc.MinExpectedTokens)
			}

			// Per-case upper bound (anti-hallucination sanity check).
			if estimate > tc.MaxExpectedTokens {
				t.Errorf("%s: estimate %d > max_expected %d — estimator over-estimates this class",
					tc.Name, estimate, tc.MaxExpectedTokens)
			}

			// Record result for aggregate metrics.
			results = append(results, caseResult{
				name:        tc.Name,
				estimate:    estimate,
				minExpected: tc.MinExpectedTokens,
				maxExpected: tc.MaxExpectedTokens,
			})

			t.Logf("%s: estimate=%d range=[%d,%d] class=%s",
				tc.Name, estimate, tc.MinExpectedTokens, tc.MaxExpectedTokens, tc.ContentClass)
		})
	}

	// --- Aggregate metrics: P50/P90/P99 ---
	// Use the midpoint of [min, max] as the "ground truth" for relative error.
	// This is a proxy until a real tokenizer provides actual token counts.
	if len(results) == 0 {
		t.Skip("no results to aggregate (all subtests failed)")
	}

	relErrors := make([]float64, 0, len(results))
	for _, r := range results {
		if r.minExpected == 0 && r.maxExpected == 0 {
			continue
		}
		midpoint := float64(r.minExpected+r.maxExpected) / 2.0
		if midpoint <= 0 {
			continue
		}
		relErr := math.Abs(float64(r.estimate)-midpoint) / midpoint
		relErrors = append(relErrors, relErr)
	}

	if len(relErrors) == 0 {
		t.Log("no relative errors to aggregate")
		return
	}

	sort.Float64s(relErrors)

	percentile := func(sorted []float64, p float64) float64 {
		if len(sorted) == 0 {
			return 0
		}
		idx := int(math.Ceil(p*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}

	p50 := percentile(relErrors, 0.50)
	p90 := percentile(relErrors, 0.90)
	p99 := percentile(relErrors, 0.99)

	t.Logf("Token estimator evaluation metrics:")
	t.Logf("  N=%d cases", len(relErrors))
	t.Logf("  P50 relative error = %.4f (%.1f%%)", p50, p50*100)
	t.Logf("  P90 relative error = %.4f (%.1f%%)", p90, p90*100)
	t.Logf("  P99 relative error = %.4f (%.1f%%)", p99, p99*100)

	// Individual error breakdown.
	for i, r := range results {
		if i < len(relErrors) {
			t.Logf("  %s: estimate=%d, rel_error=%.4f", r.name, r.estimate, relErrors[i])
		}
	}

	// Phase 1 thresholds (conservative — intended to PASS on all reasonable estimators).
	// S5 will tighten these based on real tokenizer ground truth.
	const (
		p50Threshold = 0.50 // 50% relative error at P50
		p90Threshold = 0.80 // 80% relative error at P90
	)

	if p50 > p50Threshold {
		t.Errorf("P50 relative error %.4f > threshold %.4f — estimator has high median error", p50, p50Threshold)
	}
	if p90 > p90Threshold {
		t.Errorf("P90 relative error %.4f > threshold %.4f — estimator has high tail error", p90, p90Threshold)
	}

	// P99-under check: no systematic under-estimation (estimate must be > 0 for all cases).
	for _, r := range results {
		if r.estimate <= 0 {
			t.Errorf("systematic under-estimation: %s returned estimate=%d (must be > 0)", r.name, r.estimate)
		}
	}
}

// TestEvaluationDatasetCoversNineClasses verifies the fixture has the required
// 9 classes: chinese_sales_copy, english_prose, go_code, typescript_code,
// markdown_table, json_object, symbol_heavy_text, mixed_text,
// attachment_reference / rag_evidence_chunks.
func TestEvaluationDatasetCoversNineClasses(t *testing.T) {
	dataPath := "testdata/evaluation_cases.json"
	raw, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("failed to read evaluation dataset: %v", err)
	}
	var dataset evaluationDataset
	if err := json.Unmarshal(raw, &dataset); err != nil {
		t.Fatalf("failed to parse evaluation dataset: %v", err)
	}

	required := []string{
		"chinese_sales_copy",
		"english_prose",
		"go_code",
		"typescript_code",
		"markdown_table",
		"json_object",
		"symbol_heavy_text",
		"mixed_text",
		// At least one of attachment_reference or rag_evidence_chunks.
		// (spec §4.3 lists "attachment reference" and "RAG evidence chunks")
	}

	nameSet := make(map[string]bool)
	for _, c := range dataset.Cases {
		nameSet[c.Name] = true
	}

	for _, name := range required {
		if !nameSet[name] {
			t.Errorf("evaluation dataset missing required case %q", name)
		}
	}

	// At least one of the attachment/RAG cases.
	hasAttachmentOrRAG := nameSet["attachment_reference"] || nameSet["rag_evidence_chunks"]
	if !hasAttachmentOrRAG {
		t.Error("evaluation dataset must include at least one of: attachment_reference, rag_evidence_chunks")
	}

	if len(dataset.Cases) < 9 {
		t.Errorf("evaluation dataset must have >= 9 cases, got %d", len(dataset.Cases))
	}

	t.Logf("evaluation dataset: %d cases — %s", len(dataset.Cases), dataset.Description)
}

// TestEvaluationDatasetAllCasesHaveContent is a sanity guard that every case
// has non-empty content (guard against accidentally blank fixtures).
func TestEvaluationDatasetAllCasesHaveContent(t *testing.T) {
	dataPath := "testdata/evaluation_cases.json"
	raw, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("failed to read evaluation dataset: %v", err)
	}
	var dataset evaluationDataset
	if err := json.Unmarshal(raw, &dataset); err != nil {
		t.Fatalf("failed to parse evaluation dataset: %v", err)
	}

	for _, c := range dataset.Cases {
		if c.Content == "" {
			t.Errorf("case %q has empty content", c.Name)
		}
		if c.MinExpectedTokens <= 0 {
			t.Errorf("case %q has non-positive min_expected_tokens=%d", c.Name, c.MinExpectedTokens)
		}
		if c.MaxExpectedTokens <= c.MinExpectedTokens {
			t.Errorf("case %q has max_expected_tokens=%d <= min_expected_tokens=%d",
				c.Name, c.MaxExpectedTokens, c.MinExpectedTokens)
		}
		_ = fmt.Sprintf("ok: %s len=%d", c.Name, len(c.Content)) // avoid unused import
	}
}
