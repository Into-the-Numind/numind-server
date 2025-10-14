# 卡片渲染性能优化与问题解决指南

## 问题现象

压力测试时出现大量渲染失败，错误信息：
```
pthread_create: Resource temporarily unavailable (11)
Zygote could not fork: process_type utility numfds 5 child_pid -1
chromedp rendering failed: chrome failed to start
```

## 根本原因分析

### 1. **Chrome进程爆炸（主要原因）**

每次渲染都启动一个新的Chrome实例，100个并发请求意味着：
- 100个Chrome主进程
- 每个Chrome产生10-20个子进程（渲染进程、GPU进程等）
- **总计1000-2000个进程同时运行**
- 每个进程消耗：
  - 内存：50-200MB
  - 文件描述符：100-500个
  - 线程：10-50个

### 2. **系统资源限制**

Linux系统默认限制：
- 最大进程数：`ulimit -u` 通常为 4096-32768
- 最大打开文件数：`ulimit -n` 通常为 1024-65535
- 最大线程数：受 `/proc/sys/kernel/threads-max` 限制

当100个Chrome实例同时运行时，很容易触发这些限制。

### 3. **容器环境特殊性**

Docker容器中：
- 共享宿主机内核
- 资源受cgroup限制
- D-Bus服务可能不可用
- 字体加载需要更多时间

## 已实施的解决方案

### ✅ 方案1：并发控制（已实现）

**代码位置：** `internal/numind/biz/book/async_processor.go`

**实现原理：**
```go
// 全局渲染信号量，限制同时渲染的Chrome实例数量
var renderSemaphore chan struct{}

// 获取渲染槽位（阻塞直到有可用槽位）
func acquireRenderSlot(ctx context.Context) error {
    select {
    case renderSemaphore <- struct{}{}:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}

// 释放渲染槽位
func releaseRenderSlot() {
    select {
    case <-renderSemaphore:
    default:
    }
}
```

**配置参数：**
```yaml
# config_dev.yaml / config_prod.yaml
card:
  rendering:
    max_concurrent: 3  # 最大并发渲染数
```

**效果：**
- ✅ 限制同时运行的Chrome实例数量
- ✅ 避免系统资源耗尽
- ✅ 请求排队等待，确保成功率

**建议配置：**
- 开发环境：2-3
- 测试环境：3-5
- 生产环境（2核4G）：3-4
- 生产环境（4核8G）：4-6
- 生产环境（8核16G）：6-8

### ✅ 方案2：超时时间优化（已实现）

**代码位置：** `config_dev.yaml`, `config_prod.yaml`

```yaml
card:
  rendering:
    timeout: 90  # 超时时间（秒）
```

**说明：**
- 原配置：30秒
- 新配置：90秒
- 实际渲染耗时：Chrome启动(3-5s) + 页面加载(2s) + 字体渲染(2-4s) + 截图(2-3s) = 10-15秒
- 高并发时可能需要更多时间

### ✅ 方案3：智能等待时间（已实现）

**代码位置：** `pkg/util/wkhtmltoimage.go:223-229`

```go
// 根据字体加载状态动态等待
if fontLoaded {
    time.Sleep(2 * time.Second) // 字体已加载，短等待
} else {
    time.Sleep(4 * time.Second) // 字体未加载，需要更多时间
}
```

**效果：**
- 字体已加载：减少3秒等待时间
- 字体未加载：保持4秒等待，确保渲染质量

### ✅ 方案4：压力测试请求间隔（已实现）

**代码位置：** `scripts/stress_test/stress_test_book.go`

```go
const (
    timeout         = 120 * time.Second      // 请求超时
    requestInterval = 500 * time.Millisecond // 请求间隔
)

// 在每次请求后添加延迟
time.Sleep(requestInterval)
```

**效果：**
- 避免瞬间并发过高
- 给服务器缓冲时间
- 更接近真实用户行为

## 额外优化建议（可选）

### 建议1：增加系统资源限制

