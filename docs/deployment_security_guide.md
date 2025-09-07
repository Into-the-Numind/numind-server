# 支付安全措施部署指南

## 概述

本指南详细说明了如何将支付安全措施部署到生产环境，确保系统的安全性和可靠性。

## 部署前检查清单

### 1. 代码安全检查

- [ ] 所有价格验证逻辑已实现
- [ ] 服务端价格计算已启用
- [ ] 审计日志记录已配置
- [ ] 单元测试已通过
- [ ] 安全测试脚本已准备

### 2. 数据库迁移

#### 创建支付审计日志表

```sql
-- 创建支付审计日志表
CREATE TABLE payment_audit_logs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    out_trade_no VARCHAR(100) NOT NULL COMMENT '商户订单号',
    user_id BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
    amount BIGINT NOT NULL COMMENT '支付金额(分)',
    membership_type VARCHAR(20) NOT NULL COMMENT '会员类型',
    package_count INT DEFAULT 0 COMMENT '包次数',
    subscription_days INT DEFAULT 0 COMMENT '订阅天数',
    status VARCHAR(20) NOT NULL COMMENT '支付状态',
    transaction_id VARCHAR(100) DEFAULT '' COMMENT '微信支付订单号',
    paid_at TIMESTAMP NULL COMMENT '支付时间',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_out_trade_no (out_trade_no),
    INDEX idx_user_id (user_id),
    INDEX idx_status (status),
    INDEX idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='支付审计日志表';
```

#### 更新现有表结构（如果需要）

```sql
-- 确保用户表有正确的字段
ALTER TABLE user 
ADD COLUMN IF NOT EXISTS is_pro BOOLEAN DEFAULT FALSE COMMENT '是否为付费用户',
ADD COLUMN IF NOT EXISTS membership_type VARCHAR(20) DEFAULT 'free' COMMENT '会员类型',
ADD COLUMN IF NOT EXISTS membership_expires TIMESTAMP NULL COMMENT '会员到期时间',
ADD COLUMN IF NOT EXISTS package_count INT DEFAULT 0 COMMENT '资源包剩余次数';

-- 添加索引
ALTER TABLE user 
ADD INDEX IF NOT EXISTS idx_membership_type (membership_type),
ADD INDEX IF NOT EXISTS idx_membership_expires (membership_expires);
```

### 3. 配置文件更新

#### 环境变量配置

```bash
# 支付相关配置
PAYMENT_PRICE_VALIDATION_ENABLED=true
PAYMENT_AUDIT_LOG_ENABLED=true
PAYMENT_SECURITY_LEVEL=high

# 价格白名单（JSON格式）
SUBSCRIPTION_PRICES='{"2800":30,"19800":365}'
PACKAGE_PRICES='{"1":300,"5":1200,"20":3800,"50":5000}'

# 审计日志配置
AUDIT_LOG_RETENTION_DAYS=365
AUDIT_LOG_LEVEL=info
```

#### 配置文件示例 (config_prod.yaml)

```yaml
# 支付安全配置
payment:
  security:
    enabled: true
    price_validation: true
    audit_logging: true
    security_level: "high"
  
  # 价格白名单
  prices:
    subscription:
      30: 2800   # 30天 -> 28元
      365: 19800 # 365天 -> 198元
    package:
      1: 300     # 1次 -> 3元
      5: 1200    # 5次 -> 12元
      20: 3800   # 20次 -> 38元
      50: 5000   # 50次 -> 50元

# 审计日志配置
audit:
  enabled: true
  retention_days: 365
  level: "info"
  storage: "database" # 或 "file"
```

### 4. 监控和告警配置

#### 关键指标监控

```yaml
# Prometheus监控配置
monitoring:
  metrics:
    - name: payment_validation_failures
      description: "支付验证失败次数"
      type: counter
      labels: ["membership_type", "reason"]
    
    - name: payment_audit_logs
      description: "支付审计日志数量"
      type: counter
      labels: ["status"]
    
    - name: payment_amount_total
      description: "支付总金额"
      type: counter
      labels: ["membership_type"]
    
    - name: payment_security_violations
      description: "支付安全违规次数"
      type: counter
      labels: ["violation_type"]
```

#### 告警规则

