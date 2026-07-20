# S6 Dev 部署验收

日期：2026-07-21
环境：Dev（生产环境未触碰）

## 合并与镜像

- `ndf-done` 已将后端 feature 合入并推送 `numind-server/develop`：`902b8d18`。
- `ndf-done` 已将前端 feature 合入并推送 `numind-web-v3/develop`：`85c5aaf`。
- 后端运行镜像：`develop-902b8d18`；registry digest `sha256:00e9a0eac0b01e3a30c9b1f0ad18386b267d59f8fca1e8b9f38763167d31c806`；runtime image id `sha256:df7a59c878ef22c49b300f71782759d7d29531ec307d90c147115c74683cceca`。
- 前端运行镜像：`develop-85c5aaf`；registry digest `sha256:a0b2b3dceb7df10a2e726bf31c6ae7fed4174c7992473832b4bbd0e58390f1fa`；runtime image id `sha256:7f05fbb88ff8fad0cc207ba7430e54e8fc81ee582e01bbee36a79d8636a373ea`。

## 线上验证

- 后端容器 health=healthy，公网 `:9091/healthz` 返回 status=ok。
- 数据库 schema migration 与迁移后字符集校验成功完成。
- 前端容器 health=healthy，公网 `:9200/health` 返回 healthy。
- 前端反向代理 `:9200/api/healthz` 返回后端 status=ok。
- 两个容器近十分钟日志无 panic、fatal 或 emergency。
- 当前后端镜像同时包含已先行部署的 `global-agent-full-tools`：所有现有/未来 Agent 默认拥有除 `document_generate` 外的现有工具，以及四个全局平台技能。

## 结论

Dev 部署与机器验收 ALL_PASS。上传链路现在只解析一次并缓存；模型初始上下文只接收附件引用，按需通过 `file_read(attachment_id)` 分页读取。生产环境未打 tag、未部署。
