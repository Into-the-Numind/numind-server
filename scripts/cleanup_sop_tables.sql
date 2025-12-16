-- 清理SOP相关表（用于修复外键依赖问题）
-- 按照依赖的逆序删除表

-- 5. 删除笔记表（依赖最多）
DROP TABLE IF EXISTS `sop_note`;

-- 4. 删除节点执行记录表
DROP TABLE IF EXISTS `sop_node_run`;

-- 3. 删除执行记录表
DROP TABLE IF EXISTS `sop_run`;

-- 2. 删除节点表
DROP TABLE IF EXISTS `sop_node`;

-- 1. 删除模板表（无依赖）
DROP TABLE IF EXISTS `sop_template`;

-- 执行完成后，重新启动服务，AutoMigrate会按正确顺序重新创建表
