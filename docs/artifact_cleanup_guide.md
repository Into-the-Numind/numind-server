# GitHub Actions 构建产物清理指南

## 📋 概述

为了解决 GitHub Actions 存储配额不足的问题，我们实现了自动化的构建产物清理机制。每次 CI/CD 构建完成后，系统会自动清理旧的构建产物，确保存储空间得到有效管理。

## 🔧 自动化清理机制

### 1. 构建时自动清理

在每次 CI/CD 构建完成后，系统会自动执行以下清理操作：

- **numind-binaries**: 保留最新的 3 个产物，删除其余的
- **numind-binary**: 保留最新的 2 个产物，删除其余的
- **智能排序**: 按创建时间排序，保留最新的产物

### 2. 保留策略优化

- **retention-days**: 
  - `numind-binaries`: 5 天
  - `numind-binary`: 2 天
- **智能清理**: 基于数量的清理，而非时间

## 🚨 紧急清理工作流

### 手动触发清理

如果遇到存储配额紧急情况，可以使用 `Emergency Artifact Cleanup` 工作流：

1. 访问 GitHub 仓库的 `Actions` 标签页
2. 在左侧找到 `Emergency Artifact Cleanup` 工作流
3. 点击 `Run workflow` 按钮
4. 选择清理类型和参数

### 清理类型选项

| 类型 | 描述 | 建议使用场景 |
|------|------|-------------|
| `all` | 清理所有构建产物 | 存储空间严重不足时 |
| `binaries` | 只清理 numind-binaries | 多平台构建产物过多时 |
| `binary` | 只清理 numind-binary | 部署产物过多时 |
| `old` | 清理超过7天的旧产物 | 定期维护时 |

### 强制清理选项

- **false (默认)**: 智能清理，保留最新的产物
- **true**: 强制清理，删除所有产物（谨慎使用）

## 📊 清理效果监控

### 清理前状态显示

```
📊 清理前的状态:
总产物数量: 15

📋 各类型产物详情:
  numind-binaries: 8 个, 总大小: 24576000 bytes
  numind-binary: 7 个, 总大小: 8192000 bytes
```

### 清理后状态显示

```
📊 清理后的状态:
剩余产物数量: 5

📋 各类型产物统计:
  numind-binaries: 3 个
  numind-binary: 2 个
```

## 🛠️ 技术实现

### 1. GitHub CLI 集成

- 自动安装 GitHub CLI (`gh`)
- 使用 `GITHUB_TOKEN` 进行认证
- 通过 API 调用管理构建产物

### 2. 智能清理算法

```bash
# 按创建时间排序，保留最新的N个
gh api repos/$REPO/actions/artifacts --jq '.artifacts[] | select(.name == "numind-binaries") | {id: .id, created_at: .created_at}' | \
  jq -s 'sort_by(.created_at) | reverse | .[3:] | .[].id'
```

### 3. 错误处理

- 清理失败时继续执行其他清理操作
- 详细的日志输出，便于问题排查
- 优雅降级，GitHub CLI 不可用时跳过清理

## 📈 性能优化

### 1. 存储空间节省

- **清理前**: 通常占用 30-50MB 存储空间
- **清理后**: 通常占用 10-15MB 存储空间
- **节省比例**: 60-70%

### 2. 构建速度提升

- 减少产物上传时间
- 降低存储配额压力
- 提高 CI/CD 整体效率

## 🔍 故障排查

### 常见问题

#### 1. GitHub CLI 安装失败

```bash
# 手动安装
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | sudo dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null
sudo apt-get update && sudo apt-get install -y gh
```

#### 2. 认证失败

- 检查 `GITHUB_TOKEN` 权限
- 确保仓库有 Actions 权限
- 验证 token 是否过期

#### 3. 清理不生效

- 检查产物是否真的被删除
- 查看 GitHub Actions 日志
- 确认清理脚本执行成功

### 调试命令

```bash
# 检查产物状态
gh api repos/$REPO/actions/artifacts --jq '.artifacts | length'

# 查看特定产物详情
gh api repos/$REPO/actions/artifacts --jq '.artifacts[] | select(.name == "numind-binaries") | {id: .id, name: .name, created_at: .created_at, size: .size_in_bytes}'

# 手动删除特定产物
gh api repos/$REPO/actions/artifacts/ARTIFACT_ID -X DELETE
```

## 🚀 最佳实践

### 1. 定期监控

- 每周检查存储使用情况
- 监控清理效果
- 及时调整保留策略

### 2. 清理策略

- 生产环境：保留更多产物（3-5个）
- 开发环境：保留较少产物（2-3个）
- 测试环境：保留最少产物（1-2个）

### 3. 自动化维护

- 利用 GitHub Actions 的定时触发
- 设置存储配额告警
- 定期执行紧急清理

## 📚 相关资源

- [GitHub Actions 存储限制文档](https://docs.github.com/en/billing/managing-billing-for-github-actions/about-billing-for-github-actions#calculating-minute-and-storage-spending)
- [GitHub CLI 官方文档](https://cli.github.com/)
- [GitHub REST API 文档](https://docs.github.com/en/rest)

## ✅ 总结

通过实施自动化的构建产物清理机制，我们成功解决了 GitHub Actions 存储配额不足的问题：

1. **自动化**: 每次构建后自动清理旧产物
2. **智能化**: 基于数量和时间的智能清理策略
3. **灵活性**: 支持手动触发的紧急清理
4. **监控性**: 详细的清理状态和效果展示
5. **可靠性**: 完善的错误处理和降级机制

这套清理机制确保了 CI/CD 流程的稳定运行，同时最大化了存储空间的使用效率。
