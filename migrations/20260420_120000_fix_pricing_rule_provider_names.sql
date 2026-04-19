-- 20260420_120000_fix_pricing_rule_provider_names.sql
--
-- 修复 pricing_rule 表中的 provider 名称，使其与 llm_provider.name 对齐。
--
-- 背景：pricing_rule seed 使用了短名称（ali, volc, baidu, bailian），
-- 但 llm_provider 实际注册的 name 是全称（ali-dashscope, volc-ark, baidu-ocr, bailian-file）。
-- 导致 billing middleware 用 route.Provider.Name 查 pricing_rule 时，
-- 命中不了 provider 级规则，全部 fallback 到全局兜底价（¥3/¥10 MTok）。
--
-- 修复范围：仅更新 AI 服务相关的 provider 名称。
-- cos / vikingdb / dashvector 属于存储/向量服务，不走 aiservice gateway，不受影响。
--
-- 幂等性：WHERE 条件精确匹配旧名称，重复执行不会出错。

-- 阿里系：ali → ali-dashscope
UPDATE pricing_rule SET provider = 'ali-dashscope', updated_at = NOW(3) WHERE provider = 'ali';

-- 火山系：volc → volc-ark
UPDATE pricing_rule SET provider = 'volc-ark', updated_at = NOW(3) WHERE provider = 'volc';

-- 百度 OCR：baidu → baidu-ocr
UPDATE pricing_rule SET provider = 'baidu-ocr', updated_at = NOW(3) WHERE provider = 'baidu';

-- 百炼文件：bailian → bailian-file
UPDATE pricing_rule SET provider = 'bailian-file', updated_at = NOW(3) WHERE provider = 'bailian';

-- dmxapi / aihubmix 已经与 llm_provider.name 一致，无需修改。

-- ============================================================
-- 同步修复 credit_estimation_coefficient 表的 provider 名称
-- （当前所有系数值相同，走全局兜底不影响计算结果，但修正后
--   精确匹配能命中，Langfuse/日志中不会出现 fallback 告警）
-- ============================================================
UPDATE credit_estimation_coefficient SET provider = 'ali-dashscope', updated_at = NOW(3) WHERE provider = 'ali';
UPDATE credit_estimation_coefficient SET provider = 'volc-ark', updated_at = NOW(3) WHERE provider = 'volc';
