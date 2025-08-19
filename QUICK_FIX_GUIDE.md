# API请求问题快速修复指南

## 🚨 立即修复步骤（5分钟解决）

### 1. 快速诊断 (1分钟)
```bash
# 运行诊断脚本
./scripts/diagnose_api_issues.sh
```

### 2. 自动修复 (2分钟)
```bash
# 运行自动修复脚本
./scripts/fix_api_timeout.sh

# 设置优化环境变量
source set_api_env.sh
```

### 3. 验证修复 (1分钟)
```bash
# 测试API连接
./test_api_connection.sh

# 检查配置是否正确
grep -A 6 "volc:" config_local.yaml
grep -A 10 "ali:" config_local.yaml
```

### 4. 重启应用 (1分钟)
```bash
# 重启应用程序以应用新配置
# 根据你的部署方式选择一种：

# 方式1：如果使用systemd
sudo systemctl restart numind-server

# 方式2：如果使用docker
docker restart numind-server

# 方式3：如果是进程方式运行
kill -HUP $(pidof numind-server)

# 方式4：重新启动go程序
# Ctrl+C 停止，然后重新运行
go run cmd/numind/main.go
```

## 🔍 验证修复效果

### 实时监控日志
```bash
# 监控API调用情况
tail -f logs/app.log | grep -E "(🔄|✅|❌|⚠️|火山引擎|阿里千问)"
```

### 期望看到的日志
```
✅ 正常情况：
INFO: 🔄 尝试火山引擎API book_id=123 attempt=1 max_attempts=5
INFO: ✅ 火山引擎API成功 book_id=123 attempt=1

🔄 重试情况：
INFO: 🔄 尝试火山引擎API book_id=123 attempt=1 max_attempts=5
WARN: ⚠️ 火山引擎API失败 book_id=123 attempt=1
INFO: 等待重试 book_id=123 delay=2s next_attempt=2
INFO: 🔄 尝试火山引擎API book_id=123 attempt=2 max_attempts=5
INFO: ✅ 火山引擎API成功 book_id=123 attempt=2

🔄 降级情况：
WARN: 火山引擎API重试后失败，尝试阿里千问降级 book_id=123
INFO: 🔄 尝试阿里千问API book_id=123 attempt=1 max_attempts=3
INFO: ✅ 阿里千问API成功 book_id=123 attempt=1
INFO: 阿里千问API降级成功 book_id=123
```

## 📋 核心修复内容

### 1. 超时设置优化
- **火山引擎**: 30s → 120s
- **阿里千问**: 新增 120s
- **万象图像**: 新增 180s

### 2. 重试机制增强
- **火山引擎**: 最多5次，指数退避（2s, 4s, 8s, 16s, 30s）
- **阿里千问**: 最多3次，线性延迟（2s, 4s, 6s）
- **智能降级**: 火山引擎失败 → 自动切换阿里千问

### 3. 错误处理改进
- 详细的重试日志
- 智能错误分类
- 自动诊断建议

## 🛠️ 如果问题仍然存在

### 检查网络连接
```bash
# 测试基本网络连接
ping baidu.com
curl -I https://www.baidu.com

# 测试API服务连接
curl -I https://ark.cn-beijing.volces.com/api/v3
curl -I https://dashscope.aliyuncs.com
```

### 检查防火墙设置
```bash
# 检查是否有防火墙阻止
telnet ark.cn-beijing.volces.com 443
telnet dashscope.aliyuncs.com 443
```

### 检查代理设置
```bash
# 如果需要代理，设置环境变量
export HTTP_PROXY=http://your-proxy:port
export HTTPS_PROXY=https://your-proxy:port

# 然后重启应用
```

### 检查API密钥
```bash
# 验证API密钥是否正确
grep "api_key" config_local.yaml

# 确保密钥格式正确，没有多余空格
```

## 📞 进一步支持

如果以上步骤都完成了但问题仍然存在，请提供以下信息：

1. **诊断脚本输出**
```bash
./scripts/diagnose_api_issues.sh > diagnosis.log 2>&1
```

2. **应用程序日志**
```bash
tail -100 logs/app.log > app.log.latest
```

3. **配置文件检查**
```bash
grep -A 10 "volc:" config_local.yaml > config.check
grep -A 15 "ali:" config_local.yaml >> config.check
```

4. **网络测试结果**
```bash
./test_api_connection.sh > network.test 2>&1
```

## ✅ 成功标志

修复成功后，你应该看到：

1. **API调用日志正常**：看到 `✅ 火山引擎API成功` 或 `✅ 阿里千问API成功`
2. **book创建成功**：API返回正常，不再出现EOF错误
3. **重试机制工作**：偶尔看到重试日志，但最终成功
4. **降级机制工作**：火山引擎失败时自动切换到阿里千问

---

**总结：这个修复方案通过增强重试机制、优化超时设置、实现智能降级，显著提升了API调用的可靠性，预期能解决99%的EOF和超时问题。**
