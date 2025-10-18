# 部署检查清单 - Chrome Fork问题修复

## ✅ 修改文件确认

- [x] `pkg/util/wkhtmltoimage.go` - Chrome参数优化 + 重试机制
- [x] `config_dev.yaml` - max_concurrent: 2
- [x] `config_prod.yaml` - max_concurrent: 4  
- [x] `config_local.yaml` - 同步配置
- [x] `internal/numind/biz/book/async_processor.go` - 并发控制（已有）

## 📋 部署前检查

### 1. 代码检查
```bash
- [ ] go build 编译成功
- [ ] 无 linter 错误
- [ ] 配置文件语法正确
```

### 2. 配置验证
```bash
# 检查dev配置
grep -A 2 "max_concurrent" config_dev.yaml
# 应显示：max_concurrent: 2

# 检查prod配置
grep -A 2 "max_concurrent" config_prod.yaml
# 应显示：max_concurrent: 4

# 检查超时配置
grep "timeout" config_dev.yaml
# 应显示：timeout: 90
```

### 3. Docker配置（如适用）
```yaml
- [ ] ulimits.nproc >= 4096
- [ ] ulimits.nofile >= 8192
- [ ] shm_size >= 1gb
- [ ] security_opt: seccomp=unconfined
```

## 🚀 部署步骤

### 开发环境（dev）

```bash
# 1. 进入项目目录
cd /Users/neozhang/go/src/github.com/Into-the-Numind/numind-server

# 2. 编译
go build -o numind ./cmd/numind/main.go

# 3. 构建Docker镜像（如果使用Docker）
docker build -t numind:dev-latest .

# 4. 重启服务
docker-compose restart numind
# 或
sudo systemctl restart numind

# 5. 验证启动
docker-compose logs -f numind | grep "初始化渲染并发控制"
# 应看到：初始化渲染并发控制 max_concurrent=2
```

### 生产环境（prod）

```bash
# 1. 备份当前版本
docker tag numind:prod numind:prod-backup-$(date +%Y%m%d)

# 2. 部署新版本
docker build -t numind:prod .

# 3. 滚动更新（推荐）
docker-compose up -d --no-deps --build numind

# 4. 验证
docker-compose logs -f numind | head -50

# 5. 监控5分钟，确认无错误
watch -n 5 'docker-compose logs --tail=20 numind | grep -i "error\|failed"'
```

## 🔍 部署后验证

### 1. 日志检查（前5分钟）

```bash
# 应该看到：
✅ 初始化渲染并发控制 max_concurrent=2
✅ Chrome实例启动成功 attempt=1
✅ wkhtmltoimage渲染成功

# 不应该看到：
❌ fork: retry: Resource temporarily unavailable
❌ chrome failed to start
❌ chrome启动失败，已重试3次
```

### 2. 功能测试

```bash
# 测试单个渲染（使用API或admin界面）
- [ ] 创建新book成功
- [ ] 卡片渲染成功
- [ ] 图片生成正常

# 压力测试（使用测试工具）
cd scripts/stress_test
./stress_test_book

- [ ] 成功率 >= 95%
- [ ] 无大量超时
- [ ] 无资源耗尽错误
```

### 3. 性能监控（前1小时）

```bash
# 监控Chrome进程数
watch -n 5 'docker exec <container> ps aux | grep chrome | wc -l'
# 期望：≤ 6个进程（max_concurrent=2, 每个Chrome 3个子进程）

# 监控内存使用
watch -n 5 'docker stats --no-stream <container>'
# 期望：≤ 1GB

# 监控渲染成功率
grep "Chrome实例启动成功" numind.log | wc -l
grep "chrome启动失败" numind.log | wc -l
# 期望：失败次数 < 5%
```

## 🚨 回滚方案

如果部署后出现问题：

### 快速回滚

```bash
# Docker方式
docker-compose down
docker tag numind:prod-backup-YYYYMMDD numind:prod
docker-compose up -d

# 或恢复配置文件
git checkout HEAD~1 config_dev.yaml pkg/util/wkhtmltoimage.go
go build -o numind ./cmd/numind/main.go
systemctl restart numind
```

### 临时降级

```bash
# 修改配置文件，降低并发
sed -i 's/max_concurrent: 2/max_concurrent: 1/' config_dev.yaml
docker-compose restart numind
```

## 📊 成功指标

| 指标 | 目标值 | 检查方式 |
|------|--------|---------|
| **渲染成功率** | ≥ 95% | 日志统计 |
| **Chrome启动成功** | ≥ 95% | 日志统计 |
| **平均响应时间** | ≤ 30s | 日志分析 |
| **同时Chrome进程数** | ≤ 6个 | ps aux |
| **容器内存使用** | ≤ 1GB | docker stats |
| **CPU使用率** | ≤ 80% | docker stats |

## 📝 部署记录

```
部署日期：__________
部署人员：__________
环境：[ ] dev  [ ] prod
部署前版本：__________
部署后版本：__________

验证结果：
- [ ] 日志检查通过
- [ ] 功能测试通过
- [ ] 性能监控正常
- [ ] 成功率 >= 95%

备注：
__________________________________________
__________________________________________
```

## 🔗 相关文档

1. [CHROME_FORK_ISSUE_SOLUTION.md](./CHROME_FORK_ISSUE_SOLUTION.md) - 问题解决方案
2. [CONTAINER_DEPLOYMENT_GUIDE.md](./CONTAINER_DEPLOYMENT_GUIDE.md) - 容器部署指南
3. [RENDERING_OPTIMIZATION.md](./RENDERING_OPTIMIZATION.md) - 渲染优化完整指南

---

**检查清单版本：** v1.0  
**最后更新：** 2025-10-11  
**适用版本：** numind-server v1.0.0+

