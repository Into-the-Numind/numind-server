package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// imageGenTool is the image_gen FullTool.
// Implements prompt-based image generation: the provider call is routed through
// the unified aiservice gateway (aiservice.ImageGen → task profile agent.image_gen
// → dmxapi gemini-2.5-flash-image), and the produced image is uploaded through
// uploadGeneratedFile.
//
// agent-mode-billing T9: each generation is billed via explicit
// Reserve→generate→Reconcile/Refund against the run initiator's credits
// (pool-aware: IsTest runs draw from the admin_test pool). image_gen is non-chat
// so it cannot ride the ContextBudgetCredits chat path; it bills here directly.
// This flat tool-level Reserve/Reconcile is the SINGLE credit deduction — the
// aiservice path (agent-imagegen-via-aiservice) adds observability + a UsageRecord
// (analytics) only and does NOT deduct credits (ImageGenRequest is non-chat, so
// the context_budget middleware skips it; the billing middleware only writes a
// UsageRecord, never a deduction).
type imageGenTool struct {
	BaseTool
	ds            store.IStore
	creditService credit.ICreditService // nil → billing skipped (unit tests)
}

var _ FullTool = (*imageGenTool)(nil)

func (t *imageGenTool) Name() string { return "image_gen" }
func (t *imageGenTool) Description() string {
	return "Generate an image from a text prompt using the Gemini image model."
}
func (t *imageGenTool) UserFacingName() string        { return "图像生成" }
func (t *imageGenTool) NarrationVerb() string         { return "生成" }
func (t *imageGenTool) IsEnabled(cfg ToolConfig) bool { return cfg.EnableImageGen }

