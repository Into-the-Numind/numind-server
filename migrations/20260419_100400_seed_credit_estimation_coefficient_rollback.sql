-- Rollback: Remove seed rows
DELETE FROM credit_estimation_coefficient WHERE change_reason = 'initial from S3 spike';
