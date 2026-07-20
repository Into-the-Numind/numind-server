package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

const (
	threeAgentSSOTSHA256 = "fc2bea1b8e05ddd285975120d0b7b401a56ed69683f90a63a4fa30f907dc66f5"
	threeAgentSeparator  = "\n\n--- 以下为不可删改的业务判断规则 ---\n\n"
)

type threeAgentManifest struct {
	SchemaVersion string                    `json:"schema_version"`
	SSOTFile      string                    `json:"ssot_file"`
	SSOTSHA256    string                    `json:"ssot_sha256"`
	Agents        []threeAgentManifestEntry `json:"agents"`
}

type threeAgentManifestEntry struct {
	Key                 string          `json:"key"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	WelcomeMessage      string          `json:"welcome_message"`
	Starters            []string        `json:"starters"`
	RuntimeContractFile string          `json:"runtime_contract_file"`
	PromptFile          string          `json:"prompt_file"`
	PromptSHA256        string          `json:"prompt_sha256"`
	SSOTSHA256          string          `json:"ssot_sha256"`
	ToolFlags           map[string]bool `json:"tool_flags"`
}

func threeAgentRepoRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "..", ".."))
}

func readThreeAgentFile(t *testing.T, root, relative string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	require.NoError(t, err, "read %s", relative)
	return body
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func extractAuthoritativePrompts(t *testing.T, ssot []byte) [3]string {
	t.Helper()
	text := string(ssot)
	const prompt2Heading = "# Prompt 2｜客户核心信息与人群画像提炼"
	const prompt3Heading = "# Prompt 3｜选题规划 Agent：选题生成"
	i2 := strings.Index(text, prompt2Heading)
	i3 := strings.Index(text, prompt3Heading)
	require.Greater(t, i2, 0)
	require.Greater(t, i3, i2)
	require.Equal(t, 1, strings.Count(text, "# Prompt 1｜"))
	require.Equal(t, 1, strings.Count(text, prompt2Heading))
	require.Equal(t, 1, strings.Count(text, prompt3Heading))
	return [3]string{text[:i2], text[i2:i3], text[i3:]}
}

func applyAuthorizedBusinessPatches(t *testing.T, prompts [3]string) [3]string {
	t.Helper()
	const prompt1Anchor = "| 跨赛道理由 | 一句话说明原因 |\n\n| 推导链 | 一句话串联整体判断 |"
	const prompt1Replacement = "| 跨赛道理由 | 一句话说明原因 |\n\n| 可借鉴部分 | 后续可以复用的钩子、结构、表达、情绪或判断方式 |\n\n| 不可照搬部分 | 不能复用的行业事实、人物、事件、政策或特殊资源 |\n\n| 推导链 | 一句话串联整体判断 |"
	const prompt3Anchor = "- 选题内容：\n\n- 归属小类："
	const prompt3Replacement = "- 选题内容：\n\n- 选择原因：用一句话说明为什么适合该客户\n\n- 归属小类："

	require.Equal(t, 1, strings.Count(prompts[0], prompt1Anchor), "Prompt 1 patch anchor must occur exactly once")
	require.Equal(t, 1, strings.Count(prompts[2], prompt3Anchor), "Prompt 3 patch anchor must occur exactly once")
	return [3]string{
		strings.Replace(prompts[0], prompt1Anchor, prompt1Replacement, 1),
		prompts[1],
		strings.Replace(prompts[2], prompt3Anchor, prompt3Replacement, 1),
	}
}

func composeThreeAgentPrompt(runtimeContract, businessPrompt string) string {
	return strings.TrimRight(runtimeContract, "\n") + threeAgentSeparator + businessPrompt
}

func expectedThreeAgentFlags(_ string) map[string]bool {
	all := []string{
		"kb_search", "document_generate", "image_gen", "bash_exec", "get_current_date",
		"web_search", "web_fetch", "ask_user_question", "file_read", "analyze_image",
		"annotate_image", "load_skill", "create_csv", "create_html", "create_json",
		"create_text", "create_docx", "create_png_chart", "run_python", "memory_write",
		"memory_read", "xhs_note_list", "lark_skill_read", "lark_inspect", "lark_execute",
		"code_sandbox", "media", "dangerous",
	}
	out := make(map[string]bool, len(all))
	for _, name := range all {
		out[name] = name != "document_generate"
	}
	return out
}

func requireContainsAll(t *testing.T, body string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		assert.Contains(t, body, fragment)
	}
}

func TestThreeAgentDefinitionContract(t *testing.T) {
	root := threeAgentRepoRoot(t)
	const docsDir = "docs/agent-definitions/three-agent-feishu-pipeline"
	manifestBody := readThreeAgentFile(t, root, docsDir+"/manifest.json")
	var manifest threeAgentManifest
	require.NoError(t, json.Unmarshal(manifestBody, &manifest))
	require.Equal(t, "numind-three-agent-definitions/v1", manifest.SchemaVersion)
	require.Equal(t, threeAgentSSOTSHA256, manifest.SSOTSHA256)
	require.Len(t, manifest.Agents, 3)

	ssot := readThreeAgentFile(t, root, manifest.SSOTFile)
	require.Equal(t, threeAgentSSOTSHA256, sha256Hex(ssot))
	authoritative := extractAuthoritativePrompts(t, ssot)
	patched := applyAuthorizedBusinessPatches(t, authoritative)
	require.Equal(t, authoritative[1], patched[1], "Agent 2 business prompt must remain byte-identical to the SSOT section")
	require.Equal(t, 1, strings.Count(patched[0], "| 可借鉴部分 | 后续可以复用的钩子、结构、表达、情绪或判断方式 |"))
	require.Equal(t, 1, strings.Count(patched[0], "| 不可照搬部分 | 不能复用的行业事实、人物、事件、政策或特殊资源 |"))
	require.Equal(t, 1, strings.Count(patched[2], "- 选择原因：用一句话说明为什么适合该客户"))

	entries := make(map[string]threeAgentManifestEntry, 3)
	finalPrompts := make(map[string]string, 3)
	expectedOrder := []string{"agent-1", "agent-2", "agent-3"}
	for i, entry := range manifest.Agents {
		require.Equal(t, expectedOrder[i], entry.Key, "manifest order is part of deterministic SSOT composition")
		require.NotEmpty(t, entry.Key)
		_, duplicate := entries[entry.Key]
		require.False(t, duplicate, "duplicate agent key %s", entry.Key)
		entries[entry.Key] = entry
		require.Equal(t, threeAgentSSOTSHA256, entry.SSOTSHA256)
		require.NotEmpty(t, entry.Name)
		require.NotEmpty(t, entry.Description)
		require.NotEmpty(t, entry.WelcomeMessage)
		require.NotEmpty(t, entry.Starters)

		runtimeContract := string(readThreeAgentFile(t, root, entry.RuntimeContractFile))
		finalPrompt := string(readThreeAgentFile(t, root, entry.PromptFile))
		expected := composeThreeAgentPrompt(runtimeContract, patched[i])
		require.Equal(t, expected, finalPrompt, "%s final prompt must be a deterministic composition", entry.Key)
		require.NotEmpty(t, finalPrompt)
		require.LessOrEqual(t, len(finalPrompt), SystemPromptMaxLen)
		require.Equal(t, entry.PromptSHA256, sha256Hex([]byte(finalPrompt)))
		require.Equal(t, expectedThreeAgentFlags(entry.Key), entry.ToolFlags)
		finalPrompts[entry.Key] = finalPrompt

		for _, forbidden := range []string{
			"organization_id", "parent_user_id", "agent_definition_id", "tenant_access_token",
			"app_access_token", "base_token=", "doc_token=", "app_token=", "bascn", "doccn", "wikcn",
		} {
			assert.NotContains(t, finalPrompt, forbidden, "%s must not embed environment identity/token material", entry.Key)
		}
	}
	require.Equal(t, []string{"agent-1", "agent-2", "agent-3"}, sortedManifestKeys(entries))

	common := []string{
		"目标缺失时询问用户", "精确搜索结果为 0 个", "精确搜索结果为 1 个", "精确搜索结果大于 1 个",
		"官方飞书授权", "恢复原工具调用", "unknown", "不得盲目重放写操作", "长期记忆不能作为正确性依据",
		"numind-pipeline-report/v1",
	}
	for key, prompt := range finalPrompts {
		t.Run(key+"-common-runtime", func(t *testing.T) { requireContainsAll(t, prompt, common...) })
	}

	requireContainsAll(t, finalPrompts["agent-1"],
		"自动扫描当前用户的小红书选题库", "不要求用户手动勾选", "默认不重新分析已完成记录",
		"小红书笔记ID", "分析状态=已完成", "分析规则版本", "每条笔记独立分析",
		"可借鉴部分", "不可照搬部分", "只做分析与打标，不生成选题，不改写正文",
		"processed", "skipped", "remaining", "failed",
	)
	requireContainsAll(t, finalPrompts["agent-2"],
		"上传文件和当前用户飞书资料可以混合输入", "has_more=false", "客户级完整最新版",
		"资料来源判断", "账号定位素材", "核心人群画像", "向内求素材库", "第三方素材说明",
		"深度看见候选点", "资料缺口清单", "不生成选题",
	)
	requireContainsAll(t, finalPrompts["agent-3"],
		"Agent 1 的爆款素材库", "Agent 2 的客户画像卡", "选择原因", "九个字段",
		"蓝 V", "0-1", "round_id", "append", "str_replace", "修改指定轮次",
		"不生成完整正文、口播稿或小红书成稿",
	)

	verifyThreeAgentServicePersistence(t, manifest.Agents, finalPrompts)
}

func sortedManifestKeys(entries map[string]threeAgentManifestEntry) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func verifyThreeAgentServicePersistence(t *testing.T, entries []threeAgentManifestEntry, prompts map[string]string) {
	t.Helper()
	svc, db := newTestService(t)
	ownerID := seedParentUserID(db)
	otherParent := &model.User{Username: "other-parent-three-agent-contract"}
	require.NoError(t, db.Create(otherParent).Error)
	ctx := context.Background()

	for _, entry := range entries {
		entry := entry
		t.Run(entry.Key+"-service-persistence", func(t *testing.T) {
			oldPrompt := "旧版本占位：" + entry.Key
			oldFlags := map[string]bool{"web_search": true}
			created, err := svc.Create(ctx, ownerID, CreateRequest{
				Name: "旧名称-" + entry.Key, Description: "旧描述", WelcomeMessage: "旧欢迎语",
				SystemPrompt: oldPrompt, Starters: []string{"旧入口"}, ToolFlags: oldFlags,
			})
			require.NoError(t, err)

			prompt := prompts[entry.Key]
			patched, err := svc.Patch(ctx, ownerID, created.ID, PatchRequest{
				Name: &entry.Name, Description: &entry.Description, WelcomeMessage: &entry.WelcomeMessage,
				SystemPrompt: &prompt, Starters: &entry.Starters, ToolFlags: &entry.ToolFlags,
			})
			require.NoError(t, err)
			require.Equal(t, uint(2), patched.Version)

			got, err := svc.Get(ctx, ownerID, created.ID)
			require.NoError(t, err)
			assert.Equal(t, entry.Name, got.Name)
			assert.Equal(t, prompt, got.SystemPrompt)
			assertJSONMapEquals(t, entry.ToolFlags, got.ToolFlags)

			history, err := svc.ListHistory(ctx, ownerID, created.ID)
			require.NoError(t, err)
			require.Len(t, history, 2)
			var latest, original model.AgentDefinition
			require.NoError(t, json.Unmarshal(history[0].Snapshot, &latest))
			require.NoError(t, json.Unmarshal(history[1].Snapshot, &original))
			assert.Equal(t, sha256Hex([]byte(prompt)), sha256Hex([]byte(latest.SystemPrompt)))
			assertJSONMapEquals(t, entry.ToolFlags, latest.ToolFlags)
			assert.Equal(t, sha256Hex([]byte(oldPrompt)), sha256Hex([]byte(original.SystemPrompt)))
			assertJSONMapEquals(t, oldFlags, original.ToolFlags)

			items, total, err := svc.List(ctx, otherParent.ID, true, 1, 100)
			require.NoError(t, err)
			assert.Zero(t, total)
			assert.Empty(t, items)
			_, err = svc.Get(ctx, otherParent.ID, created.ID)
			assert.ErrorIs(t, err, errno.ErrSkillNotFound)
			_, err = svc.Patch(ctx, otherParent.ID, created.ID, PatchRequest{Name: &entry.Name})
			assert.ErrorIs(t, err, errno.ErrSkillNotFound)
		})
	}
}

func assertJSONMapEquals(t *testing.T, want map[string]bool, raw []byte) {
	t.Helper()
	var got map[string]bool
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, want, got)
}
