package contextbudget

import (
	"math"
	"strings"
	"unicode"
)

// TokenClass holds the token-per-character rate for a character class.
type TokenClass struct {
	// TokenPerChar is the average number of tokens per character for this class.
	TokenPerChar float64 `json:"token_per_char"`
}

// TokenProfile describes how to estimate token counts for a set of fragments.
type TokenProfile struct {
	// Method is a human-readable identifier for this estimation strategy.
	Method string `json:"method"`
	// MessageOverheadTokens is the fixed overhead added per message boundary.
	MessageOverheadTokens int `json:"message_overhead_tokens"`
	// FragmentOverheadTokens is the fixed overhead added per fragment.
	FragmentOverheadTokens int `json:"fragment_overhead_tokens"`
	// Classes maps text class names to their TokenClass configuration.
	// Expected keys: zh, en, code, json, markdown_table, symbol, mixed.
	Classes map[string]TokenClass `json:"classes"`
	// SafetyMultiplier is applied after calibration to add an additional safety margin (>= 1.0).
	SafetyMultiplier float64 `json:"safety_multiplier"`
	// CalibrationMultiplier adjusts the raw estimate to account for model-specific tokenization drift.
	CalibrationMultiplier float64 `json:"calibration_multiplier"`
}

// EstimateResult holds the output of EstimateFragments.
type EstimateResult struct {
	// PromptTokens is the total estimated token count for the entire input.
	PromptTokens int `json:"prompt_tokens"`
	// PerFragmentMap maps each fragment ID to its individual token estimate.
	PerFragmentMap map[string]int `json:"per_fragment_map"`
}

// EstimateFragments estimates the token count for a slice of ContextFragments.
//
// The algorithm (Spec §4.1):
//  1. Classify each fragment's content into character classes (zh/en/code/json/markdown_table/symbol/mixed).
//  2. For each class, compute ceil(char_count * token_per_char).
//  3. Add fragment_overhead_tokens.
//  4. Multiply by calibration_multiplier then safety_multiplier, ceiling the result.
//  5. Total = sum(per_fragment) + message_overhead_tokens * messageCount + fixedOverhead.
func EstimateFragments(fragments []ContextFragment, profile TokenProfile, fixedOverhead int, messageCount int) EstimateResult {
	safetyMul := profile.SafetyMultiplier
	if safetyMul < 1.0 {
		safetyMul = 1.0
	}
	calMul := profile.CalibrationMultiplier
	if calMul <= 0 {
		calMul = 1.0
	}

	perFragment := make(map[string]int, len(fragments))
	total := 0

	for _, f := range fragments {
		est := estimateSingleFragment(f.Content, profile, safetyMul, calMul)
		perFragment[f.ID] = est
		total += est
	}

	total += profile.MessageOverheadTokens * messageCount
	total += fixedOverhead

	return EstimateResult{
		PromptTokens:   total,
		PerFragmentMap: perFragment,
	}
}

// estimateSingleFragment computes the token estimate for a single fragment's content.
func estimateSingleFragment(content string, profile TokenProfile, safetyMul, calMul float64) int {
	if content == "" {
		raw := float64(profile.FragmentOverheadTokens)
		raw = math.Ceil(raw * calMul * safetyMul)
		if int(raw) < 1 {
			return 1
		}
		return int(raw)
	}

	classified := classifyContent(content)

	rawTokens := 0.0
	for class, charCount := range classified {
		tc := tokenClassFor(class, profile)
		rawTokens += math.Ceil(float64(charCount) * tc.TokenPerChar)
	}

	rawTokens += float64(profile.FragmentOverheadTokens)
	rawTokens = math.Ceil(rawTokens * calMul * safetyMul)

	est := int(rawTokens)
	if est < 1 {
		est = 1
	}
	return est
}

// tokenClassFor returns the TokenClass for the given class name,
// falling back to the "en" class or a conservative default if not found.
func tokenClassFor(class string, profile TokenProfile) TokenClass {
	if tc, ok := profile.Classes[class]; ok {
		return tc
	}
	if tc, ok := profile.Classes["en"]; ok {
		return tc
	}
	// Conservative fallback: 1 token per character.
	return TokenClass{TokenPerChar: 1.0}
}

