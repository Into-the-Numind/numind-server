# 部署影响分析

## ✅ 总结：低风险，不会影响现有功能

## 1. 修改影响范围

### 新增/修改的文件

| 文件 | 修改类型 | 影响范围 | 风险等级 |
|------|----------|----------|----------|
| `Dockerfile` | 新增依赖 | 镜像构建 | 🟢 低 |
| `scripts/docker-entrypoint.sh` | 新增 | 容器启动 | 🟢 低 |
| `scripts/check_deployment.sh` | 新增 | 部署检查 | 🟢 无 |
| `Go 代码` | 新增 | 文档切分 | 🟢 低 |

### 不会影响的部分

- ✅ 现有的 API 接口（完全兼容）
- ✅ 数据库结构（无变化）
- ✅ 配置文件格式（无变化）
- ✅ 前端代码（无变化）
- ✅ 其他业务逻辑（无变化）

## 2. 潜在风险及应对

### 风险 1：镜像构建时间增加

**影响**：构建时间从 8 分钟 → 12-15 分钟

**原因**：
- 安装 sentence-transformers 需要额外时间
- 下载模型（100MB）需要时间

**应对**：
- 模型下载有 3 次重试，失败不会中断构建
- 使用国内镜像源，下载速度有保障

### 风险 2：镜像体积增加

**影响**：镜像大小增加约 150MB

**组成**：
- Python 包：~50MB
- 模型文件：~100MB

**应对**：
- 仍在合理范围内
- 可以通过多阶段构建优化（后续可做）

### 风险 3：模型下载失败

**影响**：构建或启动时模型下载失败

**后果**：
- 构建时失败：不影响，构建继续
- 启动时失败：自动回退到规则切分

**应对**：
```
如果模型下载失败：
  1. 构建仍然成功
  2. 启动时再次尝试下载
  3. 运行时下载失败 → 使用规则切分
  4. 系统正常运行，只是缺少语义切分功能
```

### 风险 4：内存占用增加

**影响**：容器内存占用增加约 500MB

**原因**：
- 模型加载到内存

**应对**：
- 生产服务器通常内存充足
- 如果内存紧张，可以禁用语义切分

## 3. 回滚方案

如果部署出现问题，可以快速回滚：

### 方案 1：使用上一个镜像版本
```bash
# 查看历史镜像
docker images neozhang96/numind-server

# 使用上一个版本
docker stop numind-server-dev
docker rm numind-server-dev
docker run -d --name numind-server-dev ... neozhang96/numind-server:<上一个tag>
```

### 方案 2：禁用语义切分（代码层面）
修改 `internal/numind/biz/salesrag/service/splitter_adapter.go`：

```go
func NewSplitterAdapter() *SplitterAdapter {
    return &SplitterAdapter{
        // 强制使用规则切分
        hybrid: NewHybridSplitter(HybridSplitterConfig{
            Strategy: StrategyRuleOnly,  // 只使用规则
        }),
    }
}
```

然后重新部署。

### 方案 3：快速热修复（无需重新部署）
修改环境变量禁用模型检查：
```bash
# 进入运行中的容器
docker exec -it numind-server-dev bash

# 修改启动脚本，跳过模型检查
sed -i 's/check_and_download_model/# check_and_download_model/' /app/start.sh

# 重启
docker restart numind-server-dev
```

## 4. 测试建议

### 部署前测试

1. **本地构建测试**（如果有 Docker）：
```bash
docker build -t numind-test -f Dockerfile .
```

2. **Go 代码编译测试**：
```bash
cd /Users/zhiyuchen/Desktop/莫小派/Codes/numind-server
go build ./...
```

### 部署后测试

1. **健康检查**：
```bash
curl http://localhost:9091/healthz
```

2. **功能测试**：
- 上传一个 PDF/DOCX 文件
- 检查是否正常解析
- 检查切片数量和质量

3. **边界测试**：
- 上传小文件（<1000 字符）
- 上传大文件（>5000 字符）
- 检查是否正常处理

## 5. 监控指标

部署后关注以下指标：

| 指标 | 正常范围 | 异常处理 |
|------|----------|----------|
| 构建时间 | <20 分钟 | 检查网络 |
| 容器启动时间 | <30 秒 | 检查模型加载 |
| 内存占用 | +400-600MB | 考虑升级配置 |
| 文档解析时间 | 正常 | 对比之前 |
| API 响应时间 | 正常 | 检查日志 |

## 6. 建议部署策略

### 分阶段部署（推荐）

```
第 1 周：开发环境 (develop)
  └── 观察 3-5 天，无问题继续

第 2 周：测试环境 (release)
  └── 观察 3-5 天，无问题继续

第 3 周：生产环境 (v1.x.x)
  └── 选择低峰时段部署
```

### 蓝绿部署（如果服务器资源允许）

1. 启动新版本容器（blue）
2. 验证新版本正常
3. 切换流量到新版本
4. 保留旧版本（green）24 小时
5. 确认无误后删除旧版本

## 7. 结论

- **风险等级**：🟢 低风险
- **影响范围**：仅限文档切分功能
- **回滚难度**：🟢 简单（多条路径）
- **建议**：可以放心部署，建议先在开发环境验证
