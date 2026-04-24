-- 20260424_204500_seed_deepseek_v4_pro_rollback.sql
--
-- 回滚 20260424_204500_seed_deepseek_v4_pro.sql。
-- 按引用顺序逆序删除：pricing_rule → ai_service_route → ai_service。

-- ============================================================
-- 1. pricing_rule — 2 行
-- ============================================================
DELETE FROM pricing_rule
WHERE model = 'deepseek-v4-pro'
  AND provider IN ('dmxapi', 'aihubmix')
  AND service_type = 'llm_chat';


-- ============================================================
-- 2. ai_service_route — 2 行
-- ============================================================
DELETE r FROM ai_service_route r
JOIN ai_service s ON s.id = r.model_id
WHERE s.model_key = 'deepseek-v4-pro';


-- ============================================================
-- 3. ai_service — 1 行
-- ============================================================
DELETE FROM ai_service WHERE model_key = 'deepseek-v4-pro';
