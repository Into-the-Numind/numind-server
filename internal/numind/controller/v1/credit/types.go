package credit

import "numind-server/internal/numind/biz/credit"

// HTTP request/response types for credits system endpoints
// See spec: numind-server/docs/superpowers/specs/2026-04-18-credits-system-design.md §3.11 + §4.1

// EstimateReq POST /v1/credits/estimate
// 不含 prompt_chars：后端根据 operation+reference_id 自己渲染 prompt（见 spec §3.11 P1-2 修正）
type EstimateReq struct {
	Operation   string `json:"operation" binding:"required"`    // sop_run / sop_chat / salesrag_chat / ...
	ReferenceID string `json:"reference_id" binding:"required"` // sop_template_id / sop_run_id / session_id / ...
}

// EstimateResp POST /v1/credits/estimate
// SOP 场景：total_estimated_credits = 遍历所有 node 求和；first_node_estimate = 首 node；node_count = N
// 非 SOP 场景：total_estimated_credits = first_node_estimate，node_count = 1
type EstimateResp struct {
	TotalEstimatedCredits int64                   `json:"total_estimated_credits"`
	FirstNodeEstimate     *int64                  `json:"first_node_estimate,omitempty"`
	NodeCount             *int                    `json:"node_count,omitempty"`
	Sufficient            bool                    `json:"sufficient"`
	SkipDeduction         bool                    `json:"skip_deduction"` // legacy_tier=true
	Reason                string                  `json:"reason,omitempty"`
	Balance               credit.BalanceBreakdown `json:"balance"`
	CoefficientID         uint64                  `json:"coefficient_id"` // 前端 opaque，可忽略
}

// ListPackagesReq GET /v1/credits/packages
type ListPackagesReq struct {
	Page     int    `form:"page,default=1"`
	PageSize int    `form:"page_size,default=20"`        // max 100
	Status   string `form:"status,omitempty"`            // active/expired/revoked，空为全部
	Type     string `form:"type,omitempty"`              // trial/subscription/booster，空为全部
	Sort     string `form:"sort,default=expires_at:asc"` // expires_at:asc/desc, created_at:asc/desc
}

// ListPackagesResp GET /v1/credits/packages
type ListPackagesResp struct {
	List  []CreditPackageItem `json:"list"`
	Total int64               `json:"total"`
}

// CreditPackageItem is a normalized view of a user's credit_package for the Packages API
type CreditPackageItem struct {
	ID            uint64  `json:"id"`
	Type          string  `json:"type"`
	TotalCredits  int64   `json:"total_credits"`
	RemainCredits int64   `json:"remain_credits"`
	ActivatedAt   string  `json:"activated_at"`
	ExpiresAt     string  `json:"expires_at"`
	Status        string  `json:"status"`
	OrderID       *uint64 `json:"order_id,omitempty"`
	CreatedAt     string  `json:"created_at"`
}
