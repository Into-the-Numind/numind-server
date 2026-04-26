// Package admin_contextbudget provides Admin API handlers for the Context Budget feature.
//
// Endpoints (all under /v1/admin/context-budget/):
//
//	GET  /token-profiles                            — list active token estimation profiles
//	POST /token-profiles                            — create (save new version)
//	PUT  /token-profiles/:id                        — update (save new version by lookup key)
//	DELETE /token-profiles/:id                      — soft-deactivate a specific version
//	GET  /token-profiles/history                    — version history by provider/model/service_type
//	GET  /policies                                  — list active budget policies
//	PUT  /policies/:operation                       — upsert policy for an operation
//	POST /preview                                   — preview budget math for a service+policy config
//	GET  /events                                    — recent context budget events (metadata only)
package admin_contextbudget

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/aiservice_admin"
	bizcb "numind-server/internal/numind/biz/contextbudget"
	"numind-server/internal/numind/store"
	cbpkg "numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ContextBudgetController handles admin CRUD for token profiles, budget policies,
// the preview helper, and the events audit log.
type ContextBudgetController struct {
	cbBiz    *bizcb.Biz
	cbStore  store.ContextBudgetStore
	aiSvcBiz aiservice_admin.IAIServiceAdminBiz
	db       *gorm.DB
}

// New creates a new ContextBudgetController.
func New(
	cbBiz *bizcb.Biz,
	cbStore store.ContextBudgetStore,
	aiSvcBiz aiservice_admin.IAIServiceAdminBiz,
	db *gorm.DB,
) *ContextBudgetController {
	return &ContextBudgetController{
		cbBiz:    cbBiz,
		cbStore:  cbStore,
		aiSvcBiz: aiSvcBiz,
		db:       db,
	}
}

// ----------------------------------------------------------------------------
// GET /v1/admin/context-budget/token-profiles
// ----------------------------------------------------------------------------

// listTokenProfilesQuery holds the optional query parameters for ListTokenProfiles.
// is_active accepts "active" (default), "inactive", or "all".
type listTokenProfilesQuery struct {
	Provider    string `form:"provider"`
	Model       string `form:"model"`
	ServiceType string `form:"service_type"`
	IsActive    string `form:"is_active"`
	Page        int    `form:"page,default=1"`
	PageSize    int    `form:"page_size,default=20"`
}

