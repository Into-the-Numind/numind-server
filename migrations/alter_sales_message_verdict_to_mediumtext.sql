-- 修复 verdict 列过短导致 "Data too long" 错误
-- verdict 存储知识引用 JSON，文档多时可能超过 TEXT 的 64KB 限制
ALTER TABLE sales_message MODIFY COLUMN verdict MEDIUMTEXT;
