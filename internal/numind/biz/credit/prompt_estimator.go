// Package credit — prompt_estimator.go implements IPromptEstimator per spec
// §3.11 (HTTP /v1/credits/estimate backend-rendered prompt sizing).
//
// Architectural note (business-logic.md §1): "业务逻辑统一放 biz 层". The
// estimator lives here, not in controller/, because it needs to traverse
// SOP templates and Sales sessions via the store layer.
package credit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// IPromptEstimator is the biz-layer contract for turning an (operation,
// reference_id) pair into a prompt-char count and the model/provider that
// will be used for the LLM call. The result feeds ICreditService
// .CheckAndEstimate (Task C.5).
//
// Accuracy contract (spec §3.11):
//
//   - sop_run:      Iterate SopTemplate's SopNode rows, sum len(Prompt) +
//                   len(Description) + len(Name). This over-estimates by
//                   a small margin (variable substitutions aren't yet
//                   rendered) which is fine — safety_buffer_pct covers it.
//   - sop_chat:     Use the execution context of the SopRun (reference_id
//                   is the sop_run.id). Fall back to template sum if context
//                   is empty.
//   - salesrag_chat: Sum message.content over the last N messages of the
//                   session (default N=10).
//   - default:      Return 0 char count and empty model/provider. Caller
//                   should fill in defaults and accept the reduced-accuracy
//                   estimate.
type IPromptEstimator interface {
	Estimate(ctx context.Context, operation, referenceID string) (
		chars int, modelName string, provider string, err error,
	)
}

type promptEstimator struct {
	ds store.IStore
}

// NewPromptEstimator constructs the biz-layer estimator. It uses the existing
// store.IStore singletons so callers do not need to inject separate SOP /
// sales session stores.
func NewPromptEstimator(ds store.IStore) IPromptEstimator {
	return &promptEstimator{ds: ds}
}

// salesragRecentMessages is the window size used for salesrag_chat
// estimation. Ten turns approximates an average user session's context.
const salesragRecentMessages = 10

func (p *promptEstimator) Estimate(ctx context.Context, operation, referenceID string) (int, string, string, error) {
	switch Operation(operation) {
	case OpSopRun:
		return p.estimateSopRun(ctx, referenceID)
	case OpSopChat:
		return p.estimateSopChat(ctx, referenceID)
	case OpSalesragChat:
		return p.estimateSalesragChat(ctx, referenceID)
	case OpProfileAnalysis, OpFileParse, OpStyleAnalysis, OpOCR:
		// These ops are not yet wired into CheckAndEstimate — return zero
		// and let the caller fall back to a conservative default via the
		// global coefficient fallback row.
		return 0, "", "", nil
	default:
		return 0, "", "", fmt.Errorf("promptEstimator: unknown operation %q", operation)
	}
}

// estimateSopRun sums the prompt+description+name chars across all nodes in
// the template. Returns (chars, modelName, provider) — modelName is taken
// from the first node (most templates use one model for all nodes). Provider
// is derived from the model name (best-effort; empty if unknown).
func (p *promptEstimator) estimateSopRun(_ context.Context, referenceID string) (int, string, string, error) {
	templateID, err := parseUintRef(referenceID)
	if err != nil {
		return 0, "", "", fmt.Errorf("sop_run: invalid reference_id: %w", err)
	}

	tmpl, err := p.ds.Sop().GetTemplate(templateID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, "", "", fmt.Errorf("sop_run: template %d not found", templateID)
		}
		return 0, "", "", fmt.Errorf("sop_run: load template: %w", err)
	}
	nodes, err := p.ds.Sop().ListNodesByTemplate(templateID)
	if err != nil {
		return 0, "", "", fmt.Errorf("sop_run: list nodes: %w", err)
	}

	chars := utf8.RuneCountInString(tmpl.Prompt) + utf8.RuneCountInString(tmpl.Description)
	modelName := ""
	for i, n := range nodes {
		chars += utf8.RuneCountInString(n.Prompt) +
			utf8.RuneCountInString(n.Description) +
			utf8.RuneCountInString(n.Name)
		if i == 0 {
			modelName = n.ModelName
		}
	}
	return chars, modelName, ProviderFromModel(modelName), nil
}

// estimateSopChat uses the SopRun's chat message history. reference_id is
// the sop_run.id. If no messages exist, fall back to the template char sum.
//
// Note: we query sop_run directly rather than calling p.ds.Sop().GetRun(),
// which Preloads("User") — User uses MySQL ENUM types that don't parse in
// the in-memory SQLite test harness, and we don't need the User object
// here anyway.
func (p *promptEstimator) estimateSopChat(ctx context.Context, referenceID string) (int, string, string, error) {
	runID, err := parseUintRef(referenceID)
	if err != nil {
		return 0, "", "", fmt.Errorf("sop_chat: invalid reference_id: %w", err)
	}
	var run model.SopRun
	if err := p.ds.DB().WithContext(ctx).
		Select("id, template_id, user_id").
		First(&run, runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, "", "", fmt.Errorf("sop_chat: run %d not found", runID)
		}
		return 0, "", "", fmt.Errorf("sop_chat: load run: %w", err)
	}
	msgs, err := p.ds.Sop().ListChatMessagesByRun(runID)
	if err != nil {
		return 0, "", "", fmt.Errorf("sop_chat: list messages: %w", err)
	}
	chars := 0
	modelName := ""
	for _, m := range msgs {
		chars += utf8.RuneCountInString(m.Content)
		if modelName == "" && m.ModelName != "" {
			modelName = m.ModelName
		}
	}
	if chars == 0 {
		// Empty chat — estimate against the template instead.
		return p.estimateSopRun(ctx, strconv.FormatUint(uint64(run.TemplateID), 10))
	}
	return chars, modelName, ProviderFromModel(modelName), nil
}

// estimateSalesragChat sums the last N messages of the session. reference_id
// is the sales_session.id. We query sales_message directly because the
// salesSessionStore's ListMessages enforces a user_id scope and the estimator
// has already been authenticated at the HTTP layer.
func (p *promptEstimator) estimateSalesragChat(ctx context.Context, referenceID string) (int, string, string, error) {
	sessionID, err := parseUintRef(referenceID)
	if err != nil {
		return 0, "", "", fmt.Errorf("salesrag_chat: invalid reference_id: %w", err)
	}
	var msgs []model.SalesMessage
	if err := p.ds.DB().WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("created_at DESC").
		Limit(salesragRecentMessages).
		Find(&msgs).Error; err != nil {
		return 0, "", "", fmt.Errorf("salesrag_chat: list messages: %w", err)
	}
	chars := 0
	for _, m := range msgs {
		chars += utf8.RuneCountInString(m.Content)
	}
	// salesrag model/provider aren't persisted per-message; callers use the
	// service defaults (qwen-turbo / ali typically). Return empty strings so
	// the caller sees "use defaults" rather than a stale first-token guess.
	return chars, "", "", nil
}

// parseUintRef turns a stringy reference_id into a uint for store lookup.
func parseUintRef(s string) (uint, error) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(n), nil
}

// Type guard — ensure promptEstimator satisfies IPromptEstimator.
var _ IPromptEstimator = (*promptEstimator)(nil)
