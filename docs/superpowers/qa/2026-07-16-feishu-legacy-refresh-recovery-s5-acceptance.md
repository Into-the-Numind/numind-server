# S5 验收记录 — feishu-legacy-refresh-recovery

## 验收日期

2026-07-16

## 范围

修复历史飞书授权卡片：当等待中的任务摘要仍指向已作废的旧授权会话时，重新生成链接不再返回 500。

## 验收结果

**ACCEPTED**

## 自动化验证

| 验证项 | 结果 |
|---|---|
| 历史 `superseded` 源会话可重新生成授权会话 | PASS |
| 正在运行的遗留授权会话（相同或不同权限范围）阻止重复生成 | PASS |
| 已过期的同权限范围遗留会话在同一事务中收尾，再生成新会话 | PASS |
| 任务摘要只在成功时重绑；拒绝路径无部分写入 | PASS |
| `go test ./internal/numind/store -count=1` | PASS |
| `go test ./internal/numind/biz/feishu -count=1` | PASS |
| `task test` | PASS |
| `task lint` | PASS |

本地测试环境为 macOS + SQLite；编译输出仅包含已知的 SQLite macOS 弃用警告。

## 审查

- 质量审查：PASS，无 P0/P1。
- 规格审查：PASS，无 P0/P1。

## 手工验证（S7）

合并并部署开发环境后，在原历史卡片点击“重新生成链接”。预期直接出现新的飞书授权链接或二维码，不再出现 `Internal server error.`；完成飞书页面操作后点击“我已完成，继续”。
