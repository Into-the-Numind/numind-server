# 数据库字符集修复指南

## 🚨 问题描述

你遇到的错误是典型的MySQL字符集不匹配问题：

```
Error 3988 (HY000): Conversion from collation utf8mb3_general_ci into utf8mb4_0900_ai_ci impossible for parameter
```

**问题原因**：
- 数据库或表使用旧的 `utf8mb3` 字符集
- 应用程序尝试插入包含中文字符的内容
- `utf8mb3` 不支持完整的Unicode字符（包括中文、emoji等）
- 系统尝试转换为 `utf8mb4` 但转换失败

## 🔍 问题分析

### 错误详情
- **错误位置**: `chat_message` 表的 `content` 字段
- **错误内容**: 包含中文字符的智能回复内容
- **字符集**: 从 `utf8mb3_general_ci` 转换到 `utf8mb4_0900_ai_ci`

### 影响范围
- WebSocket聊天功能无法正常工作
- 智能回复无法保存到数据库
- 中文字符显示异常

## 🛠️ 解决方案

### 方案1：使用自动化修复脚本（推荐）

```bash
# 运行自动化修复脚本
./scripts/fix_database_charset.sh
```

该脚本会：
1. 自动备份数据库
2. 修复数据库字符集
3. 修复所有相关表
4. 特别修复 `chat_message.content` 字段
5. 验证修复结果

### 方案2：手动执行SQL修复

```bash
# 连接到数据库
mysql -u root -p numind

# 执行修复脚本
source scripts/quick_fix_charset.sql
```

### 方案3：分步手动修复

```sql
-- 1. 修复数据库字符集
ALTER DATABASE numind CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 2. 修复chat_message表
ALTER TABLE chat_message CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 3. 特别修复content字段
ALTER TABLE chat_message MODIFY COLUMN content TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 4. 修复其他相关表
ALTER TABLE chat_session CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE book CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE user CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

## 📋 修复步骤

### 1. 备份数据库（重要！）
```bash
mysqldump -u root -p --single-transaction --routines --triggers numind > backup_$(date +%Y%m%d_%H%M%S).sql
```

### 2. 执行修复
选择上述任一方案执行修复

### 3. 验证修复结果
```sql
-- 检查数据库字符集
SELECT SCHEMA_NAME, DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME 
FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = 'numind';

-- 检查表字符集
SELECT TABLE_NAME, TABLE_COLLATION 
FROM information_schema.TABLES 
WHERE TABLE_SCHEMA = 'numind' AND TABLE_NAME IN ('chat_message', 'chat_session', 'book', 'user');

-- 检查content字段字符集
SELECT CHARACTER_SET_NAME, COLLATION_NAME 
FROM information_schema.COLUMNS 
WHERE TABLE_SCHEMA = 'numind' AND TABLE_NAME = 'chat_message' AND COLUMN_NAME = 'content';
```

### 4. 重启应用程序
```bash
# 停止当前服务
# 重新启动
go run cmd/numind/main.go
```

## 🔧 预防措施

### 1. 数据库配置
在MySQL配置文件中添加：
```ini
[mysqld]
character-set-server = utf8mb4
collation-server = utf8mb4_unicode_ci

[mysql]
default-character-set = utf8mb4
```

### 2. 连接字符串
确保应用程序连接字符串包含正确的字符集：
```
charset=utf8mb4&collation=utf8mb4_unicode_ci
```

### 3. 表创建规范
创建新表时明确指定字符集：
```sql
CREATE TABLE example (
    id INT PRIMARY KEY,
    content TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

## 📊 字符集对比

| 字符集 | 支持范围 | 存储空间 | 兼容性 |
|--------|----------|----------|--------|
| `utf8mb3` | 基本多语言平面 | 3字节/字符 | MySQL 5.7+ |
| `utf8mb4` | 完整Unicode | 4字节/字符 | MySQL 5.7+ |

**推荐使用**: `utf8mb4_unicode_ci`

## 🧪 测试验证

### 1. 基本功能测试
```bash
# 测试WebSocket连接
python3 scripts/test_smart_chat.py
```

### 2. 中文字符测试
发送包含中文字符的消息，验证：
- 消息能正常保存到数据库
- 中文字符显示正常
- 智能回复功能正常

### 3. 数据库验证
```sql
-- 插入测试数据
INSERT INTO chat_message (session_id, user_id, role, content) 
VALUES (1, 1, 'user', '测试中文字符：你好世界！');

-- 查询验证
SELECT * FROM chat_message WHERE content LIKE '%中文%';
```

## 🚨 注意事项

### 1. 备份重要性
- 修复前必须备份数据库
- 修复过程不可逆
- 建议在测试环境先验证

### 2. 性能影响
- 大表转换可能需要较长时间
- 建议在低峰期执行
- 监控数据库性能

### 3. 应用程序兼容性
- 确保应用程序支持utf8mb4
- 检查连接池配置
- 验证ORM框架设置

## 🔍 故障排除

### 如果修复失败

1. **检查权限**
```sql
SHOW GRANTS FOR CURRENT_USER();
```

2. **检查表状态**
```sql
SHOW TABLE STATUS WHERE Name = 'chat_message';
```

3. **检查锁状态**
```sql
SHOW PROCESSLIST;
```

4. **查看错误日志**
```bash
tail -f /var/log/mysql/error.log
```

### 常见错误及解决方案

| 错误 | 原因 | 解决方案 |
|------|------|----------|
| `Access denied` | 权限不足 | 使用有足够权限的用户 |
| `Table is locked` | 表被锁定 | 等待锁释放或强制解锁 |
| `Disk space full` | 磁盘空间不足 | 清理磁盘空间 |

## 📞 技术支持

如果问题仍然存在：

1. 检查MySQL版本和配置
2. 查看完整的错误日志
3. 确认数据库连接配置
4. 联系数据库管理员

## 🎯 预期结果

修复成功后：
- ✅ 中文字符正常显示和存储
- ✅ WebSocket聊天功能正常工作
- ✅ 智能回复功能正常保存
- ✅ 数据库性能保持稳定
- ✅ 支持emoji和特殊字符

---

**重要提醒**: 修复字符集是解决当前问题的根本方法，建议在生产环境执行前先在测试环境验证。
