// Package admin_credit contains admin-facing HTTP handlers for the credits
// system. This file adds the estimation-coefficient CRUD endpoints defined by
// spec §3.11 + §4.1.2. They manage credit_estimation_coefficient (append-only
// versioned) — the biz.UpdateCoefficient path handles the SELECT FOR UPDATE
// + retry (spec §2.11.6), and this controller only wraps the HTTP contract.
package admin_credit

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// CoefficientController handles admin CRUD for credit_estimation_coefficient.
type CoefficientController struct {
	biz credit.IEstimationBiz
	ds  store.IStore
}

// NewCoefficientController constructs the controller. Both biz and ds are
// required — biz owns UpdateCoefficient's retry logic; ds is used for the
// read-side paginated list + history queries (those don't need concurrency
// guarantees, only append-only history traversal).
func NewCoefficientController(biz credit.IEstimationBiz, ds store.IStore) *CoefficientController {
	return &CoefficientController{biz: biz, ds: ds}
}

// ----------------------------------------------------------------------------
// Request / Response DTOs
// ----------------------------------------------------------------------------

// ListCoefficientsReq parses GET /v1/admin/estimation-coefficients query.
// is_active: "" or "1" → only active (default); "all" → every row.
// "0" → only historical (is_active=0) — admin-UI uses this for audit views.
type ListCoefficientsReq struct {
	Page      int    `form:"page,default=1"`
	PageSize  int    `form:"page_size,default=20"`
	Provider  string `form:"provider,omitempty"`
	Model     string `form:"model,omitempty"`
	Operation string `form:"operation,omitempty"`
	IsActive  string `form:"is_active,omitempty"`
}

// ListCoefficientsResp is the paginated list envelope.
type ListCoefficientsResp struct {
	List  []model.CreditEstimationCoefficient `json:"list"`
	Total int64                               `json:"total"`
}

// HistoryReq parses GET .../history — all three fields required to scope the
// append-only history chain to one (provider, model, operation) key.
type HistoryReq struct {
	Provider  string `form:"provider" binding:"required"`
	Model     string `form:"model" binding:"required"`
	Operation string `form:"operation" binding:"required"`
}

// HistoryResp is the history list — no pagination (version chain is bounded).
type HistoryResp struct {
	List []model.CreditEstimationCoefficient `json:"list"`
}

// UpsertCoefficientReq is the shared body for POST (create new key) and PUT
// (bump version for existing key). Version/IsActive are derived server-side.
// ChangeReason is required on PUT (audit trail), optional on POST (first-insert
// has no prior state to diff against). UpdatedBy is derived from current admin.
type UpsertCoefficientReq struct {
	Provider              string  `json:"provider" binding:"required"`
	Model                 string  `json:"model" binding:"required"`
	Operation             string  `json:"operation" binding:"required"`
	CharToTokenRatio      float64 `json:"char_to_token_ratio" binding:"required,gt=0"`
	CompletionPromptRatio float64 `json:"completion_prompt_ratio" binding:"required,gt=0"`
	SafetyBufferPct       float64 `json:"safety_buffer_pct" binding:"gte=0"`
	ChangeReason          string  `json:"change_reason,omitempty"`
}

// ----------------------------------------------------------------------------
// Handlers
// ----------------------------------------------------------------------------