// ListTokenProfiles returns token estimation profiles with optional filtering by
// provider, model, service_type, and is_active, with pagination (spec §7.1).
// is_active defaults to "active"; pass "inactive" or "all" to include historical rows.
func (ctrl *ContextBudgetController) ListTokenProfiles(c *gin.Context) {
	log.C(c).Infow("Admin list token profiles called")

	var q listTokenProfilesQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize <= 0 || q.PageSize > 100 {
		q.PageSize = 20
	}

	ctx := c.Request.Context()
	db := ctrl.db.WithContext(ctx).Model(&model.TokenEstimationProfile{})

	if q.Provider != "" {
		db = db.Where("provider = ?", q.Provider)
	}
	if q.Model != "" {
		db = db.Where("model = ?", q.Model)
	}
	if q.ServiceType != "" {
		db = db.Where("service_type = ?", q.ServiceType)
	}
	switch q.IsActive {
	case "inactive":
		db = db.Where("is_active = ?", false)
	case "all":
		// no is_active filter
	default: // "active" or empty
		db = db.Where("is_active = ?", true)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		log.C(c).Errorw("Failed to count token profiles", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败"), nil)
		return
	}

	var profiles []model.TokenEstimationProfile
	if err := db.Order("version DESC, id DESC").
		Limit(q.PageSize).
		Offset((q.Page - 1) * q.PageSize).
		Find(&profiles).Error; err != nil {
		log.C(c).Errorw("Failed to list token profiles", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败"), nil)
		return
	}
	if profiles == nil {
		profiles = []model.TokenEstimationProfile{}
	}
	core.WriteResponse(c, nil, gin.H{"list": profiles, "total": total})
}

// ----------------------------------------------------------------------------
// POST /v1/admin/context-budget/token-profiles
// ----------------------------------------------------------------------------

// createTokenProfileReq is the request body for creating a token profile version.
type createTokenProfileReq struct {
	Provider              string         `json:"provider"               binding:"required"`
	Model                 string         `json:"model"                  binding:"required"`
	ModelFamily           string         `json:"model_family"`
	ServiceType           string         `json:"service_type"           binding:"required"`
	ProfileJSON           datatypes.JSON `json:"profile_json"           binding:"required"`
	SafetyMultiplier      float64        `json:"safety_multiplier"      binding:"required"`
	CalibrationMultiplier float64        `json:"calibration_multiplier"`
	IsFallback            bool           `json:"is_fallback"`
	ChangeReason          string         `json:"change_reason"`
}

// CreateTokenProfile saves a new version of a token estimation profile.
func (ctrl *ContextBudgetController) CreateTokenProfile(c *gin.Context) {
	log.C(c).Infow("Admin create token profile called")

	var req createTokenProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}
	if req.SafetyMultiplier < 1.0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("safety_multiplier 必须 >= 1.0"), nil)
		return
	}
	calMul := req.CalibrationMultiplier
	if calMul <= 0 {
		calMul = 1.0
	}

	actor := extractActor(c)
	saved, err := ctrl.cbStore.SaveTokenProfileVersion(c.Request.Context(), store.SaveTokenProfileInput{
		Provider:              req.Provider,
		Model:                 req.Model,
		ModelFamily:           req.ModelFamily,
		ServiceType:           req.ServiceType,
		ProfileJSON:           req.ProfileJSON,
		SafetyMultiplier:      req.SafetyMultiplier,
		CalibrationMultiplier: calMul,
		IsFallback:            req.IsFallback,
		ChangeReason:          req.ChangeReason,
		UpdatedBy:             actor,
	})
	if err != nil {
		log.C(c).Errorw("Failed to create token profile", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("创建失败"), nil)
		return
	}
	core.WriteResponse(c, nil, saved)
}

// ----------------------------------------------------------------------------
// PUT /v1/admin/context-budget/token-profiles/:id
// ----------------------------------------------------------------------------

// updateTokenProfileReq is the request body for updating a token profile.
// The caller provides the updated fields; the controller loads the existing
// row by ID to derive the lookup key and then saves a new version.
type updateTokenProfileReq struct {
	ProfileJSON           datatypes.JSON `json:"profile_json"           binding:"required"`
	SafetyMultiplier      float64        `json:"safety_multiplier"      binding:"required"`
	CalibrationMultiplier float64        `json:"calibration_multiplier"`
	ChangeReason          string         `json:"change_reason"`
}

// UpdateTokenProfile saves a new version of an existing token profile by ID.
// The lookup key (provider, model, service_type, is_fallback) is derived from
// the existing row, guaranteeing the update targets the correct profile family.
func (ctrl *ContextBudgetController) UpdateTokenProfile(c *gin.Context) {
	log.C(c).Infow("Admin update token profile called")

	id, err := parseUint64Param(c, "id")
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	var req updateTokenProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}
	if req.SafetyMultiplier < 1.0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("safety_multiplier 必须 >= 1.0"), nil)
		return
	}

	// Load existing row to derive lookup key.
	var existing model.TokenEstimationProfile
	if err := ctrl.db.WithContext(c.Request.Context()).First(&existing, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("token profile 不存在: id=%d", id), nil)
			return
		}
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询 token profile 失败"), nil)
		return
	}

	calMul := req.CalibrationMultiplier
	if calMul <= 0 {
		calMul = 1.0
	}

	actor := extractActor(c)
	saved, bizErr := ctrl.cbStore.SaveTokenProfileVersion(c.Request.Context(), store.SaveTokenProfileInput{
		Provider:              existing.Provider,
		Model:                 existing.Model,
		ModelFamily:           existing.ModelFamily,
		ServiceType:           existing.ServiceType,
		ProfileJSON:           req.ProfileJSON,
		SafetyMultiplier:      req.SafetyMultiplier,
		CalibrationMultiplier: calMul,
		IsFallback:            existing.IsFallback,
		ChangeReason:          req.ChangeReason,
		UpdatedBy:             actor,
	})
	if bizErr != nil {
		log.C(c).Errorw("Failed to update token profile", "id", id, "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("更新失败"), nil)
		return
	}
	core.WriteResponse(c, nil, saved)
}

