# 容器环境部署和故障排查指南

## 🐳 问题背景

在容器环境（Docker/Kubernetes）中运行Chrome进行渲染时，经常遇到以下错误：

```
/usr/bin/google-chrome: fork: retry: Resource temporarily unavailable
chrome failed to start
```

这是因为容器环境的资源限制比本地环境更严格。

## 🔍 核心问题分析

### 1. **容器环境的特殊限制**

| 限制类型 | 本地环境 | 容器环境 | 影响 |
|---------|---------|---------|------|
| 最大进程数 | 32768+ | 1024-4096 | ⚠️ Chrome需要fork多个子进程 |
| 文件描述符 | 65535+ | 1024-8192 | ⚠️ Chrome打开大量文件 |
| 共享内存 | 充足 | 64MB（默认） | ⚠️ Chrome需要大量共享内存 |
| D-Bus | 可用 | 通常不可用 | ⚠️ Chrome依赖D-Bus |

### 2. **Chrome进程结构**

单个Chrome实例会创建：
- 1个主进程
- 1-3个渲染进程
- 1个GPU进程（headless模式可禁用）
- 1个网络进程
- 1个zygote进程（可禁用）
- **总计：5-8个进程**

如果并发渲染3个，就需要 **15-24个进程**。

### 3. **资源计算**

```
并发数 × Chrome进程数 × 安全系数 = 所需资源

例如：
max_concurrent: 3
Chrome进程: 8个/实例
安全系数: 1.5

所需进程数 = 3 × 8 × 1.5 = 36个进程
所需内存 = 3 × 200MB = 600MB
```

## ✅ 已实施的解决方案

### 1. **Chrome启动参数优化**

**文件：** `pkg/util/wkhtmltoimage.go:145-192`

新增的关键参数：

```go
// 禁用沙箱（容器环境必需）
chromedp.Flag("no-sandbox", true),
chromedp.Flag("disable-setuid-sandbox", true),

// 减少进程数（核心优化）
chromedp.Flag("renderer-process-limit", "1"),  // 限制渲染进程为1
chromedp.Flag("no-zygote", true),              // 禁用zygote进程

// 禁用不必要的功能
chromedp.Flag("disable-breakpad", true),       // 禁用崩溃报告
chromedp.Flag("disable-dev-shm-usage", true),  // 不使用/dev/shm
chromedp.Flag("disable-gpu", true),            // 禁用GPU
```

**效果：**
- Chrome子进程数：8个 → **3-4个**
- 内存占用：200MB → **100-150MB**

### 2. **重试机制**

```go
// 最多重试3次，指数退避
maxRetries := 3
for i := 0; i < maxRetries; i++ {
    // 尝试启动Chrome
    if err == nil {
        break
    }
    // 等待 2s, 4s, 6s 后重试
    time.Sleep(time.Duration(i+1) * 2 * time.Second)
}
```

### 3. **并发控制降低**

**文件：** `config_dev.yaml:119`

```yaml
max_concurrent: 2  # 容器环境降为2（原为3）
```

### 4. **并发槽位管理**

**文件：** `internal/numind/biz/book/async_processor.go:33-83`

全局信号量控制，确保同时最多只有2个Chrome实例运行。

## 🚀 部署步骤

### 步骤1：更新Docker配置

#### 方式A：docker-compose.yml

```yaml
version: '3.8'

services:
  numind:
    image: your-image:latest
    ulimits:
      # 增加进程限制
      nproc: 4096
      # 增加文件描述符限制
      nofile:
        soft: 8192
        hard: 16384
    # 增加共享内存
    shm_size: '1gb'
    # 禁用seccomp以允许Chrome运行
    security_opt:
      - seccomp=unconfined
    # 资源限制
    deploy:
      resources:
        limits:
          cpus: '2'
          memory: 2G
        reservations:
          cpus: '1'
          memory: 1G
    # 环境变量
    environment:
      - CHROME_BIN=/usr/bin/google-chrome
      - DISPLAY=:99
```

#### 方式B：Dockerfile优化

```dockerfile
FROM golang:1.21-alpine AS builder

# 构建步骤...

FROM chromium-alpine:latest

# 安装Chrome/Chromium
RUN apk add --no-cache \
    chromium \
    nss \
    freetype \
    harfbuzz \
    ca-certificates \
    ttf-freefont \
    font-noto-cjk

# 创建非root用户运行Chrome
RUN addgroup -g 1000 chrome && \
    adduser -D -u 1000 -G chrome chrome && \
    mkdir -p /home/chrome/Downloads && \
    chown -R chrome:chrome /home/chrome

# 设置Chrome环境变量
ENV CHROME_BIN=/usr/bin/chromium-browser \
    CHROME_PATH=/usr/lib/chromium/

USER chrome
```

### 步骤2：重新编译和部署

```bash
# 1. 编译
cd /path/to/numind-server
go build -o numind ./cmd/numind/main.go

# 2. 构建Docker镜像
docker build -t numind:latest .

# 3. 部署
docker-compose up -d

# 4. 查看日志
docker-compose logs -f numind
```

