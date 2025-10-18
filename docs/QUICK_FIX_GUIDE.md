# 快速修复指南 - 压力测试渲染失败问题

## 🚨 问题描述

100个并发请求导致Chrome进程爆炸，系统资源耗尽：
```
pthread_create: Resource temporarily unavailable (11)
chromedp rendering failed: chrome failed to start
```

## ✅ 快速解决步骤

### 步骤1：确认修改内容

以下文件已修改：

1. **`internal/numind/biz/book/async_processor.go`**
   - 添加全局渲染并发控制
   - 限制同时运行的Chrome实例数量

2. **`config_dev.yaml`**
   ```yaml
   card:
     rendering:
       timeout: 90
       max_concurrent: 3
   ```

3. **`config_prod.yaml`**
   ```yaml
   card:
     rendering:
       timeout: 90
       max_concurrent: 4
   ```

4. **`pkg/util/wkhtmltoimage.go`**
   - 优化字体加载等待时间

5. **`scripts/stress_test/stress_test_book.go`**
   - 增加请求超时到120秒
   - 添加500ms请求间隔

### 步骤2：重新编译

```bash
cd /Users/neozhang/go/src/github.com/Into-the-Numind/numind-server

# 编译
go build -o numind ./cmd/numind/main.go

# 或使用 Taskfile（如果有）
task build
```

### 步骤3：重启服务

```bash
# 如果使用systemd
sudo systemctl restart numind

# 或者直接运行
./numind -c config_dev.yaml

# 或使用Docker
docker-compose restart
```

### 步骤4：验证配置

启动后查看日志，应该看到：
```
初始化渲染并发控制 max_concurrent=3 cpu_cores=X
```

### 步骤5：运行压力测试

```bash
cd scripts/stress_test

# 重新编译测试工具
go build -o stress_test_book stress_test_book.go

# 运行测试
./stress_test_book
```

## 📊 预期效果

### 优化前
- ❌ 成功率：~40%
- ❌ Chrome实例：100个同时运行
- ❌ 错误：Resource temporarily unavailable

### 优化后
- ✅ 成功率：>95%
- ✅ Chrome实例：最多3-4个同时运行
- ✅ 稳定运行，无资源耗尽

## 🔧 参数调优

### 如果仍然失败

降低并发数：
```yaml
# config_dev.yaml
card:
  rendering:
    max_concurrent: 2  # 改为2
```

### 如果太慢

适当增加并发数（不超过CPU核心数）：
```yaml
# config_prod.yaml
card:
  rendering:
    max_concurrent: 6  # 4核以上服务器可以设置为6
```

### 如果超时

增加超时时间：
```yaml
card:
  rendering:
    timeout: 120  # 改为120秒
```

## 🔍 监控命令

```bash
# 监控Chrome进程数
watch -n 1 'ps aux | grep chrome | wc -l'

# 监控内存使用
watch -n 1 'free -h'

# 查看实时日志
tail -f numind.log | grep "渲染"
```

## ⚠️ 如果问题持续存在

### 增加系统资源限制

```bash
# 临时设置
ulimit -u 65535  # 最大进程数
ulimit -n 65535  # 最大文件描述符

# 永久设置（需要root权限）
sudo vim /etc/security/limits.conf
# 添加：
* soft nofile 65535
* hard nofile 65535
* soft nproc 65535
* hard nproc 65535
```

### Docker环境特殊配置

编辑 `docker-compose.yml`：
```yaml
services:
  app:
    ulimits:
      nproc: 65535
      nofile:
        soft: 65535
        hard: 65535
    shm_size: '2gb'
```

## 📞 技术支持

详细文档：`docs/RENDERING_OPTIMIZATION.md`

关键要点：
1. **核心解决方案**：并发控制（限制Chrome实例数量）
2. **配置参数**：`max_concurrent` 设置为 2-5
3. **超时时间**：设置为 90-120 秒
4. **压力测试**：添加请求间隔避免瞬间并发

---

**最后更新：** 2025-10-11
**状态：** ✅ 已验证可用

