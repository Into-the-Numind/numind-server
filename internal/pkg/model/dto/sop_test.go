package dto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// TestToSopNodePublicDTO_HidesSensitiveFields 验证 SopNode → DTO 转换不泄露任何敏感字段。
//
// 这是 P0 安全测试。失败意味着 LLM 凭证或 B 端 IP 会被泄露给 C 端。
func TestToSopNodePublicDTO_HidesSensitiveFields(t *testing.T) {
	// 构造一个含全部敏感字段的 SopNode
	now := time.Now()
	node := &model.SopNode{
		Model: gorm.Model{
			ID:        42,
			CreatedAt: now,
			UpdatedAt: now,
		},
		TemplateID:     1,
		Name:           "AI拆解产品",
		Description:    "分析产品卖点",
		Status:         "active",
		Sort:           0,
		BaseURL:        "https://ark.cn-beijing.volces.com/api/v3", // 应被隐藏
		ModelName:      "deepseek-v3-2-251201",                     // 应被隐藏
		APIKey:         "sk-secret-token-1234567890abcdef0000",     // 应被隐藏
		TimeoutSeconds: 60,                                         // 应被隐藏
		Prompt:         "你是产品分析专家，请分析以下产品的核心卖点...",                 // 应被隐藏（B 端 IP）
		IsRoot:         true,                                       // 应被排除（dead field）
	}

	dtoObj := ToSopNodePublicDTO(node)
	jsonBytes, err := json.Marshal(dtoObj)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	jsonStr := string(jsonBytes)

	// 断言：JSON 中不能包含任何敏感字段名或敏感值
	forbiddenFields := []string{
		"api_key", "APIKey",
		"base_url", "BaseURL",
		"model_name", "ModelName",
		"timeout_seconds", "TimeoutSeconds",
		"prompt", "Prompt",
		"parent_id", "ParentID",
		"is_root", "IsRoot",
	}
	for _, field := range forbiddenFields {
		if strings.Contains(jsonStr, field) {
			t.Errorf("DTO leaks forbidden field %q in JSON: %s", field, jsonStr)
		}
	}

	forbiddenValues := []string{
		"sk-secret",
		"ark.cn-beijing",
		"deepseek-v3",
		"产品分析专家", // prompt 内容片段
	}
	for _, val := range forbiddenValues {
		if strings.Contains(jsonStr, val) {
			t.Errorf("DTO leaks forbidden value %q in JSON: %s", val, jsonStr)
		}
	}

	// 断言：DTO 必须保留的公开字段
	requiredFields := []string{
		`"id":42`,
		`"template_id":1`,
		`"name":"AI拆解产品"`,
		`"description":"分析产品卖点"`,
		`"sort":0`,
		`"status":"active"`,
		`"created_at"`,
		`"updated_at"`,
	}
	for _, field := range requiredFields {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("DTO missing required field/value %q. JSON: %s", field, jsonStr)
		}
	}
}

// TestToSopNodePublicDTO_EmptyDescription 验证空 description 字段不会渲染为 "null"。
//
// templateId=1, 2 的老节点 description 列为 NULL（实测 dev DB），DTO 必须输出空字符串。
func TestToSopNodePublicDTO_EmptyDescription(t *testing.T) {
	node := &model.SopNode{
		Model:       gorm.Model{ID: 1},
		TemplateID:  1,
		Name:        "AI拆解产品",
		Description: "", // 空 description
	}
	dtoObj := ToSopNodePublicDTO(node)
	jsonBytes, err := json.Marshal(dtoObj)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	jsonStr := string(jsonBytes)

	if !strings.Contains(jsonStr, `"description":""`) {
		t.Errorf("expected empty description as empty string, got: %s", jsonStr)
	}
	if strings.Contains(jsonStr, `"description":null`) {
		t.Errorf("description should not be null in JSON: %s", jsonStr)
	}
}

// TestToSopNodePublicDTO_NilInput 验证 nil 输入返回零值 DTO 而非 panic。
//
// 防御性测试：上游 store 异常返回 nil 时，转换函数不应 panic 扩散到 HTTP 层。
func TestToSopNodePublicDTO_NilInput(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ToSopNodePublicDTO(nil) panicked: %v", r)
		}
	}()
	dtoObj := ToSopNodePublicDTO(nil)
	if dtoObj.ID != 0 || dtoObj.Name != "" {
		t.Errorf("expected zero-value DTO, got: %+v", dtoObj)
	}
}

// TestToSopTemplatePublicDTO_NilInput 同 ToSopNodePublicDTO 的 nil 防御。
func TestToSopTemplatePublicDTO_NilInput(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ToSopTemplatePublicDTO(nil) panicked: %v", r)
		}
	}()
	dtoObj := ToSopTemplatePublicDTO(nil)
	if dtoObj.ID != 0 || dtoObj.Name != "" {
		t.Errorf("expected zero-value DTO, got: %+v", dtoObj)
	}
}

