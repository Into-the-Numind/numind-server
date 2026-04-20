-- Migration: 20260421_000001_fix_ai_service_thinking_flags.sql
-- Feature:   aihubmix-protocol-audit (Task 7a)
-- Date:      2026-04-21
--
-- 背景：2026-04-20 dev DB 查询发现 ai_service 表中 6/8 AiHubMix 路由行的
-- supports_thinking / thinking_only 值错误。根因：
-- migrations/20260416_100000_seed_aihubmix_provider.sql 写入时未按 T2
-- 协议实测结果赋值，且这两列从老表 llm_model RENAME 继承的默认值有偏差。
--
-- T2 协议实测依据：docs/aihubmix-protocol-reference.md
-- 线上 bug：llmrouter/preference.go:246 因 supports_thinking=0 硬拒对
-- thinking 变体模型（claude-sonnet-4-6-thinking / *-thinking）的 thinking=true 请求。
-- 本 migration 修正后 bug 自愈，无需代码改动。
--
-- 修正映射表（基于 T2 实测，docs/aihubmix-protocol-reference.md §2.5）：
--   id=1  claude-sonnet-4-6                  (1, 0) 保持 optional ✅
--   id=5  claude-sonnet-4-6-thinking         (0,0) → (1,1) intrinsic  fix
--   id=12 gemini-3.1-pro-preview             (1, 1) 保持 intrinsic（T2 证实 none/minimal 返 400，无法关闭思考）
--   id=13 deepseek-v3.2                      (1,1) → (1,0) optional   fix（T2 证实 effort=none 可关 reasoning）
--   id=14 gpt-5.4                            (1,1) → (1,0) optional   fix（T2 证实可通过 effort 切换）
--   id=15 gemini-3.1-pro-preview-thinking    (0,0) → (1,1) intrinsic  fix
--   id=16 deepseek-v3.2-thinking             (0,0) → (1,1) intrinsic  fix
--   id=17 gpt-5.4-thinking                   (0,0) → (1,1) intrinsic  fix
--
-- 共 6 行需修正（id 5, 13, 14, 15, 16, 17）。
-- 使用 model_key 而非 id 做 WHERE，跨环境（dev/qa/prod）稳定。
--
-- ROLLBACK: migrations/20260421_000001_fix_ai_service_thinking_flags_rollback.sql
-- （含 P2-F pre-check guard，防止半更新状态下误 rollback）

-- Pre-flight guard: 期望命中 8 行（AiHubMix 所有路由），否则 migration abort
-- 若少于 8 行说明路由 seed 不完整，本 migration 不应执行
SELECT 1 / (
  (SELECT COUNT(*) FROM ai_service s
   JOIN ai_service_route r ON r.model_id = s.id
   JOIN llm_provider p ON p.id = r.provider_id
   WHERE p.name = 'aihubmix')
  - 7
) AS aihubmix_row_guard;
-- 期望：8 行 → 8-7=1 → 1/1 成功
-- 不足：≤7 行 → 分母 ≤ 0 → 除零失败 → migration abort

-- (1) Claude thinking 变体 → intrinsic (1, 1)
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 1
WHERE model_key = 'claude-sonnet-4-6-thinking';

-- (2) DeepSeek base → optional (1, 0)
-- 理由：T2 实测 reasoning_effort=none 可成功关闭 thinking（reasoning_tokens 字段消失）
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 0
WHERE model_key = 'deepseek-v3.2';

-- (3) GPT 5.4 base → optional (1, 0)
-- 理由：T2 实测 reasoning_effort=low 时 reasoning_tokens=0（相当于关闭），high 产生 reasoning
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 0
WHERE model_key = 'gpt-5.4';

-- (4) Gemini thinking 变体 → intrinsic (1, 1)
-- 理由：provider_model_id 是 base slug，Gemini 原生思考永远触发
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 1
WHERE model_key = 'gemini-3.1-pro-preview-thinking';

-- (5) DeepSeek thinking 变体 → intrinsic (1, 1)
-- 理由：provider_model_id='deepseek-v3.2-think'，-think 后缀触发固定思考
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 1
WHERE model_key = 'deepseek-v3.2-thinking';

-- (6) GPT 5.4 thinking 变体 → intrinsic (1, 1)
-- 理由：provider_model_id 是 base slug，UI 语义约定固定思考
UPDATE ai_service
SET supports_thinking = 1, thinking_only = 1
WHERE model_key = 'gpt-5.4-thinking';

-- Post-flight verification：应返回 8 行，各自标志值符合 §2.5 表
-- 手工执行：
--   SELECT s.model_key, s.supports_thinking, s.thinking_only
--   FROM ai_service s
--   WHERE s.model_key IN (
--       'claude-sonnet-4-6', 'claude-sonnet-4-6-thinking',
--       'gemini-3.1-pro-preview', 'gemini-3.1-pro-preview-thinking',
--       'deepseek-v3.2', 'deepseek-v3.2-thinking',
--       'gpt-5.4', 'gpt-5.4-thinking'
--   ) ORDER BY s.id;
--
-- 期望输出：
--   claude-sonnet-4-6               | 1 | 0
--   claude-sonnet-4-6-thinking      | 1 | 1
--   gemini-3.1-pro-preview          | 1 | 1
--   deepseek-v3.2                   | 1 | 0
--   gpt-5.4                         | 1 | 0
--   gemini-3.1-pro-preview-thinking | 1 | 1
--   deepseek-v3.2-thinking          | 1 | 1
--   gpt-5.4-thinking                | 1 | 1