func (t *imageGenTool) returnSoftError(format string, args ...any) (ToolResult, error) {
	msg := fmt.Sprintf(format, args...)
	out, _ := json.Marshal(map[string]string{
		"error": "ERROR: " + msg,
	})
	return ToolResult(out), nil
}

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *imageGenTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {"type": "string", "description": "Text description of the image to generate."}
		},
		"required": ["prompt"]
	}`)
}

func (t *imageGenTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	// 1. 解析 Tool 输入
	var inp struct {
		Prompt string `json:"prompt"`
	}
	// Malformed model input must come back soft: a non-nil Go error here is a
	// NodeRunError that kills the whole run (dev run 137).
	if err := json.Unmarshal(input, &inp); err != nil {
		return t.returnSoftError("invalid image_gen input format: %v", err)
	}
	if strings.TrimSpace(inp.Prompt) == "" {
		return t.returnSoftError("image_gen: prompt is required")
	}

	// 1.5 并发上限：同一用户同时最多 imageGenMaxConcurrentPerUser 个文生图请求。
	// 超额直接软返回（不预扣、不生成）。在计费之前检查，避免无谓 Reserve。
	if bc := billing.FromContext(ctx); bc != nil && bc.UserID != 0 {
		if !imageGenConcurrency.acquire(bc.UserID) {
			return t.returnSoftError("你同时最多 %d 个文生图请求，请等前面的完成再试", imageGenMaxConcurrentPerUser)
		}
		defer imageGenConcurrency.release(bc.UserID)
	}

	// 2. 计费：生成前预扣（nil creditService → 跳过计费，保持测试行为）。
	rsvID, billErr := t.reserve(ctx)
	if billErr != nil {
		// Distinguish genuine insufficient-credits from a wiring/system error so
		// the LLM-visible message is accurate (a user with credits hitting a
		// misconfigured ctx should not be told 积分不足).
		if errors.Is(billErr, credit.ErrInsufficientCredits) || errors.Is(billErr, errno.ErrAdminTestExhausted) {
			return t.returnSoftError("积分不足，无法生成图像: %v", billErr)
		}
		return t.returnSoftError("无法生成图像（计费系统错误）: %v", billErr)
	}

	// 3. 生成图像（走统一 aiservice 网关：Langfuse tracing + 路由/降级 + UsageRecord）。
	imgBytes, genErr := t.generateImage(ctx, inp.Prompt)
	if genErr != nil {
		t.refund(ctx, rsvID, "image_gen_failed")
		return t.returnSoftError("%v", genErr)
	}

	// 4. 上传产物。
	filename := defaultImageFilename(time.Now())
	res, uploadErr := uploadGeneratedFile(ctx, imgBytes, "image/png", filename, "png")
	if uploadErr != nil {
		t.refund(ctx, rsvID, "image_gen_upload_failed")
		return t.returnSoftError("failed to upload generated image: %v", uploadErr)
	}

	// 5. 成功：对账扣减（扁平 per-image，无 token 校正）。
	t.reconcile(ctx, rsvID)
	return res, nil
}

// defaultImageFilename builds the default object name for a generated image:
// image-YYYYMMDD-HHMMSS.png. ASCII + date-form so it survives the COS object-key
// sanitize unchanged and reads cleanly when the frontend falls back to it (the LLM
// did not write a markdown alt). Replaces the old gemini-image-{unix} (a giant
// timestamp that also leaked the underlying model name).
func defaultImageFilename(now time.Time) string {
	return fmt.Sprintf("image-%s.png", now.Format("20060102-150405"))
}

// imageGenCredits returns the flat per-image credit cost (estimate == actual).
func imageGenCredits() int64 { return credit.GetEstimatedCredits(string(credit.OpImageGen)) }

// reserve pre-deducts image credits from the run initiator's pool. Returns
// (0, nil) when billing is unwired (tests). Returns (0, err) on insufficient
// credits so Execute surfaces a soft error to the LLM.
func (t *imageGenTool) reserve(ctx context.Context) (uint64, error) {
	if t.creditService == nil {
		return 0, nil // billing not wired (unit tests)
	}
	bc := billing.FromContext(ctx)
	if bc == nil || bc.UserID == 0 {
		return 0, errors.New("missing billing user in context")
	}
	ds := t.ds
	if ds == nil {
		ds = store.S
	}
	if ds == nil {
		return 0, errors.New("store not configured")
	}
	user, err := ds.Users().GetByID(ctx, bc.UserID)
	if err != nil {
		return 0, fmt.Errorf("load user: %w", err)
	}
	rsv, err := t.creditService.ReserveBudget(ctx, user, credit.BudgetReservationInput{
		BudgetPrecheckInput: credit.BudgetPrecheckInput{
			UserID:    bc.UserID,
			Operation: string(credit.OpImageGen),
			Pool:      aismw.BillingPoolFromCtx(ctx), // admin_test for IsTest runs
		},
		EstimatedCredits: imageGenCredits(),
	})
	if err != nil {
		return 0, err
	}
	if rsv == nil {
		return 0, nil
	}
	return rsv.ID, nil
}

func (t *imageGenTool) reconcile(ctx context.Context, rsvID uint64) {
	if t.creditService != nil && rsvID != 0 {
		if err := t.creditService.Reconcile(ctx, rsvID, imageGenCredits()); err != nil {
			// Non-fatal: the reservation sweeper reclaims a stuck reserved row.
			log.Warnw("imageGenTool: Reconcile failed", "reservation_id", rsvID, "error", err)
		}
	}
}

func (t *imageGenTool) refund(ctx context.Context, rsvID uint64, reason string) {
	if t.creditService != nil && rsvID != 0 {
		if err := t.creditService.Refund(ctx, rsvID, reason); err != nil {
			log.Warnw("imageGenTool: Refund failed", "reservation_id", rsvID, "reason", reason, "error", err)
		}
	}
}

// generateImage runs the text-to-image call through the unified aiservice gateway
// and returns the raw PNG bytes, or an error (soft-mapped by the caller). Extracted
// from Execute so billing can wrap it with Reserve/Reconcile/Refund cleanly.
//
// Routing through aiservice.ImageGen gives this call the same Langfuse tracing +
// routing/fallback + UsageRecord (analytics) as Chat/Embed/Rerank. IMPORTANT for
// billing: the aiservice path does NOT deduct credits — its billing middleware
// only writes a UsageRecord, and ImageGenRequest is a non-chat request so the
// context_budget middleware skips it (no ChargeUser policy fires). The single
// credit deduction remains the tool's flat Reserve/Reconcile in Execute, which is
// left untouched around this call.
func (t *imageGenTool) generateImage(ctx context.Context, prompt string) ([]byte, error) {
	resp, err := aiservice.ImageGen(ctx, profile.AgentImageGen, aiservice.ImageGenRequest{
		Prompt:      prompt,
		AspectRatio: "1:1",
	})
	if err != nil {
		return nil, fmt.Errorf("image generation failed: %v", err)
	}
	if resp == nil || strings.TrimSpace(resp.ImageBase64) == "" {
		return nil, errors.New("no image data returned (possibly blocked by safety filters)")
	}

	imgBytes, decErr := base64.StdEncoding.DecodeString(resp.ImageBase64)
	if decErr != nil {
		return nil, fmt.Errorf("failed to decode base64 image data: %v", decErr)
	}
	return imgBytes, nil
}
