-- 定价规则种子数据（基于 DEC-028 生产成本分析）
-- 价格单位：元/百万 tokens (per_mtok)、元/次 (per_call)、元/GB (per_gb)
-- sell 价格暂与 cost 价格相同（积分制下售价通过积分映射体现，不在 pricing_rule 层加价）

-- ============================================================
-- 1. LLM Chat 服务
-- ============================================================

-- DeepSeek V3（SOP 主力模型，通过 Volcengine 调用）
-- 定价：≤32K 档 input ¥1/MTok output ¥2/MTok；>32K 档 input ¥2/MTok output ¥8/MTok
-- 使用 tiered_token 模式处理分段定价
INSERT INTO pricing_rule (service_type, provider, model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
  is_active, created_at, updated_at)
VALUES
-- DeepSeek V3 (Volc) — 使用加权平均价 (78.2% ≤32K + 21.8% >32K)
('llm_chat', 'volc', 'deepseek-v3-2-251201', 'flat', 'call',
  1.2184, 3.3080, 0, 0,
  1.2184, 3.3080, 0, 0,
  1, NOW(), NOW()),

-- DeepSeek V3 (DMXAPI, SalesRAG 回复生成用) — 同价
('llm_chat', 'dmxapi', 'deepseek-v3-2-251201', 'flat', 'call',
  1.0, 2.0, 0, 0,
  1.0, 2.0, 0, 0,
  1, NOW(), NOW()),

-- Qwen-Turbo-Latest (DMXAPI, SalesRAG 意图分析/策略选择)
-- 定价：input ¥0.3/MTok output ¥0.6/MTok
('llm_chat', 'dmxapi', 'qwen-turbo-latest', 'flat', 'call',
  0.3, 0.6, 0, 0,
  0.3, 0.6, 0, 0,
  1, NOW(), NOW()),

-- Doubao-Seed-2-0-Lite (Volc, 客户画像/语言风格分析)
-- 定价：input ¥0.3/MTok output ¥0.6/MTok
('llm_chat', 'volc', 'doubao-seed-2-0-lite-260215', 'flat', 'call',
  0.3, 0.6, 0, 0,
  0.3, 0.6, 0, 0,
  1, NOW(), NOW()),

-- Volc 默认模型 (config 配置的 volc.model)
('llm_chat', 'volc', '', 'flat', 'call',
  1.0, 2.0, 0, 0,
  1.0, 2.0, 0, 0,
  1, NOW(), NOW()),

-- Ali 默认 LLM (config 配置)
('llm_chat', 'ali', '', 'flat', 'call',
  0.3, 0.6, 0, 0,
  0.3, 0.6, 0, 0,
  1, NOW(), NOW()),

-- ============================================================
-- 2. LLM Vision 服务
-- ============================================================

-- Qwen-VL-Plus (Ali)
('llm_vision', 'ali', 'qwen-vl-plus', 'flat', 'call',
  3.0, 8.0, 0, 0,
  3.0, 8.0, 0, 0,
  1, NOW(), NOW()),

-- Qwen3-VL (Ali)
('llm_vision', 'ali', 'qwen3-vl', 'flat', 'call',
  1.5, 4.5, 0, 0,
  1.5, 4.5, 0, 0,
  1, NOW(), NOW()),

-- Doubao Vision (Volc)
('llm_vision', 'volc', 'doubao-seed-1-8-251228', 'flat', 'call',
  2.0, 6.0, 0, 0,
  2.0, 6.0, 0, 0,
  1, NOW(), NOW()),

-- Volc 默认 Vision
('llm_vision', 'volc', '', 'flat', 'call',
  2.0, 6.0, 0, 0,
  2.0, 6.0, 0, 0,
  1, NOW(), NOW()),

-- ============================================================
-- 3. Embedding 服务
-- ============================================================

-- Text-Embedding-V4 (Ali)
-- 定价：¥0.7/MTok
('embedding', 'ali', 'text-embedding-v4', 'flat', 'call',
  0.7, 0, 0, 0,
  0.7, 0, 0, 0,
  1, NOW(), NOW()),

-- Doubao-Embedding-Vision (Volc)
('embedding', 'volc', 'doubao-embedding-vision-250615', 'flat', 'call',
  0.35, 0, 0, 0,
  0.35, 0, 0, 0,
  1, NOW(), NOW()),

-- ============================================================
-- 4. Rerank 服务
-- ============================================================

-- Qwen3-Rerank (DMXAPI, SalesRAG 知识库/话术重排序)
-- 定价：¥0.5/MTok（按 token 计费）
('rerank', 'dmxapi', 'qwen3-rerank', 'flat', 'call',
  0.5, 0, 0, 0,
  0.5, 0, 0, 0,
  1, NOW(), NOW()),

-- ============================================================
-- 5. 文件解析服务
-- ============================================================

-- Qwen-Long (Ali Bailian, 文件解析)
-- 定价：约 ¥0.03/次
('file_extract', 'bailian', '', 'flat', 'call',
  0, 0, 0.03, 0,
  0, 0, 0.03, 0,
  1, NOW(), NOW()),

-- 百度 OCR (按次计费)
-- 定价：¥0.01/次
('file_extract', 'baidu', '', 'flat', 'call',
  0, 0, 0.01, 0,
  0, 0, 0.01, 0,
  1, NOW(), NOW()),

-- ============================================================
-- 6. COS 存储
-- ============================================================

-- 腾讯云 COS
('cos_upload', 'cos', '', 'flat', 'gb',
  0, 0, 0, 0.099,
  0, 0, 0, 0.099,
  1, NOW(), NOW()),

-- ============================================================
-- 7. 向量数据库
-- ============================================================

-- VikingDB (Volc)
('vector_db', 'vikingdb', '', 'flat', 'call',
  0, 0, 0.001, 0,
  0, 0, 0.001, 0,
  1, NOW(), NOW()),

-- DashVector (Ali)
('vector_db', 'dashvector', '', 'flat', 'call',
  0, 0, 0.001, 0,
  0, 0, 0.001, 0,
  1, NOW(), NOW())
;
