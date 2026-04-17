-- Add model_name column to chatbot_message.
--
-- Context: ai-service-manager Gateway path reports the actual provider-resolved
-- model per request (via ChatChunk.Model), which is already propagated into
-- billing.TokenUsage.ModelName. Before this migration ChatbotMessage had no
-- column to persist it, so that field was inert.
--
-- After this migration + the matching server commit, every new assistant
-- message will record which model actually produced it, enabling audit,
-- per-model quality analysis and admin history-list display of the model used.
--
-- Idempotent via INFORMATION_SCHEMA check (MySQL 8.0 does NOT support
-- `ADD COLUMN IF NOT EXISTS` — that's MariaDB-only).

DROP PROCEDURE IF EXISTS _mig_chatbot_message_model_name;
DELIMITER //
CREATE PROCEDURE _mig_chatbot_message_model_name()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'chatbot_message'
      AND COLUMN_NAME = 'model_name'
  ) THEN
    ALTER TABLE chatbot_message
      ADD COLUMN model_name VARCHAR(100) NOT NULL DEFAULT ''
        COMMENT '生成 assistant 消息时实际使用的模型（Gateway 路径填充；user 消息留空）';
  END IF;
END //
DELIMITER ;

CALL _mig_chatbot_message_model_name();
DROP PROCEDURE _mig_chatbot_message_model_name;

-- Rollback: ALTER TABLE chatbot_message DROP COLUMN model_name;
