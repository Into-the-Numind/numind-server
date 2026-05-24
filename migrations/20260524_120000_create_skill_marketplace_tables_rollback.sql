-- agent-mode-v2-skill-marketplace T01 rollback
-- 倒序删 2 张表（无 FK，顺序不严格）
-- 不动 #1 的 skill 表 ENUM（imported_from_marketplace 已是 #1 预留值，保留不影响）

DROP TABLE IF EXISTS skill_subscription;
DROP TABLE IF EXISTS skill_marketplace;
