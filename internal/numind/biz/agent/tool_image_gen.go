package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// imageGenTool is the image_gen FullTool.
// Implements prompt-based image generation via the 'dmxapi' provider's
// gemini-2.5-flash-image model and uploads it through uploadGeneratedFile.
//
// agent-mode-billing T9: each generation is billed via explicit
// Reserve→generate→Reconcile/Refund against the run initiator's credits
// (pool-aware: IsTest runs draw from the admin_test pool). image_gen is non-chat
// so it cannot ride the ContextBudgetCredits chat path; it bills here directly.
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

	// 3. 生成图像（裸 HTTP 打 dmxapi — 收编 aiservice 为后续 follow-up）。
	imgBytes, genErr := t.generateImage(ctx, inp.Prompt)
	if genErr != nil {
		t.refund(ctx, rsvID, "image_gen_failed")
		return t.returnSoftError("%v", genErr)
	}

	// 4. 上传产物。
	filename := fmt.Sprintf("gemini-image-%d.png", time.Now().Unix())
	res, uploadErr := uploadGeneratedFile(ctx, imgBytes, "image/png", filename, "png")
	if uploadErr != nil {
		t.refund(ctx, rsvID, "image_gen_upload_failed")
		return t.returnSoftError("failed to upload generated image: %v", uploadErr)
	}

	// 5. 成功：对账扣减（扁平 per-image，无 token 校正）。
	t.reconcile(ctx, rsvID)
	return res, nil
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

// generateImage runs the dmxapi text-to-image call and returns the raw PNG bytes,
// or an error (soft-mapped by the caller). Extracted from Execute so billing can
// wrap it with Reserve/Reconcile/Refund cleanly.
func (t *imageGenTool) generateImage(ctx context.Context, prompt string) ([]byte, error) {
	ds := t.ds
	if ds == nil {
		ds = store.S
	}
	var db *gorm.DB
	if ds != nil {
		func() {
			defer func() { _ = recover() }()
			db = ds.DB()
		}()
	}
	if db == nil {
		return nil, errors.New("database store context is not configured")
	}

	var provider model.LLMProvider
	if err := db.WithContext(ctx).Where("name = ?", "dmxapi").First(&provider).Error; err != nil {
		return nil, fmt.Errorf("failed to retrieve 'dmxapi' provider config: %v", err)
	}
	if provider.APIKey == "" {
		return nil, errors.New("dmxapi provider API key is not configured in DB")
	}

	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = "https://www.dmxapi.cn"
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}

	var apiURL string
	if strings.Contains(baseURL, "/v1/") {
		apiURL = strings.Replace(baseURL, "/v1/", "/v1beta/models/gemini-2.5-flash-image:generateContent", 1)
	} else if strings.Contains(baseURL, "/v1") {
		apiURL = strings.Replace(baseURL, "/v1", "/v1beta/models/gemini-2.5-flash-image:generateContent", 1)
	} else {
		apiURL = baseURL + "v1beta/models/gemini-2.5-flash-image:generateContent"
	}

	reqBody := map[string]interface{}{
		"contents": []interface{}{
			map[string]interface{}{
				"parts": []interface{}{
					map[string]interface{}{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"imageConfig": map[string]interface{}{"aspectRatio": "1:1"},
		},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("x-goog-api-key", provider.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBuffer bytes.Buffer
		_, _ = errBuffer.ReadFrom(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, errBuffer.String())
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						Data string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response payload: %v", err)
	}

	var base64Data string
	if len(result.Candidates) > 0 && len(result.Candidates[0].Content.Parts) > 0 {
		for _, part := range result.Candidates[0].Content.Parts {
			if part.InlineData.Data != "" {
				base64Data = part.InlineData.Data
				break
			}
		}
	}
	if base64Data == "" {
		return nil, errors.New("no image base64 data returned from API response (possibly blocked by safety filters)")
	}

	imgBytes, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 image data: %v", err)
	}
	return imgBytes, nil
}
