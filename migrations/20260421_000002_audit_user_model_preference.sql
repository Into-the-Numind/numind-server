-- Migration: 20260421_000002_audit_user_model_preference.sql
-- Feature:   aihubmix-protocol-audit (Task 7b) — Q10=A
-- Date:      2026-04-21
--
-- 对 user_model_preference 表中与 20260421_000001 修正相关的存量行做**审计 + 防御性 normalize**。
--
-- 背景：20260421_000001 把 DeepSeek/GPT base 的 thinking_only 从 1 改为 0。
-- 理论上 migration 前 preference.go:242 `if svc.ThinkingOnly { thinking = true }`
-- 会在 thinking_only=1 时强推 thinking=true，所以 user_model_preference 中
-- 'deepseek-v3.2' / 'gpt-5.4' 两个 model_key 的 thinking=0 行**理论上不存在**。
-- 实践上：如果手动 SQL / 旧代码缺陷 / migration 执行时机异常导致存在这类行，
-- migration 后这些偏好会真的生效（原来被 preference.go:242 强推为 true），
-- 出现静默行为变更窗口。
--
-- 本 migration 把这类偏差 normalize 掉（thinking=0 → 1），与原有被强推的用户体验对齐。
-- 若 affected_rows=0（预期情况）→ no-op，证明无历史缺陷。
--
-- NO ROLLBACK：Part B 的 UPDATE 无法精准逆操作（0→1 后无法区分原本是 0 还是 1）。
-- 若需撤销，走数据恢复流程（从备份还原 user_model_preference 表），非 SQL 逆操作。
-- 但因 affected_rows 预期 0，rollback 需求概率极低。

-- ============================================================
-- Part A 审计只读（SELECT 输出建议用 shell 脚本 migrations/audit/ 跑并 tee 到 log）
-- 本 migration 文件不含 Part A（SELECT 输出会被 migration runner 吞掉），
-- 独立脚本 `migrations/audit/20260421_preference_audit.sh` 承担审计职责
-- ============================================================

-- ============================================================
-- Part B 防御性 normalize
-- ============================================================

UPDATE user_model_preference
SET thinking = 1
WHERE model_key IN ('deepseek-v3.2', 'gpt-5.4')
  AND thinking = 0;

-- 验证（手工）：SELECT ROW_COUNT(); 记录实际更新行数到 audit log
-- 期望：0（如有历史偏差，可能 1-N）
