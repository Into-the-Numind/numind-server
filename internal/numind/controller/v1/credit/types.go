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
	SkipDeduction         bool                    `json:"skip_deduction"` // currently always false post legacy-deprecation; reserved for future use cases
	Reason                string                  `json:"reason,omitempty"`
	Balance               credit.BalanceBreakdown `json:"balance"`
	CoefficientID         uint64                  `json:"coefficient_id"` // 前端 opaque，可忽略
}

// T9: ListPackagesReq, ListPackagesResp, CreditPackageItem deleted — credit_package dead types removed.
