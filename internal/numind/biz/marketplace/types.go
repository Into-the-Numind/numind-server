// Package marketplace implements the agent-mode-v2-skill-marketplace cross-tenant
// Skill publish/browse/subscribe flow. Spec: docs/superpowers/specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md
package marketplace

// PublishRequest is the JSON body for POST /v1/marketplace/publish.
// confirmed_sanitized_body is what the publisher saw + clicked-through in the
// frontend diff review gate; the biz layer re-runs sanitize and verifies a
// normalized-hash match (within 5% char delta tolerance) before persisting.
type PublishRequest struct {
	SkillID                  uint     `json:"skill_id" binding:"required"`
	CategoryTags             []string `json:"category_tags" binding:"required,min=1,max=5"`
	ConfirmedSanitizedBodyMD string   `json:"confirmed_sanitized_body" binding:"required"`
}

// BrowseQuery is the query-string binding for GET /v1/marketplace/list.
// The biz layer maps these to store.ListOptions; sort/pagination defaults applied
// in the controller (or here if controller does only binding+validation).
type BrowseQuery struct {
	Q        string `form:"q"`
	Category string `form:"category"`
	Sort     string `form:"sort"`      // "recommended" (default) | "recent" | "popular"
	Page     int    `form:"page"`      // 1-based
	PageSize int    `form:"page_size"` // default 20, max 100
}
