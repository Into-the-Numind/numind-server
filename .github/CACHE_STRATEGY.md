# GitHub Actions 缓存策略说明

## 缓存架构

### 分层缓存设计

```
Docker 构建分层：
├─ 层 1: 基础镜像 (ubuntu:22.04, golang:1.24) → 全局共享
├─ 层 2: 系统依赖 (apt-get install) → go.mod 变化时重建
├─ 层 3: Go 依赖 (go mod download) → go.mod 变化时重建  ← 缓存重点
├─ 层 4: 源码构建 (go build) → 每次代码变更重建
└─ 层 5: 最终镜像 → 每次导出到 cache-to
```

### 缓存键策略

| 缓存层级 | 缓存键 | 失效条件 | 命中率 |
|---------|--------|---------|--------|
| Go 依赖 | `${BRANCH}_${GO_MOD_HASH}` | go.mod 变化 | 80%+ |
| 回退缓存 | `${BRANCH}` | 新分支创建 | 60% |
| 主干缓存 | `master` | master 重建 | 40% |

## 使用方式

### 自动缓存（默认）

Push 代码时自动使用缓存：
```bash
git push origin develop
# CI 自动使用最近的缓存
```

### 手动刷新缓存

当遇到缓存相关问题时：

1. **GitHub 网页手动触发：**
   ```
   Actions → Numind CI/CD → Run workflow
   ☑️ 刷新 Docker 缓存
   ```

2. **API 触发：**
   ```bash
   curl -X POST \
     -H "Authorization: token $GITHUB_TOKEN" \
     -H "Accept: application/vnd.github.v3+json" \
     https://api.github.com/repos/Into-the-Numind/numind-server/actions/workflows/ci-cd.yaml/dispatches \
     -d '{"ref":"develop","inputs":{"refresh_cache":"true"}}'
   ```

### 完全跳过缓存

用于排查构建问题或确保纯净构建：

1. **GitHub 网页：**
   ```
   Actions → Numind CI/CD → Run workflow
   ☑️ 跳过缓存
   ```

2. **效果：**
   - 不读取任何缓存
   - 完整重新构建所有层
   - 构建时间增加 5-10 分钟

## 缓存配额管理

### 配额限制
- GitHub Actions Cache：10 GB/仓库
- 超出后自动删除最旧的缓存

### 自动清理
- 已配置每周日凌晨自动清理旧缓存
- 保留最近 3 个缓存版本

### 手动清理
```bash
# 清理所有分支缓存
Actions → Cleanup Caches → Run workflow

# 清理特定分支
Actions → Cleanup Caches → Run workflow
输入分支名: develop
```

## 故障排查

### 问题 1: `error writing layer blob: not_found`

**原因：** 缓存损坏或配额满

**解决：**
```bash
# 方法 1: 刷新缓存
Actions → Numind CI/CD → Run workflow → ☑️ 刷新 Docker 缓存

# 方法 2: 跳过缓存
Actions → Numind CI/CD → Run workflow → ☑️ 跳过缓存

# 方法 3: 清理缓存后重试
Actions → Cleanup Caches → Run workflow
# 然后重新推送代码
```

### 问题 2: 缓存未命中

**检查点：**
1. go.mod 是否修改？（正常行为）
2. 分支名是否变化？（正常行为）
3. 缓存是否被手动清理？

**诊断命令：**
```bash
# 查看当前缓存列表
gh cache list --repo Into-the-Numind/numind-server
```

### 问题 3: 构建时间未减少

**可能原因：**
1. 缓存未命中（检查 go.mod 是否频繁变更）
2. 使用 `mode=min` 只缓存最终层（有意为之，减少配额使用）
3. 网络问题导致下载缓存慢

**优化建议：**
- 尽量减少 go.mod 的频繁修改
- 合并多个小提交后一起 push
- 使用 `refresh_cache` 而非 `skip_cache` 排查问题

## 最佳实践

### 开发者建议

1. **批量修改依赖**
   ```bash
   # 不好：多次修改 go.mod
   go get package1 && git push
   go get package2 && git push
   
   # 好：一次性修改
   go get package1 package2 && git push
   ```

2. **依赖更新时机**
   - 避免在高峰期更新依赖（阻塞团队构建）
   - 周五下班前更新，让缓存周末生成

3. **监控构建时间**
   ```bash
   # 查看构建历史
   gh run list --workflow=ci-cd.yaml --limit=10
   ```

### 维护者建议

1. **定期检查缓存配额**
   ```bash
   gh cache list --repo Into-the-Numind/numind-server
   ```

2. **遇到构建问题优先尝试刷新缓存**
   - 比禁用缓存更快恢复
   - 不影响其他分支的缓存

3. **保持 Dockerfile 分层优化**
   - 依赖层和构建层分离
   - 参考 Dockerfile 中的注释

## 技术细节

### 缓存导出模式

```yaml
mode=min  # 只缓存最终镜像层（推荐，节省配额）
mode=max  # 缓存所有层（更快但占用配额多）
```

### 缓存作用域

```yaml
cache-from: |
  type=gha,scope=develop_abc12345    # 精确匹配（优先）
  type=gha,scope=develop             # 分支匹配（回退）
  type=gha,scope=master              # 主干匹配（最后尝试）
```

### 缓存失效策略

| 场景 | 行为 | 说明 |
|-----|------|------|
| go.mod 变化 | 重新下载依赖 | 预期行为 |
| 代码变化 | 重新构建 | 预期行为 |
| 手动刷新 | 新缓存键 | 强制重建 |
| 7 天无访问 | 自动删除 | GitHub 策略 |
| 配额满 | 删除最旧 | GitHub 策略 |

## 相关文件

- `.github/workflows/ci-cd.yaml` - 主构建流程
- `.github/workflows/cleanup-cache.yml` - 缓存清理工作流
- `Dockerfile` - 分层构建优化
