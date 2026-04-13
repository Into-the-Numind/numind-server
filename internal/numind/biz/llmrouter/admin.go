package llmrouter

import (
	"context"
	"fmt"

	"numind-server/internal/pkg/model"
)

// ListProviders 分页查询所有 LLM 供应商
func (r *Router) ListProviders(ctx context.Context, offset, limit int) ([]model.LLMProvider, int64, error) {
	providers, total, err := r.ds.LLMProvider().List(ctx, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("llmrouter.ListProviders: %w", err)
	}
	return providers, total, nil
}

// CreateProvider 创建 LLM 供应商
func (r *Router) CreateProvider(ctx context.Context, p *model.LLMProvider) error {
	if err := r.ds.LLMProvider().Create(ctx, p); err != nil {
		return fmt.Errorf("llmrouter.CreateProvider: %w", err)
	}
	return nil
}

// UpdateProvider 更新 LLM 供应商（先查后改再保存）
func (r *Router) UpdateProvider(ctx context.Context, id uint64, displayName, baseURL, apiKey string, isActive *bool) error {
	p, err := r.ds.LLMProvider().Get(ctx, id)
	if err != nil {
		return fmt.Errorf("llmrouter.UpdateProvider: get: %w", err)
	}
	if displayName != "" {
		p.DisplayName = displayName
	}
	if baseURL != "" {
		p.BaseURL = baseURL
	}
	if apiKey != "" {
		p.APIKey = apiKey
	}
	if isActive != nil {
		p.IsActive = *isActive
	}
	if err := r.ds.LLMProvider().Update(ctx, p); err != nil {
		return fmt.Errorf("llmrouter.UpdateProvider: save: %w", err)
	}
	return nil
}

// DeleteProvider 删除 LLM 供应商
func (r *Router) DeleteProvider(ctx context.Context, id uint64) error {
	if err := r.ds.LLMProvider().Delete(ctx, id); err != nil {
		return fmt.Errorf("llmrouter.DeleteProvider: %w", err)
	}
	return nil
}

// ListModels 分页查询所有 LLM 模型（按 sort_order 排序）
func (r *Router) ListModels(ctx context.Context, offset, limit int) ([]model.LLMModel, int64, error) {
	models, total, err := r.ds.LLMModel().List(ctx, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("llmrouter.ListModels: %w", err)
	}
	return models, total, nil
}

// normalizeThinkingFlags 统一处理 thinking_only 与其他标志的互斥关系：
// thinking_only 模型不能是 thinking 变体，且天然支持思考。
func normalizeThinkingFlags(m *model.LLMModel) {
	if m.ThinkingOnly {
		m.IsThinking = false
		m.BaseModelID = nil
		m.SupportsThinking = true
	}
}

// CreateModel 创建 LLM 模型
func (r *Router) CreateModel(ctx context.Context, m *model.LLMModel) error {
	normalizeThinkingFlags(m)
	if err := r.ds.LLMModel().Create(ctx, m); err != nil {
		return fmt.Errorf("llmrouter.CreateModel: %w", err)
	}
	return nil
}

// UpdateModel 更新 LLM 模型（先查后改再保存）
func (r *Router) UpdateModel(ctx context.Context, id uint64, updates map[string]interface{}) error {
	m, err := r.ds.LLMModel().Get(ctx, id)
	if err != nil {
		return fmt.Errorf("llmrouter.UpdateModel: get: %w", err)
	}
	if v, ok := updates["display_name"].(string); ok && v != "" {
		m.DisplayName = v
	}
	if v, ok := updates["icon"].(string); ok {
		m.Icon = v
	}
	if v, ok := updates["sort_order"].(int); ok {
		m.SortOrder = v
	}
	if v, ok := updates["is_active"].(*bool); ok && v != nil {
		m.IsActive = *v
	}
	if v, ok := updates["supports_thinking"].(*bool); ok && v != nil {
		m.SupportsThinking = *v
	}
	if v, ok := updates["base_model_id"].(*uint64); ok {
		m.BaseModelID = v
	}
	if v, ok := updates["is_thinking"].(*bool); ok && v != nil {
		m.IsThinking = *v
	}
	if v, ok := updates["thinking_only"].(*bool); ok && v != nil {
		m.ThinkingOnly = *v
	}
	normalizeThinkingFlags(m)
	if err := r.ds.LLMModel().Update(ctx, m); err != nil {
		return fmt.Errorf("llmrouter.UpdateModel: save: %w", err)
	}
	return nil
}

// DeleteModel 删除 LLM 模型
func (r *Router) DeleteModel(ctx context.Context, id uint64) error {
	if err := r.ds.LLMModel().Delete(ctx, id); err != nil {
		return fmt.Errorf("llmrouter.DeleteModel: %w", err)
	}
	return nil
}

// ListRoutes 查询指定模型的所有路由映射（含 Provider 预加载）
func (r *Router) ListRoutes(ctx context.Context, modelID uint64) ([]model.LLMModelProvider, error) {
	routes, err := r.ds.LLMModelProvider().ListByModel(ctx, modelID)
	if err != nil {
		return nil, fmt.Errorf("llmrouter.ListRoutes: %w", err)
	}
	return routes, nil
}

// CreateRoute 创建路由映射
func (r *Router) CreateRoute(ctx context.Context, mp *model.LLMModelProvider) error {
	if err := r.ds.LLMModelProvider().Create(ctx, mp); err != nil {
		return fmt.Errorf("llmrouter.CreateRoute: %w", err)
	}
	return nil
}

// UpdateRoute 更新路由映射（先查后改再保存）
func (r *Router) UpdateRoute(ctx context.Context, id uint64, providerModelID string, priority int, inputPrice, outputPrice float64, isActive *bool) error {
	mp, err := r.ds.LLMModelProvider().Get(ctx, id)
	if err != nil {
		return fmt.Errorf("llmrouter.UpdateRoute: get: %w", err)
	}
	if providerModelID != "" {
		mp.ProviderModelID = providerModelID
	}
	mp.Priority = priority
	mp.InputPricePerMTok = inputPrice
	mp.OutputPricePerMTok = outputPrice
	if isActive != nil {
		mp.IsActive = *isActive
	}
	if err := r.ds.LLMModelProvider().Update(ctx, mp); err != nil {
		return fmt.Errorf("llmrouter.UpdateRoute: save: %w", err)
	}
	return nil
}

// DeleteRoute 删除路由映射
func (r *Router) DeleteRoute(ctx context.Context, id uint64) error {
	if err := r.ds.LLMModelProvider().Delete(ctx, id); err != nil {
		return fmt.Errorf("llmrouter.DeleteRoute: %w", err)
	}
	return nil
}
