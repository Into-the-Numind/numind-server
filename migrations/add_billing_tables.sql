-- 用量明细记录表: 每次外部 API 调用记录一条
CREATE TABLE IF NOT EXISTS `usage_record` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` int unsigned NOT NULL COMMENT '用户ID',
  `service_type` varchar(50) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '服务类型: llm_chat, llm_vision, embedding, rerank, cos_upload, file_extract, vector_db',
  `provider` varchar(50) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '服务商: ali, volc, dmxapi, cos, vikingdb, dashvector, bailian',
  `model` varchar(100) COLLATE utf8mb4_unicode_ci DEFAULT '' COMMENT '模型名称',
  `operation` varchar(100) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务扣费点: sop_node_execute, salesrag_chat 等',
  `prompt_tokens` int NOT NULL DEFAULT 0,
  `completion_tokens` int NOT NULL DEFAULT 0,
  `total_tokens` int NOT NULL DEFAULT 0,
  `reasoning_tokens` int NOT NULL DEFAULT 0,
  `estimated_prompt_tokens` int NOT NULL DEFAULT 0,
  `bytes_uploaded` bigint NOT NULL DEFAULT 0 COMMENT 'COS 上传字节数',
  `item_count` int NOT NULL DEFAULT 0 COMMENT '向量操作条数 / Rerank 文档数',
  `cost_cents` bigint NOT NULL DEFAULT 0 COMMENT '预估成本（分）',
  `biz_ref_type` varchar(50) COLLATE utf8mb4_unicode_ci DEFAULT '' COMMENT '关联业务对象类型',
  `biz_ref_id` int unsigned DEFAULT 0 COMMENT '关联业务对象ID',
  `is_fallback` tinyint(1) NOT NULL DEFAULT 0 COMMENT '是否为降级调用',
  `metadata` json DEFAULT NULL COMMENT '额外上下文',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  INDEX `idx_ur_user_created` (`user_id`, `created_at`),
  INDEX `idx_ur_user_cost` (`user_id`, `created_at`, `cost_cents`),
  INDEX `idx_ur_service` (`service_type`),
  INDEX `idx_ur_operation` (`operation`),
  INDEX `idx_ur_created` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用量明细记录表';

-- 用户计费账户表
CREATE TABLE IF NOT EXISTS `billing_account` (
  `id` int unsigned NOT NULL AUTO_INCREMENT,
  `user_id` int unsigned NOT NULL COMMENT '用户ID',
  `balance_cents` bigint NOT NULL DEFAULT 0 COMMENT '当前余额（分）',
  `total_consumed_cents` bigint NOT NULL DEFAULT 0 COMMENT '累计消费（分）',
  `total_recharged_cents` bigint NOT NULL DEFAULT 0 COMMENT '累计充值（分）',
  `status` varchar(20) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT 'active, suspended, frozen',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE INDEX `idx_ba_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户计费账户';

-- 定价规则表
CREATE TABLE IF NOT EXISTS `pricing_rule` (
  `id` int unsigned NOT NULL AUTO_INCREMENT,
  `service_type` varchar(50) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '服务类型',
  `provider` varchar(50) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '服务商',
  `model` varchar(100) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '模型名称，空=默认价格',
  `input_price_per_mtok` decimal(10,4) NOT NULL DEFAULT 0 COMMENT '每百万输入 tokens 价格（元）',
  `output_price_per_mtok` decimal(10,4) NOT NULL DEFAULT 0 COMMENT '每百万输出 tokens 价格（元）',
  `price_per_call` decimal(10,4) NOT NULL DEFAULT 0 COMMENT '每次调用价格（元）',
  `price_per_gb` decimal(10,4) NOT NULL DEFAULT 0 COMMENT '每 GB 价格（元）',
  `is_active` tinyint(1) NOT NULL DEFAULT 1,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE INDEX `uk_pricing_lookup` (`service_type`, `provider`, `model`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='定价规则表';