```yaml
# Alertmanager告警规则
alerts:
  - alert: PaymentValidationFailure
    expr: rate(payment_validation_failures[5m]) > 0.1
    for: 1m
    labels:
      severity: warning
    annotations:
      summary: "支付验证失败率过高"
      description: "过去5分钟支付验证失败率超过10%"
  
  - alert: PaymentSecurityViolation
    expr: rate(payment_security_violations[5m]) > 0
    for: 0m
    labels:
      severity: critical
    annotations:
      summary: "检测到支付安全违规"
      description: "检测到支付安全违规行为，需要立即处理"
```

## 部署步骤

### 1. 代码部署

```bash
# 1. 拉取最新代码
git pull origin main

# 2. 运行测试
go test ./internal/numind/biz/payment/ -v
go test ./internal/numind/controller/v1/membership/ -v

# 3. 构建应用
go build -o numind-server cmd/numind/main.go

# 4. 停止旧服务
sudo systemctl stop numind-server

# 5. 备份当前版本
cp numind-server numind-server.backup.$(date +%Y%m%d_%H%M%S)

# 6. 部署新版本
sudo cp numind-server /usr/local/bin/
sudo chmod +x /usr/local/bin/numind-server

# 7. 启动新服务
sudo systemctl start numind-server
sudo systemctl status numind-server
```

### 2. 数据库迁移

```bash
# 运行数据库迁移
mysql -u username -p database_name < migrations/payment_security.sql

# 验证表结构
mysql -u username -p database_name -e "DESCRIBE payment_audit_logs;"
mysql -u username -p database_name -e "DESCRIBE user;"
```

### 3. 配置更新

```bash
# 更新配置文件
sudo cp config_prod.yaml /etc/numind-server/
sudo systemctl reload numind-server

# 验证配置
curl -X GET "http://localhost:8080/v1/membership/plans" | jq '.'
```

### 4. 安全测试

```bash
# 运行安全测试
./scripts/test_payment_security.sh

# 检查日志
sudo journalctl -u numind-server -f | grep -E "(价格验证|支付审计|安全违规)"
```

## 生产环境配置

### 1. 反向代理配置 (Nginx)

```nginx
# /etc/nginx/sites-available/numind-server
server {
    listen 443 ssl http2;
    server_name your-domain.com;
    
    # SSL配置
    ssl_certificate /path/to/certificate.crt;
    ssl_certificate_key /path/to/private.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-RSA-AES256-GCM-SHA512:DHE-RSA-AES256-GCM-SHA512;
    
    # 安全头
    add_header X-Frame-Options DENY;
    add_header X-Content-Type-Options nosniff;
    add_header X-XSS-Protection "1; mode=block";
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains";
    
    # API路由
    location /v1/membership/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        
        # 请求大小限制
        client_max_body_size 1M;
        
        # 超时配置
        proxy_connect_timeout 30s;
        proxy_send_timeout 30s;
        proxy_read_timeout 30s;
    }
    
    # 日志配置
    access_log /var/log/nginx/numind-server-access.log;
    error_log /var/log/nginx/numind-server-error.log;
}
```

### 2. 系统服务配置

```ini
# /etc/systemd/system/numind-server.service
[Unit]
Description=Numind Server
After=network.target mysql.service

[Service]
Type=simple
User=numind
Group=numind
WorkingDirectory=/opt/numind-server
ExecStart=/usr/local/bin/numind-server
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5

# 环境变量
Environment=GIN_MODE=release
Environment=CONFIG_FILE=/etc/numind-server/config_prod.yaml
Environment=PAYMENT_SECURITY_ENABLED=true

# 资源限制
LimitNOFILE=65536
LimitNPROC=32768

# 安全配置
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/numind-server/logs

[Install]
WantedBy=multi-user.target
```

### 3. 日志配置

```yaml
# /etc/numind-server/logging.yaml
version: 1
disable_existing_loggers: false

formatters:
  default:
    format: '%(asctime)s - %(name)s - %(levelname)s - %(message)s'
  json:
    format: '{"timestamp": "%(asctime)s", "level": "%(levelname)s", "logger": "%(name)s", "message": "%(message)s"}'

handlers:
  console:
    class: logging.StreamHandler
    level: INFO
    formatter: default
    stream: ext://sys.stdout
  
  file:
    class: logging.handlers.RotatingFileHandler
    level: INFO
    formatter: json
    filename: /opt/numind-server/logs/numind-server.log
    maxBytes: 10485760  # 10MB
    backupCount: 5
  
  audit:
    class: logging.handlers.RotatingFileHandler
    level: INFO
    formatter: json
    filename: /opt/numind-server/logs/payment-audit.log
    maxBytes: 10485760  # 10MB
    backupCount: 10

loggers:
  payment.audit:
    level: INFO
    handlers: [audit]
    propagate: false
  
  payment.security:
    level: WARNING
    handlers: [file, console]
    propagate: false

root:
  level: INFO
  handlers: [console, file]
```

