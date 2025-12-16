package v1

import "numind-server/internal/pkg/model"

// CreateSopTemplateRequest 创建SOP模板请求
type CreateSopTemplateRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// UpdateSopTemplateRequest 更新SOP模板请求
type UpdateSopTemplateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
}

// CreateSopNodeRequest 创建SOP节点请求
type CreateSopNodeRequest struct {
	TemplateID     uint   `json:"template_id" binding:"required"`
	ParentID       *uint  `json:"parent_id"`
	Name           string `json:"name" binding:"required"`
	BaseURL        string `json:"base_url" binding:"required"`
	ModelName      string `json:"model_name" binding:"required"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Sort           int    `json:"sort"`
	Prompt         string `json:"prompt"`
}

// UpdateSopNodeRequest 更新SOP节点请求
type UpdateSopNodeRequest struct {
	Name           *string `json:"name"`
	Status         *string `json:"status"`
	BaseURL        *string `json:"base_url"`
	ModelName      *string `json:"model_name"`
	TimeoutSeconds *int    `json:"timeout_seconds"`
	Sort           *int    `json:"sort"`
	Prompt         *string `json:"prompt"`
}

// ExecuteSopTemplateRequest 执行SOP模板请求（用户端，从token获取user_id）
type ExecuteSopTemplateRequest struct {
	InitialInput string `json:"initial_input" binding:"required"`
}

// AdminExecuteSopTemplateRequest 执行SOP模板请求（管理端，需要指定user_id）
type AdminExecuteSopTemplateRequest struct {
	UserID       uint   `json:"user_id" binding:"required"`
	InitialInput string `json:"initial_input" binding:"required"`
}

// SopTemplateResponse SOP模板响应
type SopTemplateResponse struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// SopNodeResponse SOP节点响应
type SopNodeResponse struct {
	ID             uint   `json:"id"`
	TemplateID     uint   `json:"template_id"`
	ParentID       *uint  `json:"parent_id"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	BaseURL        string `json:"base_url"`
	ModelName      string `json:"model_name"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Sort           int    `json:"sort"`
	IsRoot         bool   `json:"is_root"`
	Prompt         string `json:"prompt"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// SopRunResponse SOP执行响应
type SopRunResponse struct {
	ID             uint               `json:"id"`
	TemplateID     uint               `json:"template_id"`
	UserID         uint               `json:"user_id"`
	Status         string             `json:"status"`
	ConversationID string             `json:"conversation_id"`
	FinalNoteID    *uint              `json:"final_note_id"`
	StartedAt      *string            `json:"started_at"`
	FinishedAt     *string            `json:"finished_at"`
	ErrorMessage   string             `json:"error_message"`
	CreatedAt      string             `json:"created_at"`
	Template       *model.SopTemplate `json:"template,omitempty"`
	FinalNote      *model.SopNote     `json:"final_note,omitempty"`
}

// SopNodeRunResponse SOP节点执行响应
type SopNodeRunResponse struct {
	ID           uint           `json:"id"`
	RunID        uint           `json:"run_id"`
	NodeID       uint           `json:"node_id"`
	Status       string         `json:"status"`
	Input        string         `json:"input"`
	Output       string         `json:"output"`
	LatencyMs    int64          `json:"latency_ms"`
	Sort         int            `json:"sort"`
	StartedAt    *string        `json:"started_at"`
	FinishedAt   *string        `json:"finished_at"`
	ErrorMessage string         `json:"error_message"`
	CreatedAt    string         `json:"created_at"`
	Node         *model.SopNode `json:"node,omitempty"`
}

// SopNoteResponse SOP笔记响应
type SopNoteResponse struct {
	ID         uint               `json:"id"`
	Content    string             `json:"content"`
	Title      string             `json:"title"`
	UserID     uint               `json:"user_id"`
	TemplateID uint               `json:"template_id"`
	RunID      uint               `json:"run_id"`
	CreatedAt  string             `json:"created_at"`
	Template   *model.SopTemplate `json:"template,omitempty"`
}

// ListSopTemplatesResponse SOP模板列表响应
type ListSopTemplatesResponse struct {
	Total     int64                 `json:"total"`
	Templates []SopTemplateResponse `json:"templates"`
}

// ListSopNodesResponse SOP节点列表响应
type ListSopNodesResponse struct {
	Total int64             `json:"total"`
	Nodes []SopNodeResponse `json:"nodes"`
}

// ListSopRunsResponse SOP执行列表响应
type ListSopRunsResponse struct {
	Total int64            `json:"total"`
	Runs  []SopRunResponse `json:"runs"`
}

// ListSopNotesResponse SOP笔记列表响应
type ListSopNotesResponse struct {
	Total int64             `json:"total"`
	Notes []SopNoteResponse `json:"notes"`
}

// SopRunDetailResponse SOP执行详情响应
type SopRunDetailResponse struct {
	Run      SopRunResponse       `json:"run"`
	NodeRuns []SopNodeRunResponse `json:"node_runs"`
}
