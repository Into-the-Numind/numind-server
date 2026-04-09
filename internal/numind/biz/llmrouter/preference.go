package llmrouter

import (
	"context"
	"fmt"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// validFeatures 是允许的功能键集合
var validFeatures = map[string]struct{}{
	"chatbot": {},
	"sop":     {},
}

// PreferenceResult 用户模型偏好查询结果
type PreferenceResult struct {
	ModelKey string `json:"model_key"`
	Thinking bool   `json:"thinking"`
}

// GetModels 返回所有激活的基础模型列表（is_thinking=false, is_active=true），按 sort_order 排序。
// 同时返回默认模型 key（列表中第一个模型的 model_key）。
func (r *Router) GetModels(ctx context.Context) ([]model.LLMModel, string, error) {
	models, err := r.ds.LLMModel().ListActiveBase(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("llmrouter.GetModels: %w", err)
	}

	defaultKey := ""
	if len(models) > 0 {
		defaultKey = models[0].ModelKey
	}

	return models, defaultKey, nil
}

// GetPreferences 返回指定用户的所有功能偏好设置。
// 对于用户未设置的功能，使用系统默认模型填充。
func (r *Router) GetPreferences(ctx context.Context, userID uint) (map[string]PreferenceResult, error) {
	// 获取系统默认模型
	defaultModel, err := r.ds.LLMModel().GetDefaultModel(ctx)
	if err != nil {
		return nil, fmt.Errorf("llmrouter.GetPreferences: get default model: %w", err)
	}

	// 获取用户已保存的偏好
	prefs, err := r.ds.UserModelPreference().GetAll(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("llmrouter.GetPreferences: get all preferences: %w", err)
	}

	// 构建偏好 map（用 feature 作为 key）
	saved := make(map[string]model.UserModelPreference, len(prefs))
	for _, p := range prefs {
		saved[p.Feature] = p
	}

	// 填充结果：对每个有效功能，已有偏好则使用，否则使用默认模型
	result := make(map[string]PreferenceResult, len(validFeatures))
	for feature := range validFeatures {
		if p, ok := saved[feature]; ok {
			result[feature] = PreferenceResult{
				ModelKey: p.ModelKey,
				Thinking: p.Thinking,
			}
		} else {
			result[feature] = PreferenceResult{
				ModelKey: defaultModel.ModelKey,
				Thinking: false,
			}
		}
	}

	return result, nil
}

// SavePreference 验证并保存用户的模型偏好。
// 验证规则：feature 必须是 "chatbot" 或 "sop"；model_key 对应的模型必须存在、激活且为基础模型；
// 若 thinking=true，则模型必须支持 thinking。
func (r *Router) SavePreference(ctx context.Context, userID uint, feature, modelKey string, thinking bool) error {
	// 验证 feature
	if _, ok := validFeatures[feature]; !ok {
		return fmt.Errorf("invalid feature %q: must be one of chatbot, sop", feature)
	}

	// 验证模型存在、激活且为基础模型
	m, err := r.ds.LLMModel().GetByKey(ctx, modelKey)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("model %q not found", modelKey)
		}
		return fmt.Errorf("llmrouter.SavePreference: get model by key: %w", err)
	}

	if !m.IsActive {
		return fmt.Errorf("model %q is not active", modelKey)
	}

	if m.IsThinking {
		return fmt.Errorf("model %q is a thinking variant, must use a base model", modelKey)
	}

	// 若请求开启 thinking，验证模型是否支持
	if thinking && !m.SupportsThinking {
		return fmt.Errorf("model %q does not support thinking mode", modelKey)
	}

	// Upsert 偏好记录
	pref := &model.UserModelPreference{
		UserID:   userID,
		Feature:  feature,
		ModelKey: modelKey,
		Thinking: thinking,
	}
	if err := r.ds.UserModelPreference().Upsert(ctx, pref); err != nil {
		return fmt.Errorf("llmrouter.SavePreference: upsert preference: %w", err)
	}

	return nil
}

// ResolveUserModel 实现三级 fallback 模型解析：
//  1. query param（若 queryModelKey != ""）
//  2. 用户偏好（feature 对应的偏好）
//  3. 系统默认模型
func (r *Router) ResolveUserModel(ctx context.Context, userID uint, feature, queryModelKey string, queryThinking *bool) (string, bool, error) {
	// 级别 1：query param 优先
	if queryModelKey != "" {
		thinking := false
		if queryThinking != nil {
			thinking = *queryThinking
		}
		return queryModelKey, thinking, nil
	}

	// 级别 2：用户偏好
	pref, err := r.ds.UserModelPreference().Get(ctx, userID, feature)
	if err == nil {
		return pref.ModelKey, pref.Thinking, nil
	}
	if err != gorm.ErrRecordNotFound {
		return "", false, fmt.Errorf("llmrouter.ResolveUserModel: get preference: %w", err)
	}

	// 级别 3：系统默认模型
	defaultModel, err := r.ds.LLMModel().GetDefaultModel(ctx)
	if err != nil {
		return "", false, fmt.Errorf("llmrouter.ResolveUserModel: get default model: %w", err)
	}

	return defaultModel.ModelKey, false, nil
}