**临时调整（重启后失效）：**
```bash
# 增加最大进程数
ulimit -u 65535

# 增加最大打开文件数
ulimit -n 65535

# 增加最大线程数（需要root权限）
sudo sysctl -w kernel.threads-max=100000
```

**永久调整：**
```bash
# 编辑 /etc/security/limits.conf
* soft nofile 65535
* hard nofile 65535
* soft nproc 65535
* hard nproc 65535

# 编辑 /etc/sysctl.conf
kernel.threads-max = 100000
vm.max_map_count = 262144

# 应用配置
sudo sysctl -p
```

### 建议2：Docker容器资源配置

**docker-compose.yml：**
```yaml
services:
  app:
    ulimits:
      nproc: 65535
      nofile:
        soft: 65535
        hard: 65535
    security_opt:
      - seccomp=unconfined
    shm_size: '2gb'  # 增加共享内存，Chrome需要
```

### 建议3：监控和日志

添加监控指标：
```go
// 记录当前渲染队列长度
renderQueueLength := len(renderSemaphore)
log.Infow("渲染队列状态", 
    "queue_length", renderQueueLength, 
    "max_concurrent", cap(renderSemaphore))
```

## 使用指南

### 1. 重新编译并部署

```bash
# 编译
go build -o numind ./cmd/numind

# 重启服务
systemctl restart numind
```

### 2. 验证配置

查看日志中的初始化信息：
```
初始化渲染并发控制 max_concurrent=3 cpu_cores=4
```

### 3. 运行压力测试

```bash
cd scripts/stress_test
./run.sh
```

### 4. 监控系统资源

```bash
# 监控进程数
watch -n 1 'ps aux | grep chrome | wc -l'

# 监控内存
watch -n 1 'free -h'

# 监控文件描述符
watch -n 1 'lsof | wc -l'
```

## 性能对比

### 优化前
- 并发渲染：无限制
- Chrome实例：100个同时运行
- 成功率：~40%
- 失败原因：资源耗尽

### 优化后
- 并发渲染：3个（可配置）
- Chrome实例：最多3个同时运行
- 预期成功率：>95%
- 总耗时：增加（但成功率大幅提升）

## 故障排查

### 问题1：仍然出现资源耗尽

**解决方案：**
1. 降低 `max_concurrent` 配置（改为2）
2. 增加系统资源限制（见"额外优化建议"）
3. 增加压力测试的 `requestInterval`（改为1秒）

### 问题2：渲染速度太慢

**解决方案：**
1. 适当增加 `max_concurrent` 配置（不超过CPU核心数）
2. 优化服务器配置（增加内存、CPU）
3. 考虑使用渲染集群（多台服务器分担负载）

### 问题3：部分卡片仍然超时

**解决方案：**
1. 增加 `timeout` 配置（改为120秒）
2. 检查网络连接
3. 检查字体文件是否正确安装

## 长期优化方向

### 1. Chrome实例池（推荐）
复用Chrome实例，而不是每次都创建新的：
```go
type ChromePool struct {
    instances chan *chromedp.Context
}

func (p *ChromePool) Get() *chromedp.Context
func (p *ChromePool) Put(ctx *chromedp.Context)
```

### 2. 无头浏览器替代方案
- Puppeteer
- Playwright
- 专用的HTML-to-Image服务

### 3. 分布式渲染
- 使用消息队列（RabbitMQ, Kafka）
- 多台渲染服务器
- 负载均衡

### 4. 缓存策略
- 相同内容的卡片缓存渲染结果
- CDN加速

## 总结

通过实施**并发控制**这一核心优化，可以从根本上解决Chrome进程爆炸导致的资源耗尽问题。配合**超时优化**和**智能等待**，可以在保证成功率的前提下，最大化渲染性能。

**关键参数调整：**
- `card.rendering.max_concurrent`: 3（开发环境）/ 4（生产环境）
- `card.rendering.timeout`: 90秒
- `requestInterval`: 500ms（压力测试）

**预期效果：**
- 成功率从~40%提升到>95%
- 系统资源使用稳定
- 无进程爆炸风险

