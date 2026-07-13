-- Cross-process, account-wide lease for real lark-cli business invocations.
-- Every claimant locks user_third_party_account before this row.

CREATE TABLE `feishu_operation_execution_gate` (
  `user_id` BIGINT UNSIGNED NOT NULL,
  `generation` BIGINT UNSIGNED NOT NULL,
  `lease_owner` VARCHAR(128) NOT NULL DEFAULT '',
  `operation_id` CHAR(36) NOT NULL DEFAULT '',
  `lease_until` DATETIME(3) NULL,
  `updated_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`user_id`),
  KEY `idx_feishu_execution_gate_lease` (`lease_until`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;