// ----------------------------------------------------------------------------
// DELETE /v1/admin/context-budget/token-profiles/:id
// ----------------------------------------------------------------------------

// DeleteTokenProfile soft-deactivates a specific token profile version by setting
// is_active=false. This does NOT create a new version — it removes a version from
// the active set (useful for emergency rollback without losing the version history).
func (ctrl *ContextBudgetController) DeleteTokenProfile(c *gin.Context) {
	log.C(c).Infow("Admin delete token profile called")

	id, err := parseUint64Param(c, "id")
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	result := ctrl.db.WithContext(c.Request.Context()).
		Model(&model.TokenEstimationProfile{}).
		Where("id = ?", id).
		UpdateColumn("is_active", false)
	if result.Error != nil {
		log.C(c).Errorw("Failed to deactivate token profile", "id", id, "error", result.Error)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("操作失败"), nil)
		return
	}
	if result.RowsAffected == 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("token profile 不存在: id=%d", id), nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}

// ----------------------------------------------------------------------------
// GET /v1/admin/context-budget/token-profiles/history
// ----------------------------------------------------------------------------

// GetTokenProfileHistory returns all versions (active and inactive) for a given
// (provider, model, service_type) lookup key, ordered by version DESC.
//
// Query params: provider, model, service_type (all required).
func (ctrl *ContextBudgetController) GetTokenProfileHistory(c *gin.Context) {
	log.C(c).Infow("Admin get token profile history called")

	provider := c.Query("provider")
	modelKey := c.Query("model")
	serviceType := c.Query("service_type")
	if provider == "" || modelKey == "" || serviceType == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("provider、model、service_type 均为必填"), nil)
		return
	}

	var profiles []model.TokenEstimationProfile
	if err := ctrl.db.WithContext(c.Request.Context()).
		Where("provider = ? AND model = ? AND service_type = ?", provider, modelKey, serviceType).
		Order("version DESC, id DESC").
		Find(&profiles).Error; err != nil {
		log.C(c).Errorw("Failed to get token profile history", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败"), nil)
		return
	}
	if profiles == nil {
		profiles = []model.TokenEstimationProfile{}
	}
	core.WriteResponse(c, nil, gin.H{"list": profiles, "total": len(profiles)})
}

// ----------------------------------------------------------------------------
// GET /v1/admin/context-budget/policies
// ----------------------------------------------------------------------------

