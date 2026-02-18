# 阶段 3: wecom 数据表清理指南

## 执行环境

在 MySQL 所在服务器或能连接 MySQL 的机器上执行。

## 数据库配置（从 config 读取）

- Host: `49.233.219.254:13306`
- Database: `numind-dev` (开发环境) / `numind` (生产环境)
- Username: `root`
- Password: `Numind2025`

## 执行步骤

### 步骤 1: 检查当前表状态

```bash
mysql -h 49.233.219.254 -P 13306 -u root -pNumind2025 numind-dev < scripts/check-wecom-tables.sql
```

预期输出：
```
+------------------+---------------+---------------+
| table_name       | estimated_rows| data_size_mb  |
+------------------+---------------+---------------+
| wecom_bind_codes | X             | X.XX          |
| wecom_cursors    | X             | X.XX          |
| wecom_messages   | X             | X.XX          |
| wecom_users      | X             | X.XX          |
+------------------+---------------+---------------+
```

### 步骤 2: 备份数据（重要！）

```bash
# 方案 A: 导出为 SQL 文件
mysqldump -h 49.233.219.254 -P 13306 -u root -pNumind2025 numind-dev \
  wecom_users wecom_messages wecom_cursors wecom_bind_codes \
  > wecom_backup_$(date +%Y%m%d_%H%M%S).sql

# 方案 B: 在同库中创建备份表
mysql -h 49.233.219.254 -P 13306 -u root -pNumind2025 numind-dev < scripts/backup-wecom-tables.sql
```

### 步骤 3: 确认备份成功

```bash
mysql -h 49.233.219.254 -P 13306 -u root -pNumind2025 numind-dev -e "
SHOW TABLES LIKE '%backup%';
"
```

应该看到：
- wecom_bind_codes_backup
- wecom_cursors_backup  
- wecom_messages_backup
- wecom_users_backup

### 步骤 4: 执行删除

```bash
mysql -h 49.233.219.254 -P 13306 -u root -pNumind2025 numind-dev < scripts/drop-wecom-tables.sql
```

预期输出：
```
+------------------+-----------+
| table_name       | status    |
+------------------+-----------+
| wecom_users      | 已删除    |
| wecom_messages   | 已删除    |
| wecom_cursors    | 已删除    |
| wecom_bind_codes | 已删除    |
+------------------+-----------+
```

### 步骤 5: 最终验证

```bash
mysql -h 49.233.219.254 -P 13306 -u root -pNumind2025 numind-dev -e "SHOW TABLES LIKE 'wecom%';"
```

预期输出：空（或者只显示备份表）

## 回滚方案

如需恢复数据：

```bash
# 从备份文件恢复
mysql -h 49.233.219.254 -P 13306 -u root -pNumind2025 numind-dev < wecom_backup_YYYYMMDD_HHMMSS.sql

# 或从备份表恢复
mysql -h 49.233.219.254 -P 13306 -u root -pNumind2025 numind-dev -e "
CREATE TABLE wecom_users AS SELECT * FROM wecom_users_backup;
CREATE TABLE wecom_messages AS SELECT * FROM wecom_messages_backup;
CREATE TABLE wecom_cursors AS SELECT * FROM wecom_cursors_backup;
CREATE TABLE wecom_bind_codes AS SELECT * FROM wecom_bind_codes_backup;
"
```

## 清理备份（一个月后）

确认系统稳定运行一个月后，可删除备份：

```bash
mysql -h 49.233.219.254 -P 13306 -u root -pNumind2025 numind-dev -e "
DROP TABLE IF EXISTS wecom_users_backup;
DROP TABLE IF EXISTS wecom_messages_backup;
DROP TABLE IF EXISTS wecom_cursors_backup;
DROP TABLE IF EXISTS wecom_bind_codes_backup;
"
```
