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

// resolveFeatureDefault 返回某 feature 对应的 Task Profile 的 default AI service。
// 若 task profile 未绑定 default，降级为该 feature allowed 列表中 sort_order 最小的 active service。
// 都找不到时返回 ErrNoDefaultService。
var ErrNoDefaultService = errors.New("no default service available for feature")

func (r *Router) resolveFeatureDefault(ctx context.Context, feature string) (*model.AIService, error) {
	taskID, ok := featureToTaskID[feature]
	if !ok {
		return nil, fmt.Errorf("llmrouter.resolveFeatureDefault: %w (got %q)", ErrInvalidFeature, feature)
	}

	var tp model.TaskProfile
	if err := r.ds.DB().WithContext(ctx).
		Where("task_id = ?", taskID).
		First(&tp).Error; err != nil {
		return nil, fmt.Errorf("llmrouter.resolveFeatureDefault: task profile %q not found: %w", taskID, err)
	}

	// 1. 优先返回 task profile 的 default_service（若 active + 未 deprecated）
	if tp.DefaultServiceID != nil {
		var svc model.AIService
		err := r.ds.DB().WithContext(ctx).
			Where("id = ? AND is_active = ? AND deprecated_at IS NULL", *tp.DefaultServiceID, true).
			First(&svc).Error
		if err == nil {
			return &svc, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("llmrouter.resolveFeatureDefault: load default service: %w", err)
		}
		// default 被下架，往下走 allowed 列表降级
	}

	// 2. 降级：allowed 列表 sort_order 最小的 active service
	var svc model.AIService
	if err := r.ds.DB().WithContext(ctx).
		Table("ai_service AS s").
		Joins("JOIN task_profile_service tps ON tps.service_id = s.id").
		Where("tps.task_profile_id = ? AND tps.role = ? AND s.is_active = ? AND s.deprecated_at IS NULL",
			tp.ID, model.TaskProfileRoleAllowed, true).
		Order("s.sort_order ASC, s.id ASC").
		Select("s.*").
		First(&svc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("llmrouter.resolveFeatureDefault: %w (feature %q)", ErrNoDefaultService, feature)
		}
		return nil, fmt.Errorf("llmrouter.resolveFeatureDefault: fallback lookup: %w", err)
	}

	return &svc, nil
}

// isAllowedForFeature reports whether `modelKey` is in the allowed set of the
// task profile mapped from `feature`, and returns the matching ai_service row.
func (r *Router) isAllowedForFeature(ctx context.Context, feature, modelKey string) (*model.AIService, error) {
	taskID, ok := featureToTaskID[feature]
	if !ok {
		return nil, fmt.Errorf("llmrouter.isAllowedForFeature: %w (got %q)", ErrInvalidFeature, feature)
	}

	var svc model.AIService
	if err := r.ds.DB().WithContext(ctx).
		Table("ai_service AS s").
		Joins("JOIN task_profile_service tps ON tps.service_id = s.id").
		Joins("JOIN task_profile tp ON tp.id = tps.task_profile_id").
		Where("tp.task_id = ? AND tps.role = ? AND s.model_key = ? AND s.is_active = ? AND s.deprecated_at IS NULL",
			taskID, model.TaskProfileRoleAllowed, modelKey, true).
		Select("s.*").
		First(&svc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("model %q is not an allowed service for feature %q", modelKey, feature)
		}
		return nil, fmt.Errorf("llmrouter.isAllowedForFeature: lookup: %w", err)
	}
	return &svc, nil
}

// GetPreferences 返回指定用户的所有功能偏好设置。
// 对于用户未设置的功能，使用该 feature 对应 Task Profile 的 default service 填充。
func (r *Router) GetPreferences(ctx context.Context, userID uint) (map[string]PreferenceResult, error) {
	prefs, err := r.ds.UserModelPreference().GetAll(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("llmrouter.GetPreferences: get all preferences: %w", err)
	}

	saved := make(map[string]model.UserModelPreference, len(prefs))
	for _, p := range prefs {
		saved[p.Feature] = p
	}

	result := make(map[string]PreferenceResult, len(validFeatures))
	for feature := range validFeatures {
		if p, ok := saved[feature]; ok {
			result[feature] = PreferenceResult{
				ModelKey: p.ModelKey,
				Thinking: p.Thinking,
			}
			continue
		}
		// 未设置：用该 feature 的 Task Profile default 填充
		svc, err := r.resolveFeatureDefault(ctx, feature)
		if err != nil {
			// 配置缺失不致命（前端 ModelSelector 仍能工作），留空 key
			result[feature] = PreferenceResult{}
			continue
		}
		result[feature] = PreferenceResult{
			ModelKey: svc.ModelKey,
			Thinking: false,
		}
	}

	return result, nil
}

// SavePreference 验证并保存用户的模型偏好。
// 验证规则：feature 必须是 chatbot/sop；model_key 必须在该 feature 的 allowed 集里
// 且 ai_service active + 未 deprecated；thinking 按模型能力校验。
func (r *Router) SavePreference(ctx context.Context, userID uint, feature, modelKey string, thinking bool) error {
	if _, ok := validFeatures[feature]; !ok {
		return fmt.Errorf("%w (got %q)", ErrInvalidFeature, feature)
	}

	svc, err := r.isAllowedForFeature(ctx, feature, modelKey)
	if err != nil {
		return err
	}

	if svc.IsThinking {
		return fmt.Errorf("model %q is a thinking variant, must use a base model", modelKey)
	}

	// thinking_only 模型始终强制 thinking=true
	if svc.ThinkingOnly {
		thinking = true
	}

	if thinking && !svc.SupportsThinking && !svc.ThinkingOnly {
		return fmt.Errorf("model %q does not support thinking mode", modelKey)
	}

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
//  3. 该 feature 的 Task Profile default service
func (r *Router) ResolveUserModel(ctx context.Context, userID uint, feature, queryModelKey string, queryThinking *bool) (string, bool, error) {
	if queryModelKey != "" {
		thinking := false
		if queryThinking != nil {
			thinking = *queryThinking
		}
		return queryModelKey, thinking, nil
	}

	pref, err := r.ds.UserModelPreference().Get(ctx, userID, feature)
	if err == nil {
		return pref.ModelKey, pref.Thinking, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, fmt.Errorf("llmrouter.ResolveUserModel: get preference: %w", err)
	}

	svc, err := r.resolveFeatureDefault(ctx, feature)
	if err != nil {
		return "", false, fmt.Errorf("llmrouter.ResolveUserModel: %w", err)
	}
	return svc.ModelKey, false, nil
}
