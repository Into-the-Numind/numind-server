// Package dto 提供 API 响应专用的数据传输对象（Data Transfer Object）。
//
// dto 包的存在是为了**严格隔离**数据库 model 与 HTTP 响应：
//   - model 包暴露完整字段（含 LLM 凭证、内部配置等敏感信息）
//   - dto 包只暴露给前端需要的、可公开的字段
//
// 直接序列化 model 到 HTTP 响应是 P0 安全漏洞。所有面向 C 端的 API 必须经过 dto 转换。
//
// SOP 相关 dto 文档参考：
//   - numind-server/docs/superpowers/specs/2026-04-11-sop-runtime-vue-rewrite-design.md §1
package dto

import (
	"time"

	"numind-server/internal/pkg/model"
)

// SopNodePublicDTO 是 SopNode 的 C 端公开视图。
//
// 隐藏的字段（5 个）：
//   - APIKey：LLM 服务密钥（P0 安全字段）
//   - BaseURL：LLM 服务地址（基础设施信息）
//   - ModelName：LLM 模型名称（基础设施信息）
//   - TimeoutSeconds：LLM 超时配置（基础设施信息）
//   - Prompt：节点提示词模板（B 端核心 IP，比 api_key 更敏感的商业资产）
//
// 排除的字段（2 个，dead fields，未在用户运行路径消费）：
//   - ParentID：树形结构父节点（当前未使用）
//   - IsRoot：是否根节点（当前未使用）
type SopNodePublicDTO struct {
	ID          uint      `json:"id"`
	TemplateID  uint      `json:"template_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"` // 可能为空，前端必须优雅退化
	Sort        int       `json:"sort"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SopNodeEditDTO 是 SopNode 的 B 端配置器视图。
//
// 与 SopNodePublicDTO 的关键差异：**保留 prompt 字段**。
// 因为 B 端创建者需要在 SopTemplateEdit.vue 编辑器中读取并修改 prompt
// 模板（这是其 SOP 的核心 IP，但创建者拥有它）。
//
// 隐藏的字段（4 个基础设施字段）：
//   - APIKey, BaseURL, ModelName, TimeoutSeconds
//
// 这些是平台后端的 LLM 服务配置，B 端不应也不能修改（CreateNode/UpdateNode
// 字段白名单已强制此约束），所以在 read 路径同样不暴露。
type SopNodeEditDTO struct {
	ID          uint      `json:"id"`
	TemplateID  uint      `json:"template_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Prompt      string    `json:"prompt"` // 与 PublicDTO 的关键差异：B 端创建者可见
	Sort        int       `json:"sort"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ToSopNodeEditDTO 将 model.SopNode 转换为 B 端编辑器 DTO。
//
// nil 输入返回零值 DTO。与 ToSopNodePublicDTO 行为一致。
func ToSopNodeEditDTO(node *model.SopNode) SopNodeEditDTO {
	if node == nil {
		return SopNodeEditDTO{}
	}
	return SopNodeEditDTO{
		ID:          node.ID,
		TemplateID:  node.TemplateID,
		Name:        node.Name,
		Description: node.Description,
		Prompt:      node.Prompt,
		Sort:        node.Sort,
		Status:      node.Status,
		CreatedAt:   node.CreatedAt,
		UpdatedAt:   node.UpdatedAt,
	}
}

// ToSopNodeEditDTOList 批量转换为 B 端编辑器 DTO。
func ToSopNodeEditDTOList(nodes []model.SopNode) []SopNodeEditDTO {
	dtos := make([]SopNodeEditDTO, 0, len(nodes))
	for i := range nodes {
		dtos = append(dtos, ToSopNodeEditDTO(&nodes[i]))
	}
	return dtos
}

// SopTemplatePublicDTO 是 SopTemplate 的 C 端公开视图。
//
// 隐藏的字段（2 个）：
//   - Prompt：模板级预处理提示词（仅后端使用）
//   - CreatorUserID：B 端创建者身份（不暴露 B 端用户 ID）
type SopTemplatePublicDTO struct {
	ID                  uint      `json:"id"`
	Name                string    `json:"name"`
	Description         string    `json:"description"`
	Status              string    `json:"status"`
	PublishStatus       string    `json:"publish_status"`
	TrailingChatEnabled bool      `json:"trailing_chat_enabled"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// ToSopNodePublicDTO 将 model.SopNode 转换为 C 端公开 DTO。
//
// 此函数是字段隐藏的唯一入口。任何返回 SopNode 列表的 controller
// 都必须通过此函数转换，禁止直接序列化 model.SopNode。
//
// nil 输入返回零值 DTO，防止上游 store 异常时 panic 扩散到 HTTP 响应层。
func ToSopNodePublicDTO(node *model.SopNode) SopNodePublicDTO {
	if node == nil {
		return SopNodePublicDTO{}
	}
	return SopNodePublicDTO{
		ID:          node.ID,
		TemplateID:  node.TemplateID,
		Name:        node.Name,
		Description: node.Description,
		Sort:        node.Sort,
		Status:      node.Status,
		CreatedAt:   node.CreatedAt,
		UpdatedAt:   node.UpdatedAt,
	}
}

// ToSopNodePublicDTOList 批量转换。
func ToSopNodePublicDTOList(nodes []model.SopNode) []SopNodePublicDTO {
	dtos := make([]SopNodePublicDTO, 0, len(nodes))
	for i := range nodes {
		dtos = append(dtos, ToSopNodePublicDTO(&nodes[i]))
	}
	return dtos
}

// ToSopTemplatePublicDTO 将 model.SopTemplate 转换为 C 端公开 DTO。
//
// nil 输入返回零值 DTO，与 ToSopNodePublicDTO 行为一致。
func ToSopTemplatePublicDTO(t *model.SopTemplate) SopTemplatePublicDTO {
	if t == nil {
		return SopTemplatePublicDTO{}
	}
	return SopTemplatePublicDTO{
		ID:                  t.ID,
		Name:                t.Name,
		Description:         t.Description,
		Status:              t.Status,
		PublishStatus:       t.PublishStatus,
		TrailingChatEnabled: t.TrailingChatEnabled,
		CreatedAt:           t.CreatedAt,
		UpdatedAt:           t.UpdatedAt,
	}
}
