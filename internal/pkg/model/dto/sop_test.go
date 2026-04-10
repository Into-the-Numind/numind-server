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
		ModelName:      "deepseek-v3-2-251201",                    // 应被隐藏
		APIKey:         "sk-secret-token-1234567890abcdef0000",    // 应被隐藏
		TimeoutSeconds: 60,                                        // 应被隐藏
		Prompt:         "你是产品分析专家，请分析以下产品的核心卖点...", // 应被隐藏（B 端 IP）
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
	jsonBytes, _ := json.Marshal(dtoObj)
	jsonStr := string(jsonBytes)

	if !strings.Contains(jsonStr, `"description":""`) {
		t.Errorf("expected empty description as empty string, got: %s", jsonStr)
	}
	if strings.Contains(jsonStr, `"description":null`) {
		t.Errorf("description should not be null in JSON: %s", jsonStr)
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
		CreatorUserID:       &creatorID,           // 应被隐藏
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