// ListPolicies returns budget policies ordered by operation ASC.
// Query param is_active: "active" (default) returns only active policies;
// "inactive" returns only deactivated policies; "all" returns the full
// version history for all operations (spec §7.2).
func (ctrl *ContextBudgetController) ListPolicies(c *gin.Context) {
	log.C(c).Infow("Admin list context budget policies called")

	isActive := c.Query("is_active")

	db := ctrl.db.WithContext(c.Request.Context()).Model(&model.ContextBudgetPolicy{})
	switch isActive {
	case "inactive":
		db = db.Where("is_active = ?", false)
	case "all":
		// no is_active filter — return full version history
	default: // "active" or empty
		db = db.Where("is_active = ?", true)
	}

	var policies []model.ContextBudgetPolicy
	if err := db.Order("operation ASC").Find(&policies).Error; err != nil {
		log.C(c).Errorw("Failed to list policies", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败"), nil)
		return
	}
	if policies == nil {
		policies = []model.ContextBudgetPolicy{}
	}
	core.WriteResponse(c, nil, gin.H{"list": policies, "total": len(policies)})
}

// ----------------------------------------------------------------------------
// PUT /v1/admin/context-budget/policies/:operation
// ----------------------------------------------------------------------------

// upsertPolicyReq is the request body for creating/updating a budget policy.
type upsertPolicyReq struct {
	ReservedOutputTokens int     `json:"reserved_output_tokens" binding:"required"`
	SafeRatio            float64 `json:"safe_ratio"             binding:"required"`
	FixedOverheadTokens  int     `json:"fixed_overhead_tokens"`
	SoftThresholdRatio   float64 `json:"soft_threshold_ratio"`
	HardThresholdRatio   float64 `json:"hard_threshold_ratio"`
	ChargeUser           bool    `json:"charge_user"`
	Description          string  `json:"description"`
	ChangeReason         string  `json:"change_reason"`
}

// UpsertPolicy saves a new version of the budget policy for the given operation.
// The operation is taken from the URL path parameter.
func (ctrl *ContextBudgetController) UpsertPolicy(c *gin.Context) {
	log.C(c).Infow("Admin upsert context budget policy called")

	operation := c.Param("operation")
	if operation == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("operation 参数必填"), nil)
		return
	}

	var req upsertPolicyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}
	if req.SafeRatio <= 0 || req.SafeRatio > 1 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("safe_ratio 必须在 (0, 1] 范围内"), nil)
		return
	}

	// Apply defaults for optional threshold ratios.
	softRatio := req.SoftThresholdRatio
	if softRatio <= 0 {
		softRatio = 0.70
	}
	hardRatio := req.HardThresholdRatio
	if hardRatio <= 0 {
		hardRatio = 0.85
	}

	actor := extractActor(c)
	saved, err := ctrl.cbStore.SavePolicyVersion(c.Request.Context(), store.SavePolicyInput{
		Operation:            operation,
		ReservedOutputTokens: req.ReservedOutputTokens,
		SafeRatio:            req.SafeRatio,
		FixedOverheadTokens:  req.FixedOverheadTokens,
		SoftThresholdRatio:   softRatio,
		HardThresholdRatio:   hardRatio,
		ChargeUser:           req.ChargeUser,
		Description:          req.Description,
		ChangeReason:         req.ChangeReason,
		UpdatedBy:            actor,
	})
	if err != nil {
		log.C(c).Errorw("Failed to upsert policy", "operation", operation, "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("保存失败"), nil)
		return
	}
	core.WriteResponse(c, nil, saved)
}

// ----------------------------------------------------------------------------
// POST /v1/admin/context-budget/preview
// ----------------------------------------------------------------------------

// previewReq is the request body for the preview endpoint.
type previewReq struct {
	ServiceID            uint64  `json:"service_id"             binding:"required"`
	Operation            string  `json:"operation"              binding:"required"`
	FixedOverheadTokens  int     `json:"fixed_overhead_tokens"  binding:"required"`
	ReservedOutputTokens int     `json:"reserved_output_tokens" binding:"required"`
	SafeRatio            float64 `json:"safe_ratio"             binding:"required"`
	SoftThresholdRatio   float64 `json:"soft_threshold_ratio"`
	HardThresholdRatio   float64 `json:"hard_threshold_ratio"`
}

