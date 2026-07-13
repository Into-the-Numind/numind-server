-- Drop the dependent proof ledger before any rollback that removes feishu_operation.

DROP TABLE IF EXISTS `feishu_operation_proof_consumption`;