// classifyContent partitions content into character class buckets.
// Returns a map from class name to character count.
//
// Classes (Spec §4.1): zh, en, code, json, markdown_table, symbol, mixed.
// Detection order (highest precedence first):
//  1. json  — whole-string match: trimmed content starts with { or [
//  2. code  — fenced code block ```...``` present in content
//  3. markdown_table — contains "| " patterns (table header/row markers)
//  4. zh    — dominant CJK content
//  5. en    — dominant ASCII alphabetic content
//  6. symbol — dominant punctuation / symbol content
//  7. mixed — everything else
func classifyContent(content string) map[string]int {
	// Detect structural classes first.
	if isJSON(content) {
		return map[string]int{"json": len([]rune(content))}
	}
	if hasCodeBlock(content) {
		return map[string]int{"code": len([]rune(content))}
	}
	if hasMarkdownTable(content) {
		return map[string]int{"markdown_table": len([]rune(content))}
	}

	// Character-level classification.
	runes := []rune(content)
	counts := map[string]int{
		"zh":     0,
		"en":     0,
		"symbol": 0,
		"other":  0,
	}
	for _, r := range runes {
		switch {
		case isCJK(r):
			counts["zh"]++
		case unicode.IsLetter(r) && r <= 0x7F:
			counts["en"]++
		case unicode.IsNumber(r) && r <= 0x7F:
			counts["en"]++
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			counts["symbol"]++
		default:
			counts["other"]++
		}
	}

	total := len(runes)
	if total == 0 {
		return map[string]int{"en": 0}
	}

	zhRatio := float64(counts["zh"]) / float64(total)
	enRatio := float64(counts["en"]) / float64(total)

	// Dominant class threshold: >= 70% of characters.
	const dominantThreshold = 0.70

	if zhRatio >= dominantThreshold {
		return map[string]int{"zh": total}
	}
	if enRatio >= dominantThreshold {
		return map[string]int{"en": total}
	}

	symbolRatio := float64(counts["symbol"]) / float64(total)
	if symbolRatio >= dominantThreshold {
		return map[string]int{"symbol": total}
	}

	// Mixed: split by actual class counts for weighted accuracy.
	result := map[string]int{}
	if counts["zh"] > 0 {
		result["zh"] = counts["zh"]
	}
	if counts["en"] > 0 {
		result["en"] = counts["en"]
	}
	if counts["symbol"] > 0 {
		result["symbol"] = counts["symbol"]
	}
	remainder := counts["other"]
	if len(result) == 0 {
		// Purely "other" characters — treat as mixed.
		result["mixed"] = remainder
	} else if remainder > 0 {
		// Assign remainder to mixed bucket.
		result["mixed"] = remainder
	}
	return result
}

// isJSON returns true if the content looks like a JSON object or array.
func isJSON(content string) bool {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) == 0 {
		return false
	}
	first := trimmed[0]
	last := trimmed[len(trimmed)-1]
	return (first == '{' && last == '}') || (first == '[' && last == ']')
}

// hasCodeBlock returns true if content contains a fenced code block.
func hasCodeBlock(content string) bool {
	return strings.Contains(content, "```")
}

// hasMarkdownTable returns true if content contains markdown table syntax.
func hasMarkdownTable(content string) bool {
	// A markdown table row typically contains "| " or " |".
	count := 0
	for i := 0; i+1 < len(content); i++ {
		if (content[i] == '|' && content[i+1] == ' ') ||
			(content[i] == ' ' && content[i+1] == '|') {
			count++
			if count >= 2 {
				return true
			}
		}
	}
	return false
}

// isCJK returns true for characters in common CJK Unicode ranges.
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0x20000 && r <= 0x2A6DF) || // CJK Extension B
		(r >= 0x2A700 && r <= 0x2B73F) || // CJK Extension C
		(r >= 0x2B740 && r <= 0x2B81F) || // CJK Extension D
		(r >= 0xF900 && r <= 0xFAFF) || // CJK Compatibility Ideographs
		(r >= 0x2F800 && r <= 0x2FA1F) || // CJK Compatibility Supplement
		(r >= 0x3000 && r <= 0x303F) || // CJK Symbols and Punctuation
		(r >= 0xFF00 && r <= 0xFFEF) // Halfwidth and Fullwidth Forms
}