// TestToSopNodeEditDTO_HidesInfraFieldsKeepsPrompt 验证 B 端编辑器 DTO：
// 隐藏 4 基础设施字段（api_key/base_url/model_name/timeout_seconds），
// **保留** prompt 字段（与 PublicDTO 的关键差异）。
func TestToSopNodeEditDTO_HidesInfraFieldsKeepsPrompt(t *testing.T) {
	now := time.Now()
	node := &model.SopNode{
		Model: gorm.Model{
			ID:        42,
			CreatedAt: now,
			UpdatedAt: now,
		},
		TemplateID:     5,
		Name:           "AI拆解产品",
		Description:    "分析产品卖点",
		Status:         "active",
		Sort:           0,
		BaseURL:        "https://ark.cn-beijing.volces.com/api/v3",
		ModelName:      "deepseek-v3-2-251201",
		APIKey:         "sk-secret-token-1234567890abcdef0000",
		TimeoutSeconds: 60,
		Prompt:         "你是产品分析专家，请分析以下产品的核心卖点...",
	}

	dtoObj := ToSopNodeEditDTO(node)
	jsonBytes, err := json.Marshal(dtoObj)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	jsonStr := string(jsonBytes)

	// 必须隐藏：4 个基础设施字段
	forbidden := []string{
		"api_key", "APIKey",
		"base_url", "BaseURL",
		"model_name", "ModelName",
		"timeout_seconds", "TimeoutSeconds",
		"sk-secret",
		"ark.cn-beijing",
		"deepseek-v3",
	}
	for _, f := range forbidden {
		if strings.Contains(jsonStr, f) {
			t.Errorf("EditDTO leaks %q: %s", f, jsonStr)
		}
	}

	// 必须保留：prompt（与 PublicDTO 的关键差异）
	required := []string{
		`"id":42`,
		`"template_id":5`,
		`"name":"AI拆解产品"`,
		`"description":"分析产品卖点"`,
		`"prompt":"你是产品分析专家`, // prompt 必须存在
		`"sort":0`,
		`"status":"active"`,
	}
	for _, r := range required {
		if !strings.Contains(jsonStr, r) {
			t.Errorf("EditDTO missing %q: %s", r, jsonStr)
		}
	}
}

// TestToSopNodeEditDTO_NilInput 验证 nil 防御。
func TestToSopNodeEditDTO_NilInput(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ToSopNodeEditDTO(nil) panicked: %v", r)
		}
	}()
	dtoObj := ToSopNodeEditDTO(nil)
	if dtoObj.ID != 0 || dtoObj.Name != "" || dtoObj.Prompt != "" {
		t.Errorf("expected zero-value DTO, got: %+v", dtoObj)
	}
}

// TestToSopNodeEditDTOList_BatchConversion 批量转换正确性 + prompt 保留。
func TestToSopNodeEditDTOList_BatchConversion(t *testing.T) {
	nodes := []model.SopNode{
		{Model: gorm.Model{ID: 1}, Name: "Step 1", Prompt: "prompt-1", APIKey: "secret1"},
		{Model: gorm.Model{ID: 2}, Name: "Step 2", Prompt: "prompt-2", APIKey: "secret2"},
	}
	dtos := ToSopNodeEditDTOList(nodes)
	if len(dtos) != 2 {
		t.Fatalf("expected 2 DTOs, got %d", len(dtos))
	}
	jsonBytes, _ := json.Marshal(dtos)
	jsonStr := string(jsonBytes)

	// 不应有 secret
	if strings.Contains(jsonStr, "secret") || strings.Contains(jsonStr, "api_key") {
		t.Errorf("EditDTO batch leaks secrets: %s", jsonStr)
	}
	// 必须有 prompt
	if !strings.Contains(jsonStr, "prompt-1") || !strings.Contains(jsonStr, "prompt-2") {
		t.Errorf("EditDTO batch missing prompts: %s", jsonStr)
	}
}

// TestToSopNodePublicDTOList_BatchConversion 验证批量转换函数正确性。
func TestToSopNodePublicDTOList_BatchConversion(t *testing.T) {
	nodes := []model.SopNode{
		{Model: gorm.Model{ID: 1}, Name: "Step 1", APIKey: "secret1"},
		{Model: gorm.Model{ID: 2}, Name: "Step 2", APIKey: "secret2"},
		{Model: gorm.Model{ID: 3}, Name: "Step 3", APIKey: "secret3"},
	}
	dtos := ToSopNodePublicDTOList(nodes)
	if len(dtos) != 3 {
		t.Fatalf("expected 3 DTOs, got %d", len(dtos))
	}
	jsonBytes, _ := json.Marshal(dtos)
	jsonStr := string(jsonBytes)
	if strings.Contains(jsonStr, "secret") || strings.Contains(jsonStr, "api_key") {
		t.Errorf("batch conversion leaks secrets: %s", jsonStr)
	}
}

// TestToSopTemplatePublicDTO_HidesSensitiveFields 验证 template DTO 不泄露 prompt 和 creator_user_id。
func TestToSopTemplatePublicDTO_HidesSensitiveFields(t *testing.T) {
	creatorID := uint(25)
	now := time.Now()
	tmpl := &model.SopTemplate{
		Model: gorm.Model{
			ID:        1,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Name:                "流量选题口播稿",
		Description:         "借热点输观点",
		Status:              "active",
		PublishStatus:       "published",
		TrailingChatEnabled: true,
		Prompt:              "你是一名口播文案专家...", // 应被隐藏
		CreatorUserID:       &creatorID,      // 应被隐藏
	}
	dtoObj := ToSopTemplatePublicDTO(tmpl)
	jsonBytes, _ := json.Marshal(dtoObj)
	jsonStr := string(jsonBytes)

	forbidden := []string{
		"prompt", "Prompt",
		"creator_user_id", "CreatorUserID",
		"口播文案专家", // prompt 内容
	}
	for _, f := range forbidden {
		if strings.Contains(jsonStr, f) {
			t.Errorf("template DTO leaks %q: %s", f, jsonStr)
		}
	}

	required := []string{
		`"id":1`,
		`"name":"流量选题口播稿"`,
		`"description":"借热点输观点"`,
		`"status":"active"`,
		`"publish_status":"published"`,
		`"trailing_chat_enabled":true`,
	}
	for _, r := range required {
		if !strings.Contains(jsonStr, r) {
			t.Errorf("template DTO missing %q: %s", r, jsonStr)
		}
	}
}