// Preview computes the token budget thresholds for a given service and policy
// parameters. Does NOT write to the database.
//
// The endpoint loads the ai_service row identified by service_id to extract
// context_window and max_output_tokens from capability_json, then delegates
// budget math to biz/contextbudget.Biz.Preview.
func (ctrl *ContextBudgetController) Preview(c *gin.Context) {
	log.C(c).Infow("Admin context budget preview called")

	var req previewReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// Load ai_service via aiSvcBiz to go through registry cache/permission logic.
	svcDetail, err := ctrl.aiSvcBiz.GetService(c.Request.Context(), req.ServiceID)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("ai_service 不存在: id=%d", req.ServiceID), nil)
		return
	}

	// Parse context_window and max_output_tokens from capability_json.
	ctxWindow, maxOutput := resolveCapability(svcDetail.CapabilityJSON)

	// Apply threshold defaults.
	softRatio := req.SoftThresholdRatio
	if softRatio <= 0 {
		softRatio = 0.70
	}
	hardRatio := req.HardThresholdRatio
	if hardRatio <= 0 {
		hardRatio = 0.85
	}

	result, err := ctrl.cbBiz.Preview(c.Request.Context(), bizcb.PreviewInput{
		Capability: cbpkg.ModelCapability{
			ContextWindow:   ctxWindow,
			MaxOutputTokens: maxOutput,
		},

		Operation:            req.Operation,
		FixedOverheadTokens:  req.FixedOverheadTokens,
		ReservedOutputTokens: req.ReservedOutputTokens,
		SafeRatio:            req.SafeRatio,
		SoftThresholdRatio:   softRatio,
		HardThresholdRatio:   hardRatio,
	})
	if err != nil {
		log.C(c).Errorw("Failed to compute preview", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("预算计算失败"), nil)
		return
	}
	core.WriteResponse(c, nil, result)
}

// ----------------------------------------------------------------------------
// GET /v1/admin/context-budget/events
// ----------------------------------------------------------------------------

// listEventsQuery holds the optional query parameters for ListEvents.
type listEventsQuery struct {
	Operation string `form:"operation"`
	Status    string `form:"status"`
	Provider  string `form:"provider"`
	Model     string `form:"model"`
	Page      int    `form:"page,default=1"`
	PageSize  int    `form:"page_size,default=20"`
}

