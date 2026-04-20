-- Feature: child-run-permission (Task 1 / spec §3.2)
-- Backfill：为所有"活跃权限记录 = 0"的存量子账号，写入父账号当前全部
-- 已发布 SOP 模板的白名单授权行。目的：在 HasTemplatePermission 从
-- "0 记录 = allow-all" 翻转为 "0 记录 = deny-all" 之前，冻结存量子账号
-- 当前的可见范围，防止翻转瞬间被误屏蔽。
--
-- 依赖：
--   * 本迁移必须在 feature 代码（Task 2 的语义翻转）部署之前执行。
--   * 部署顺序遵循 spec §6：先 migration（create + backfill），再 deploy code。
--
-- 幂等保护：
--   1) INSERT IGNORE —— UNIQUE KEY (sub_user_id, template_id) 冲突时跳过
--   2) NOT EXISTS —— 只为"当前无任何活跃权限记录"的子账号插入，避免二次
--      backfill 把人工 revoke 掉的行重新写回
--
-- P0 修复（S3 Gate review）：
--   NOT EXISTS 子查询必须加 `AND p.deleted_at IS NULL`。
--   user_template_permission 嵌入 gorm.Model，带软删除 DeletedAt。曾被父
--   账号撤权的子账号 hard rows 仍在，如果不过滤 deleted_at，这些子账号
--   会被 NOT EXISTS 误判为"已有记录"跳过 backfill，语义翻转后变成
--   deny-all 被误屏蔽。
--
-- parent_user_id 修复（S3 Gate review）：
--   user_template_permission.parent_user_id 是 NOT NULL 列，初版 spec
--   漏写，必须从 user.parent_user_id JOIN 取值一并填入。
--
-- 约束：
--   * 只处理"当前活跃记录 = 0"的子账号（软删除全部失效也算 0）
--   * sop_template 过滤 status='active' + publish_status='published' + 未软删
--   * user 过滤 parent_user_id IS NOT NULL（是子账号）+ 未软删
--   * 新写行的 created_at/updated_at 统一为 migration 执行时刻
--     —— 作为 rollback 脚本的时间窗口标记

INSERT IGNORE INTO `user_template_permission`
  (`parent_user_id`, `sub_user_id`, `template_id`, `created_at`, `updated_at`)
SELECT
  u.parent_user_id AS parent_user_id,
  u.id             AS sub_user_id,
  t.id             AS template_id,
  NOW(3)           AS created_at,
  NOW(3)           AS updated_at
FROM `user` u
CROSS JOIN `sop_template` t
WHERE
      u.parent_user_id IS NOT NULL
  AND u.deleted_at IS NULL
  AND t.status = 'active'
  AND t.publish_status = 'published'
  AND t.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1
    FROM `user_template_permission` p
    WHERE p.sub_user_id = u.id
      AND p.deleted_at IS NULL   -- 关键：软删除过滤，见 P0 修复说明
    LIMIT 1
  );