## 监控和维护

### 1. 健康检查

```bash
#!/bin/bash
# /opt/numind-server/scripts/health_check.sh

# 检查服务状态
if ! systemctl is-active --quiet numind-server; then
    echo "ERROR: numind-server service is not running"
    exit 1
fi

# 检查API响应
if ! curl -f -s http://localhost:8080/v1/membership/plans > /dev/null; then
    echo "ERROR: API health check failed"
    exit 1
fi

# 检查数据库连接
if ! mysql -u username -p password -e "SELECT 1" > /dev/null 2>&1; then
    echo "ERROR: Database connection failed"
    exit 1
fi

echo "Health check passed"
exit 0
```

### 2. 日志分析

```bash
#!/bin/bash
# /opt/numind-server/scripts/analyze_payment_logs.sh

# 分析支付验证失败
echo "=== 支付验证失败分析 ==="
grep "价格验证失败" /opt/numind-server/logs/numind-server.log | \
  jq -r '.membership_type, .amount, .error' | \
  sort | uniq -c | sort -nr

# 分析安全违规
echo "=== 安全违规分析 ==="
grep "支付安全违规" /opt/numind-server/logs/numind-server.log | \
  jq -r '.violation_type, .user_id, .amount' | \
  sort | uniq -c | sort -nr

# 分析支付金额
echo "=== 支付金额统计 ==="
grep "Payment audit log" /opt/numind-server/logs/payment-audit.log | \
  jq -r '.amount, .membership_type' | \
  awk '{sum[$2]+=$1} END {for(i in sum) print i, sum[i]}' | \
  sort -k2 -nr
```

### 3. 定期维护

```bash
#!/bin/bash
# /opt/numind-server/scripts/maintenance.sh

# 清理旧日志
find /opt/numind-server/logs -name "*.log.*" -mtime +30 -delete

# 清理审计日志（保留1年）
mysql -u username -p password -e "
DELETE FROM payment_audit_logs 
WHERE created_at < DATE_SUB(NOW(), INTERVAL 1 YEAR);"

# 优化数据库
mysql -u username -p password -e "OPTIMIZE TABLE payment_audit_logs;"

# 备份重要数据
mysqldump -u username -p password \
  --single-transaction \
  --routines \
  --triggers \
  numind_server > /backup/numind_server_$(date +%Y%m%d).sql
```

## 故障排除

### 1. 常见问题

#### 问题：价格验证失败
```bash
# 检查日志
grep "价格验证失败" /opt/numind-server/logs/numind-server.log

# 检查配置
curl -X GET "http://localhost:8080/v1/membership/plans" | jq '.data.plans[0]'
```

#### 问题：审计日志未记录
```bash
# 检查审计日志配置
grep "audit" /etc/numind-server/config_prod.yaml

# 检查数据库表
mysql -u username -p password -e "SELECT COUNT(*) FROM payment_audit_logs;"
```

#### 问题：用户is_pro字段未更新
```bash
# 检查支付处理日志
grep "Membership purchase processed" /opt/numind-server/logs/numind-server.log

# 检查用户表
mysql -u username -p password -e "SELECT id, is_pro, membership_type FROM user WHERE id = USER_ID;"
```

### 2. 紧急回滚

```bash
#!/bin/bash
# /opt/numind-server/scripts/emergency_rollback.sh

echo "开始紧急回滚..."

# 停止服务
sudo systemctl stop numind-server

# 恢复备份
sudo cp numind-server.backup.* /usr/local/bin/numind-server
sudo chmod +x /usr/local/bin/numind-server

# 恢复配置
sudo cp config_prod.yaml.backup /etc/numind-server/config_prod.yaml

# 启动服务
sudo systemctl start numind-server

# 验证服务
sleep 10
if systemctl is-active --quiet numind-server; then
    echo "回滚成功"
else
    echo "回滚失败，需要手动处理"
fi
```

## 总结

通过以上部署指南，可以确保支付安全措施在生产环境中正确运行：

1. **完整的安全验证**：防止价格篡改和参数伪造
2. **详细的审计日志**：记录所有支付操作用于审计
3. **全面的监控告警**：及时发现和处理安全问题
4. **可靠的故障恢复**：快速响应和解决问题

定期检查和维护这些安全措施，确保系统的长期稳定运行。
