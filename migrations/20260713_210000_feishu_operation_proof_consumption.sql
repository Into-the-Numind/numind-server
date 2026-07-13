-- One-shot consumption ledger for same-run empty-create overwrite proofs.
-- Lock order is account -> source operation -> consumer operation -> consumption.

CREATE TABLE `feishu_operation_proof_consumption` (
  `source_operation_id` CHAR(36) NOT NULL,
  `consumer_operation_id` CHAR(36) NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `generation` BIGINT UNSIGNED NOT NULL,
  `agent_run_id` BIGINT UNSIGNED NOT NULL,
  `created_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`source_operation_id`),
  UNIQUE KEY `uniq_feishu_proof_consumer` (`consumer_operation_id`),
  KEY `idx_feishu_proof_audit` (`user_id`, `generation`, `agent_run_id`),
  CONSTRAINT `fk_feishu_proof_source_operation`
    FOREIGN KEY (`source_operation_id`) REFERENCES `feishu_operation` (`id`) ON DELETE RESTRICT,
  CONSTRAINT `fk_feishu_proof_consumer_operation`
    FOREIGN KEY (`consumer_operation_id`) REFERENCES `feishu_operation` (`id`) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;
