-- Rollback: drop skill.origin_type added by 20260528_150000_skill_add_origin_type.sql
-- 注意：rollback 会丢失所有 origin_type 数据（官方预置/租户私有/用户私有标记），
--      仅当需要回滚 commit e1e1450 (origin_type 特性) 时执行。

ALTER TABLE skill DROP COLUMN IF EXISTS origin_type;
