package llmrouter

import (
	"context"
	"errors"
	"fmt"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// featureToTaskID maps C-side feature keys to Task Profile IDs.
// Source of truth: manifest decisions — ai-service-manager inherits
// `user_selectable=true` only for chatbot.stream + sop.text (see §5.3 of
// the ai-service-manager design spec).
var featureToTaskID = map[string]string{
	"chatbot": "chatbot.stream",
	"sop":     "sop.text",
}

// validFeatures is derived from featureToTaskID so the two stay in sync.
var validFeatures = func() map[string]struct{} {
	m := make(map[string]struct{}, len(featureToTaskID))
	for k := range featureToTaskID {
		m[k] = struct{}{}
	}
	return m
}()

// ErrInvalidFeature indicates the caller supplied a feature string that is not
// in the allowed set (chatbot / sop). Controllers should map this to HTTP 400.
var ErrInvalidFeature = errors.New("invalid feature: must be one of chatbot, sop")

// PreferenceResult 用户模型偏好查询结果
type PreferenceResult struct {
	ModelKey string `json:"model_key"`
	Thinking bool   `json:"thinking"`
}

// GetModels 返回该 feature 对应的 Task Profile 的 allowed services（过滤 active
// 且未 deprecated），按 sort_order 排序。默认模型取 Task Profile 的
// default_service（若它不在 allowed 列表则降级为 list[0]）。
//
// feature 为空时兼容老调用方，按 "chatbot" 处理。
func (r *Router) GetModels(ctx context.Context, feature string) ([]model.LLMModel, string, error) {
	if feature == "" {
		feature = "chatbot"
	}
	taskID, ok := featureToTaskID[feature]
	if !ok {
		return nil, "", fmt.Errorf("llmrouter.GetModels: %w (got %q)", ErrInvalidFeature, feature)
	}

	// 1. 查 Task Profile（拿 id + default_service_id）
	var tp model.TaskProfile
	if err := r.ds.DB().WithContext(ctx).
		Where("task_id = ?", taskID).
		First(&tp).Error; err != nil {
		return nil, "", fmt.Errorf("llmrouter.GetModels: task profile %q not found: %w", taskID, err)
	}

	// 2. 查该 task 的 allowed services（JOIN ai_service 过滤 active + 未 deprecated）
	var services []model.AIService
	if err := r.ds.DB().WithContext(ctx).
		Table("ai_service AS s").
		Joins("JOIN task_profile_service tps ON tps.service_id = s.id").
		Where("tps.task_profile_id = ? AND tps.role = ? AND s.is_active = ? AND s.deprecated_at IS NULL",
			tp.ID, model.TaskProfileRoleAllowed, true).
		Order("s.sort_order ASC, s.id ASC").
		Select("s.*").
		Scan(&services).Error; err != nil {
		return nil, "", fmt.Errorf("llmrouter.GetModels: list allowed services for task %q: %w", taskID, err)
	}

	// 3. 映射 ai_service → LLMModel（保持 response shape 向后兼容 v3 ModelSelector）
	models := make([]model.LLMModel, 0, len(services))
	defaultKey := ""
	for _, s := range services {
		models = append(models, model.LLMModel{
			ID:               s.ID,
			ModelKey:         s.ModelKey,
			DisplayName:      s.DisplayName,
			IsThinking:       s.IsThinking,
			BaseModelID:      s.BaseModelID,
			SupportsThinking: s.SupportsThinking,
			ThinkingOnly:     s.ThinkingOnly,
			Icon:             s.Icon,
			SortOrder:        s.SortOrder,
			IsActive:         s.IsActive,
			CreatedAt:        s.CreatedAt,
			UpdatedAt:        s.UpdatedAt,
		})
		if tp.DefaultServiceID != nil && s.ID == *tp.DefaultServiceID {
			defaultKey = s.ModelKey
		}
	}

	// 4. 若 default_service 不在 allowed 列表（管理员配置缺失），降级到 list[0]
	if defaultKey == "" && len(models) > 0 {
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

	// thinking_only 模型始终强制 thinking=true
	if m.ThinkingOnly {
		thinking = true
	}

	// 若请求开启 thinking，验证模型是否支持
	if thinking && !m.SupportsThinking && !m.ThinkingOnly {
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
