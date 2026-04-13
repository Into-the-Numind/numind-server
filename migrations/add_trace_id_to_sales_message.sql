-- 为 sales_message 添加 Langfuse trace_id 字段
ALTER TABLE `sales_message` ADD COLUMN `trace_id` VARCHAR(255) DEFAULT '' AFTER `images`;
CREATE INDEX `idx_sales_message_trace_id` ON `sales_message` (`trace_id`);
