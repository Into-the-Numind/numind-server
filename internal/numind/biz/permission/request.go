package permission

import "numind-server/internal/numind/biz/agent"

// PermissionRequest — pipeline 输入
type PermissionRequest struct {
	AgentRunID        uint64
	UserID            uint
	ParentUserID      uint
	AgentDefinitionID uint64
	Tool              agent.FullTool
	InputJSON         string
	SandboxID         string
}
