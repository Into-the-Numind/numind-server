-- Drop the account-dependent execution gate before the workspace rollback.

DROP TABLE IF EXISTS `feishu_operation_execution_gate`;
