package llmrouter

import (
	"numind-server/internal/numind/store"
)

// Router LLM 路由服务，负责用户侧模型选择与偏好管理。
//
// 历史：早期还承担按 modelKey 解析路由 + 多 provider failover 的职责
// （Resolve + StreamChat + cache）。ai-service-manager 上线后，这些能力
// 全部由 internal/pkg/aiservice Gateway 接管；Router 仅保留用户侧偏好相关方法
// （GetModels / GetPreferences / SavePreference / ResolveUserModel）。
type Router struct {
	ds store.IStore
}

// New 创建新的 LLMRouter 实例
func New(ds store.IStore) *Router {
	return &Router{ds: ds}
}
