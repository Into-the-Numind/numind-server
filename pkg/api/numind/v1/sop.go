package v1

import "numind-server/internal/pkg/model"

// CreateSopTemplateRequest 创建SOP模板请求
type CreateSopTemplateRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"` // 预处理提示词
}

// UpdateSopTemplateRequest 更新SOP模板请求
type UpdateSopTemplateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
	Prompt      *string `json:"prompt"` // 预处理提示词
}

// CreateSopNodeRequest 创建SOP节点请求
type CreateSopNodeRequest struct {
	TemplateID     uint   `json:"template_id" binding:"required"`
	ParentID       *uint  `json:"parent_id"`
	Name           string `json:"name" binding:"required"`
	BaseURL        string `json:"base_url" binding:"required"`
	ModelName      string `json:"model_name" binding:"required"`
	APIKey         string `json:"api_key"` // API密钥
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
	APIKey         *string `json:"api_key"` // API密钥
	TimeoutSeconds *int    `json:"timeout_seconds"`
	Sort           *int    `json:"sort"`
	Prompt         *string `json:"prompt"`
}

// ExecuteSopTemplateRequest 执行SOP模板请求（用户端，从token获取user_id）
type ExecuteSopTemplateRequest struct {
	Text string `json:"text" binding:"required"`
}

// AdminExecuteSopTemplateRequest 执行SOP模板请求（管理端，需要指定user_id）
type AdminExecuteSopTemplateRequest struct {
	UserID uint   `json:"user_id" binding:"required"`
	Text   string `json:"text" binding:"required"`
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

// CreateSopRunRequest 创建SOP执行请求
type CreateSopRunRequest struct {
	TemplateID uint   `json:"template_id" binding:"required"`
	Text       string `json:"text"` // 可选，如果不提供则使用默认输入
}

// ExecuteSopNodeRequest 执行SOP节点请求
type ExecuteSopNodeRequest struct {
	Text string `json:"text"` // 可选，如果不提供则使用上一个节点的输出
}

// NextNodeResponse 下一个节点响应
type NextNodeResponse struct {
	NodeID   uint   `json:"node_id"`
	NodeName string `json:"node_name"`
	Sort     int    `json:"sort"`
	IsFirst  bool   `json:"is_first"`
	HasNext  bool   `json:"has_next"`
}

// RunStatusResponse Run执行状态响应
type RunStatusResponse struct {
	Status          string              `json:"status"`
	CurrentNodeSort int                 `json:"current_node_sort"`
	CompletedNodes  []CompletedNodeInfo `json:"completed_nodes"`
	NextNode        *NextNodeInfo       `json:"next_node,omitempty"`
	TotalNodes      int                 `json:"total_nodes"`
	CompletedCount  int                 `json:"completed_count"`
}

// CompletedNodeInfo 已完成节点信息
type CompletedNodeInfo struct {
	NodeID   uint   `json:"node_id"`
	NodeName string `json:"node_name"`
	Sort     int    `json:"sort"`
	Input    string `json:"input"`     // 节点输入
	Output   string `json:"output"`    // 完整输出
	Thinking string `json:"thinking,omitempty"`
}

// NextNodeInfo 下一个节点信息
type NextNodeInfo struct {
	NodeID   uint   `json:"node_id"`
	NodeName string `json:"node_name"`
	Sort     int    `json:"sort"`
	IsFirst  bool   `json:"is_first"`
	HasNext  bool   `json:"has_next"`
}

// EditTextMessage 文本编辑对话消息
type EditTextMessage struct {
	Role    string `json:"role"`    // user/assistant
	Content string `json:"content"` // 消息内容
}

// EditTextRequest 文本编辑请求（支持多轮对话）
type EditTextRequest struct {
	OriginalText        string            `json:"original_text,omitempty"`         // 原始文本内容（第一次对话必需，后续可选）
	UserMessage         string            `json:"user_message" binding:"required"` // 用户编辑指令（必需）
	ConversationHistory []EditTextMessage `json:"conversation_history,omitempty"`  // 对话历史（可选，前端维护）
}

// ExecutedTemplateInfo 用户已执行的模板信息
type ExecutedTemplateInfo struct {
	TemplateID   uint   `json:"template_id"`   // 模板ID
	TemplateName string `json:"template_name"` // 模板名称
	RunCount     int64  `json:"run_count"`     // 执行次数
	ExecutedAt   string `json:"executed_at"`   // 执行时间
	RunID        uint   `json:"run_id"`        // Run ID
	RunStatus    string `json:"run_status"`    // 执行状态
}

// ListExecutedTemplatesResponse 用户已执行的模板列表响应
type ListExecutedTemplatesResponse struct {
	Total     int64                  `json:"total"`     // 模板总数
	Templates []ExecutedTemplateInfo `json:"templates"` // 模板列表
}

// TemplateRunHistoryResponse 模板运行历史记录响应
type TemplateRunHistoryResponse struct {
	ID             uint                      `json:"id"`              // Run ID
	TemplateID     uint                      `json:"template_id"`     // 模板ID
	Status         string                    `json:"status"`          // Run状态
	CreatedAt      string                    `json:"created_at"`      // 创建时间
	UpdatedAt      string                    `json:"updated_at"`     // 更新时间
	CompletedCount int                       `json:"completed_count"` // 已完成节点数
	TotalNodes     int                       `json:"total_nodes"`    // 总节点数
	NodeRuns       []TemplateNodeRunInfo     `json:"node_runs"`      // 节点执行记录列表
	ChatMessages   []TemplateChatMessageInfo `json:"chat_messages"`  // 对话记录列表
}

// TemplateNodeRunInfo 模板节点执行信息
type TemplateNodeRunInfo struct {
	ID            uint                `json:"id"`             // NodeRun ID
	NodeID        uint                `json:"node_id"`        // 节点ID
	NodeName      string              `json:"node_name"`     // 节点名称（从Node关联获取）
	Sort          int                 `json:"sort"`           // 节点排序
	Status        string              `json:"status"`         // 节点状态
	FinishedAt    *string             `json:"finished_at"`   // 完成时间
	Input         string              `json:"input"`         // 用户输入（对应input字段）
	Output        string              `json:"output"`        // AI输出内容（对应output字段）
	Thinking      string              `json:"thinking"`      // AI思考过程（可选，对应thinking字段）
	OutputPreview string              `json:"output_preview,omitempty"` // 输出预览（用于列表展示，截取前200字符）
	Files         []TemplateFileInfo  `json:"files,omitempty"`          // 文件列表（如果有）
}

// TemplateFileInfo 模板文件信息
type TemplateFileInfo struct {
	ID       uint   `json:"id"`        // 文件ID
	FileName string `json:"file_name"` // 文件名
	FileURL  string `json:"file_url"`  // 文件URL
	FileSize int64  `json:"file_size"` // 文件大小
	FileType string `json:"file_type"` // 文件类型
}

// TemplateChatMessageInfo 模板对话消息信息
type TemplateChatMessageInfo struct {
	ID        uint   `json:"id"`         // 消息ID
	Role      string `json:"role"`       // 角色（user/assistant）
	Content   string `json:"content"`    // 消息内容
	CreatedAt string `json:"created_at"` // 创建时间
}

// ListTemplateRunsResponse 模板运行历史列表响应
type ListTemplateRunsResponse struct {
	TemplateID uint                        `json:"template_id"` // 模板ID
	Total      int64                       `json:"total"`       // 总记录数
	Runs       []TemplateRunHistoryResponse `json:"runs"`       // 运行记录列表
}