// ListCoefficients GET /v1/admin/estimation-coefficients
// 默认 is_active=1（仅列当前启用）；is_active=all 列所有 version；is_active=0 列历史。
// 支持按 provider/model/operation 进一步过滤。
func (c *CoefficientController) ListCoefficients(ctx *gin.Context) {
	var req ListCoefficientsReq
	if err := ctx.ShouldBindQuery(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	if req.Page < 1 {
		req.Page = 1
	}
	if req.PageSize < 1 {
		req.PageSize = 20
	}
	if req.PageSize > 100 {
		req.PageSize = 100
	}
	offset := (req.Page - 1) * req.PageSize

	db := c.ds.DB().WithContext(ctx).Model(&model.CreditEstimationCoefficient{})

	// is_active filter: "" or "1" → active only; "all" → skip filter; "0" → inactive only
	switch req.IsActive {
	case "", "1":
		db = db.Where("is_active = ?", true)
	case "0":
		db = db.Where("is_active = ?", false)
	case "all":
		// no filter
	default:
		core.WriteResponse(ctx, errno.ErrInvalidParameter.SetMessage("invalid is_active: %s", req.IsActive), nil)
		return
	}

	if req.Provider != "" {
		db = db.Where("provider = ?", req.Provider)
	}
	if req.Model != "" {
		db = db.Where("model = ?", req.Model)
	}
	if req.Operation != "" {
		db = db.Where("operation = ?", req.Operation)
	}

	// Independent count query (avoid GORM session pollution).
	// Must mirror the `db` query's is_active switch (including "all" → no filter)
	// to keep pagination total accurate (review P2-1 fix).
	var total int64
	countDB := c.ds.DB().WithContext(ctx).Model(&model.CreditEstimationCoefficient{})
	switch req.IsActive {
	case "", "1":
		countDB = countDB.Where("is_active = ?", true)
	case "0":
		countDB = countDB.Where("is_active = ?", false)
	case "all":
		// no filter (matches list query's semantics)
	}
	if req.Provider != "" {
		countDB = countDB.Where("provider = ?", req.Provider)
	}
	if req.Model != "" {
		countDB = countDB.Where("model = ?", req.Model)
	}
	if req.Operation != "" {
		countDB = countDB.Where("operation = ?", req.Operation)
	}
	if err := countDB.Count(&total).Error; err != nil {
		log.C(ctx).Errorw("count coefficients failed", "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	var rows []model.CreditEstimationCoefficient
	if err := db.Order("updated_at DESC").Offset(offset).Limit(req.PageSize).Find(&rows).Error; err != nil {
		log.C(ctx).Errorw("list coefficients failed", "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(ctx, nil, ListCoefficientsResp{List: rows, Total: total})
}

// ListCoefficientHistory GET /v1/admin/estimation-coefficients/history
// 按 (provider, model, operation) 查所有 version（含 is_active=0），按 version DESC。
// spec §4.1.2 — admin UI 用此 endpoint 供"历史版本 drawer" 展示。
func (c *CoefficientController) ListCoefficientHistory(ctx *gin.Context) {
	var req HistoryReq
	if err := ctx.ShouldBindQuery(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	var rows []model.CreditEstimationCoefficient
	if err := c.ds.DB().WithContext(ctx).
		Where("provider = ? AND model = ? AND operation = ?", req.Provider, req.Model, req.Operation).
		Order("version DESC").
		Find(&rows).Error; err != nil {
		log.C(ctx).Errorw("list coefficient history failed",
			"provider", req.Provider, "model", req.Model, "operation", req.Operation, "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(ctx, nil, HistoryResp{List: rows})
}

// CreateCoefficient POST /v1/admin/estimation-coefficients
// 新增 (provider, model, operation) 的第一行或者后续 version bump。
// 内部调 UpdateCoefficient — 该方法是 append-only 语义（无差异）。
func (c *CoefficientController) CreateCoefficient(ctx *gin.Context) {
	var req UpsertCoefficientReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	row := c.reqToModel(ctx, &req)
	id, err := c.biz.UpdateCoefficient(ctx, row)
	if err != nil {
		c.writeCoefficientError(ctx, err)
		return
	}

	// Return the freshly inserted row so admin UI can show it without refetch.
	var fresh model.CreditEstimationCoefficient
	if err := c.ds.DB().WithContext(ctx).First(&fresh, id).Error; err != nil {
		log.C(ctx).Warnw("coefficient created but refetch failed", "id", id, "err", err)
		core.WriteResponse(ctx, nil, gin.H{"id": id})
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"coefficient": fresh})
}

// UpdateCoefficient PUT /v1/admin/estimation-coefficients/:id
// 编辑意味着在 (provider, model, operation) 上追加一个新 version — 老 version is_active=0。
// :id 参数用于 UI 显式选中历史行 (provider/model/operation 仍以 body 为准)。
// change_reason 推荐填写（编辑场景有审计价值），此处不强制 required 以保持与 create 语义一致。
func (c *CoefficientController) UpdateCoefficient(ctx *gin.Context) {
	idStr := ctx.Param("id")
	if _, err := strconv.ParseUint(idStr, 10, 64); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("invalid coefficient id: %s", idStr), nil)
		return
	}

	var req UpsertCoefficientReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	row := c.reqToModel(ctx, &req)
	newID, err := c.biz.UpdateCoefficient(ctx, row)
	if err != nil {
		c.writeCoefficientError(ctx, err)
		return
	}

	var fresh model.CreditEstimationCoefficient
	if err := c.ds.DB().WithContext(ctx).First(&fresh, newID).Error; err != nil {
		log.C(ctx).Warnw("coefficient updated but refetch failed", "id", newID, "err", err)
		core.WriteResponse(ctx, nil, gin.H{"id": newID})
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"coefficient": fresh})
}

// DeleteCoefficient DELETE /v1/admin/estimation-coefficients/:id
// 软删（spec §4.1.2）：仅置 is_active=0；因 append-only 约束不做物理删。
// 若历史行已经 is_active=0，返回 already-inactive 作为幂等响应。
func (c *CoefficientController) DeleteCoefficient(ctx *gin.Context) {
	idStr := ctx.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("invalid coefficient id: %s", idStr), nil)
		return
	}

	// Load first so we can return a clean 404 vs a silent zero-rows-affected.
	var row model.CreditEstimationCoefficient
	if err := c.ds.DB().WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			core.WriteResponse(ctx, errno.ErrInvalidParameter.SetMessage("coefficient %d not found", id), nil)
			return
		}
		log.C(ctx).Errorw("load coefficient failed", "id", id, "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}
	if !row.IsActive {
		core.WriteResponse(ctx, nil, gin.H{"id": id, "already_inactive": true})
		return
	}

	// Soft-delete: UPDATE is_active = 0. We DON'T insert a new active row here —
	// admin must explicitly re-create one with fresh coefficients if they want
	// the (provider, model, operation) key to keep firing CheckAndEstimate.
	if err := c.ds.DB().WithContext(ctx).
		Model(&model.CreditEstimationCoefficient{}).
		Where("id = ?", id).
		Update("is_active", false).Error; err != nil {
		log.C(ctx).Errorw("soft-delete coefficient failed", "id", id, "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(ctx, nil, gin.H{"id": id, "deleted": true})
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// reqToModel maps the DTO → model.CreditEstimationCoefficient, stamping the
// acting admin into updated_by. IsActive / Version are derived by biz.
func (c *CoefficientController) reqToModel(ctx *gin.Context, req *UpsertCoefficientReq) *model.CreditEstimationCoefficient {
	adminName := ""
	if u := middleware.GetCurrentUser(ctx); u != nil {
		adminName = u.Username
	}
	return &model.CreditEstimationCoefficient{
		Provider:              req.Provider,
		Model:                 req.Model,
		Operation:             req.Operation,
		CharToTokenRatio:      req.CharToTokenRatio,
		CompletionPromptRatio: req.CompletionPromptRatio,
		SafetyBufferPct:       req.SafetyBufferPct,
		ChangeReason:          req.ChangeReason,
		UpdatedBy:             adminName,
	}
}

// writeCoefficientError classifies biz errors into HTTP responses.
//   - ErrCoefficientConcurrent → HTTP 503 (spec §2.11.6)
//   - other → 500 Internal Server Error
func (c *CoefficientController) writeCoefficientError(ctx *gin.Context, err error) {
	log.C(ctx).Errorw("coefficient upsert failed", "err", err)
	if errors.Is(err, credit.ErrCoefficientConcurrent) {
		core.WriteResponse(ctx, errno.ErrCoefficientConcurrent, nil)
		return
	}
	core.WriteResponse(ctx, errno.ErrInternalServer.SetMessage("%s", err.Error()), nil)
}