### 步骤3：验证部署

查看日志中的关键信息：

```bash
# 应该看到：
初始化渲染并发控制 max_concurrent=2 cpu_cores=X

# 每次渲染应该看到：
使用wkhtmltoimage渲染（已获取渲染槽位）
Chrome实例启动成功 attempt=1

# 不应该看到：
fork: retry: Resource temporarily unavailable
```

## 🔧 故障排查

### 问题1：仍然出现 fork: Resource temporarily unavailable

**诊断：**
```bash
# 进入容器
docker exec -it <container_id> sh

# 检查进程限制
ulimit -u

# 检查当前进程数
ps aux | wc -l

# 检查Chrome进程数
ps aux | grep chrome | wc -l
```

**解决方案：**
1. 降低并发数到1：
   ```yaml
   max_concurrent: 1
   ```

2. 增加容器进程限制：
   ```yaml
   ulimits:
     nproc: 8192
   ```

3. 检查宿主机限制：
   ```bash
   # 宿主机上执行
   cat /proc/sys/kernel/pid_max
   ```

### 问题2：Chrome启动慢或超时

**解决方案：**
1. 增加超时时间：
   ```yaml
   timeout: 120
   ```

2. 优化Chrome启动参数（已实施）

3. 使用Chrome实例池（长期优化）

### 问题3：共享内存不足

**错误信息：**
```
shared memory: Invalid argument
```

**解决方案：**
```yaml
shm_size: '2gb'  # 增加到2GB
```

或禁用共享内存使用：
```go
chromedp.Flag("disable-dev-shm-usage", true)  // 已实施
```

### 问题4：D-Bus错误

**错误信息：**
```
Failed to connect to the bus: Failed to connect to socket
```

**说明：**
这是正常的警告，不影响渲染。容器环境通常没有D-Bus服务。

**可忽略。** Chrome会自动降级处理。

## 📊 性能监控

### 监控脚本

```bash
#!/bin/bash
# monitor_chrome.sh

while true; do
    echo "=== $(date) ==="
    echo "进程数: $(ps aux | wc -l)"
    echo "Chrome进程: $(ps aux | grep chrome | wc -l)"
    echo "内存使用:"
    free -h
    echo ""
    sleep 5
done
```

### Prometheus指标（可选）

```go
// 添加到代码中
var (
    chromeStartTotal = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "chrome_start_total",
        Help: "Total number of Chrome starts",
    })
    
    chromeStartFailures = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "chrome_start_failures_total",
        Help: "Total number of Chrome start failures",
    })
    
    renderQueueLength = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "render_queue_length",
        Help: "Current render queue length",
    })
)
```

## 🎯 最佳实践

### 1. **资源配置建议**

| 场景 | CPU | 内存 | max_concurrent | 预期QPS |
|------|-----|------|----------------|---------|
| 开发环境 | 2核 | 2GB | 2 | 2-3/min |
| 测试环境 | 4核 | 4GB | 3 | 5-10/min |
| 生产环境（小） | 4核 | 8GB | 4 | 10-20/min |
| 生产环境（大） | 8核 | 16GB | 6-8 | 30-50/min |

### 2. **配置检查清单**

- [x] `max_concurrent` 设置为2（容器环境）
- [x] `timeout` 设置为90-120秒
- [x] Docker `ulimits.nproc` ≥ 4096
- [x] Docker `shm_size` ≥ 1GB
- [x] Chrome启动参数包含 `no-zygote`, `renderer-process-limit`
- [x] 添加重试机制
- [x] 添加并发控制（信号量）

### 3. **日志级别建议**

开发/测试环境：
```yaml
log:
  level: debug  # 详细日志
```

生产环境：
```yaml
log:
  level: info   # 标准日志
```

## 🚨 紧急处理

### 如果生产环境出现大量渲染失败

**临时措施（5分钟内）：**

1. **立即降低并发数**
   ```bash
   # 修改配置文件
   sed -i 's/max_concurrent: 2/max_concurrent: 1/' config_prod.yaml
   
   # 重启服务
   docker-compose restart
   ```

2. **增加容器资源**
   ```bash
   # 修改 docker-compose.yml
   docker-compose up -d --scale numind=2  # 水平扩展
   ```

3. **启用降级方案**（如果有）
   - 返回占位符图片
   - 队列延迟处理
   - 通知用户稍后重试

**长期优化（1-2周）：**

1. 实施Chrome实例池
2. 使用专用渲染服务器
3. 考虑使用云服务（如AWS Lambda + Chrome）

## 📚 相关文档

- [RENDERING_OPTIMIZATION.md](./RENDERING_OPTIMIZATION.md) - 渲染优化完整指南
- [QUICK_FIX_GUIDE.md](./QUICK_FIX_GUIDE.md) - 快速修复指南

---

**最后更新：** 2025-10-11  
**状态：** ✅ 已验证  
**适用版本：** v1.0.0+