// ListEvents returns recent context budget events, metadata only.
// No prompt content, rendered messages, or fragment text is included.
//
// Query params: page (default 1), page_size (default 20, max 100).
// Optional filters: operation, status, provider, model.
func (ctrl *ContextBudgetController) ListEvents(c *gin.Context) {
	log.C(c).Infow("Admin list context budget events called")

	var lq listEventsQuery
	if err := c.ShouldBindQuery(&lq); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}
	if lq.Page < 1 {
		lq.Page = 1
	}
	if lq.PageSize <= 0 || lq.PageSize > 100 {
		lq.PageSize = 20
	}
	offset := (lq.Page - 1) * lq.PageSize

	q := ctrl.db.WithContext(c.Request.Context()).Model(&model.ContextBudgetEvent{})
	if lq.Operation != "" {
		q = q.Where("operation = ?", lq.Operation)
	}
	if lq.Status != "" {
		q = q.Where("status = ?", lq.Status)
	}
	if lq.Provider != "" {
		q = q.Where("provider = ?", lq.Provider)
	}
	if lq.Model != "" {
		q = q.Where("model = ?", lq.Model)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		log.C(c).Errorw("Failed to count context budget events", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败"), nil)
		return
	}

	// Select metadata columns only — explicitly exclude any column that could carry
	// prompt text. ContextBudgetEvent intentionally stores no prompt content
	// (spec §3.5 / §11.2), but we select only the metadata columns here to be
	// explicit about the contract.
	var events []contextBudgetEventMetadata
	if err := q.Select("id, user_id, operation, task_id, provider, model, " +
		"context_window, max_output_tokens, reserved_output_tokens, " +
		"fixed_overhead_tokens, safe_ratio, safe_input_budget, " +
		"estimated_before, estimated_after, " +
		"actual_prompt_tokens, actual_completion_tokens, " +
		"reserve_amount, reconcile_delta, " +
		"dropped_fragment_count, summarized_fragment_count, critical_fragment_count, " +
		"calibration_ratio, token_profile_id, budget_policy_id, " +
		"reservation_id, usage_record_id, " +
		"status, error_code, created_at").
		Order("id DESC").
		Offset(offset).
		Limit(lq.PageSize).
		Scan(&events).Error; err != nil {
		log.C(c).Errorw("Failed to list context budget events", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败"), nil)
		return
	}
	if events == nil {
		events = []contextBudgetEventMetadata{}
	}
	core.WriteResponse(c, nil, gin.H{"list": events, "total": total})
}

// contextBudgetEventMetadata is the response shape for a single context budget event.
// It contains metadata only — no prompt content, rendered messages, or fragment text.
// Fields mirror model.ContextBudgetEvent but omit any field that could carry user content.
type contextBudgetEventMetadata struct {
	ID                      uint64   `json:"id"`
	UserID                  *uint    `json:"user_id"`
	Operation               string   `json:"operation"`
	TaskID                  string   `json:"task_id"`
	Provider                string   `json:"provider"`
	Model                   string   `json:"model"`
	ContextWindow           int      `json:"context_window"`
	MaxOutputTokens         int      `json:"max_output_tokens"`
	ReservedOutputTokens    int      `json:"reserved_output_tokens"`
	FixedOverheadTokens     int      `json:"fixed_overhead_tokens"`
	SafeRatio               float64  `json:"safe_ratio"`
	SafeInputBudget         int      `json:"safe_input_budget"`
	EstimatedBefore         int      `json:"estimated_before"`
	EstimatedAfter          int      `json:"estimated_after"`
	ActualPromptTokens      *int     `json:"actual_prompt_tokens"`
	ActualCompletionTokens  *int     `json:"actual_completion_tokens"`
	ReserveAmount           *int64   `json:"reserve_amount"`
	ReconcileDelta          *int64   `json:"reconcile_delta"`
	DroppedFragmentCount    int      `json:"dropped_fragment_count"`
	SummarizedFragmentCount int      `json:"summarized_fragment_count"`
	CriticalFragmentCount   int      `json:"critical_fragment_count"`
	CalibrationRatio        *float64 `json:"calibration_ratio"`
	TokenProfileID          *uint64  `json:"token_profile_id"`
	BudgetPolicyID          *uint64  `json:"budget_policy_id"`
	ReservationID           *uint64  `json:"reservation_id"`
	UsageRecordID           *uint64  `json:"usage_record_id"`
	Status                  string   `json:"status"`
	ErrorCode               string   `json:"error_code"`
	CreatedAt               string   `json:"created_at"`
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// parseUint64Param parses a named URL path parameter as uint64.
func parseUint64Param(c *gin.Context, name string) (uint64, error) {
	raw := c.Param(name)
	val, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errno.ErrBind.SetMessage("无效的 %s 参数", name)
	}
	return val, nil
}

// extractActor extracts the acting admin's username from the Gin context.
// Falls back to "unknown" when the admin middleware is not present (e.g. tests).
func extractActor(c *gin.Context) string {
	if name, ok := c.Get("admin_name"); ok {
		if s, ok := name.(string); ok && s != "" {
			return s
		}
	}
	return "unknown"
}

// resolveCapability extracts context_window and max_output_tokens from a
// capability_json map. Returns (0, 0) when either field is absent.
func resolveCapability(cap model.JSONMap) (contextWindow, maxOutput int) {
	if cap == nil {
		return 0, 0
	}
	if v, ok := cap["context_window"]; ok {
		contextWindow = toInt(v)
	}
	if v, ok := cap["max_output_tokens"]; ok {
		maxOutput = toInt(v)
	}
	return
}

// toInt converts an interface{} value to int.
// Handles float64 (from JSON decode), int, int64, and json.Number.
func toInt(v interface{}) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	}
	return 0
}
