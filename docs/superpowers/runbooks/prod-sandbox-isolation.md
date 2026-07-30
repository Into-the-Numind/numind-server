# Prod Sandbox 同机隔离发布 Runbook

本文是给 AI/工程执行者用的发布说明，不要求产品负责人手工 SSH。

## 一句话目标

把 Dev 已验证的 Sandbox 能力放到 Prod 同一台服务器上，但只给用户 API 一个受限
broker socket，不给用户 API 或 admin API 主机 Docker 权限，也不让 Sandbox 读取
Prod 用户数据、配置、证书、数据库或主 Docker socket。

## 上线前必须具备的证据

- Dev 同形态 broker 链路验收通过：SOP、聊天、Agent、文档导出、插件、飞书、5 并发。
- Prod 数据库备份已完成且可恢复性已验证。
- `scripts/cicd/calculate-sandbox-capacity.sh` 输出 ready，并生成内存/cgroup 数值。
- Sandbox image 使用固定 digest：
  `ccr.ccs.tencentyun.com/youshunumind/sandbox-skill@sha256:<64hex>`。
- server release image 内存在：
  `/app/numind-sandboxd`、`/app/numind-sandbox-reconcile`、
  `/app/sandbox-artifacts.sha256`。
- 产品负责人明确授权执行 Prod 写操作/部署。

## 发布顺序

1. 构建并推送 server release image。
2. 在 Prod 服务器上运行 `provision-sandbox-host.sh`：
   - 创建/校验 `numind-sandbox` 不可登录用户；
   - 创建/校验 `numind-sandbox-api` socket 组；
   - 创建/校验 8GiB Sandbox data-root；
   - 写入 systemd slice/drop-in；
   - 写入 sandboxd 配置；
   - 验证 cgroup、rootless 前置条件和 Prod 路径不可读。
3. 从 server release image 提取 sandboxd/reconcile，并核对 SHA256。
4. 停旧 sandboxd，让旧任务最多 300 秒排空。
5. 原子替换 sandboxd/reconcile binary。
6. 重启 sandboxd，并通过 Unix socket 检查 `/healthz` 和 `/readyz`。
7. broker ready 后，才部署 prod 用户 API，并只挂载 broker socket。
8. admin API 单独部署，强制 `NUMIND_SANDBOX_BACKEND=disabled`，不挂 broker/Docker。
9. 发布后执行产品 smoke：
   - 普通聊天；
   - 一个真实 SOP；
   - Agent `run_python`；
   - PPTX/DOCX/XLSX/PDF；
   - 飞书连接；
   - 5 个轻任务并发；
   - 第 6 个任务的排队/容量文案。

## 自动停止条件

以下任一失败，脚本必须停止，不能继续部署用户 API：

- Sandbox image 不是固定 digest；
- capacity/cgroup/data-root/rootless 前置条件不满足；
- broker socket 不是 `/run/numind-sandbox/sandboxd.sock` 或被误配成 Docker socket；
- sandboxd/reconcile checksum 不匹配；
- sandboxd 重启后 `/healthz` 或 `/readyz` 不通过；
- Prod 数据/配置/证书/主 Docker socket 对 `numind-sandbox` 可读。

## 回滚顺序

1. 用户 API 先切回 `NUMIND_SANDBOX_BACKEND=disabled` 或旧 API 镜像。
2. sandboxd 停止接新任务，并最多等待 300 秒排空。
3. 运行 `numind-sandbox-reconcile` dry-run/apply（按发布窗口决策）收口：
   - pending lease；
   - running sandbox session；
   - Agent run 状态；
   - Reserve/Reconcile 积分一致性。
4. 恢复旧 sandboxd/reconcile binary。
5. 重启 broker；如果 broker 仍不可用，保持 Sandbox disabled，核心 API 继续服务。
6. 不删除 journal/data-root；它们是审计和后续恢复证据。

## 清理规则

只允许清理 Docker 未引用对象，例如 `docker image prune -f`。禁止删除：

- 当前 release image；
- rollback image；
- `/opt/numind-sandbox/journal`；
- `/opt/numind-sandbox/data-root.img`；
- `/opt/numind-sandbox/data-root`；
- Prod 应用数据、配置、证书、MySQL/Redis 数据。

## 需要传入的运行时变量

用户 API broker 模式至少需要：

```text
NUMIND_SANDBOX_BACKEND=broker
NUMIND_SANDBOX_BROKER_SOCKET=/run/numind-sandbox/sandboxd.sock
NUMIND_SANDBOX_BROKER_OWNER_ID=numind-user-api-primary
NUMIND_SANDBOX_BROKER_INSTANCE=numind-prod-sandbox-primary
NUMIND_SANDBOX_API_HOST_UID=1001
NUMIND_SANDBOX_IMAGE_DIGEST=ccr.ccs.tencentyun.com/youshunumind/sandbox-skill@sha256:<64hex>
NUMIND_SANDBOX_SECCOMP_SHA256=<64hex>
```

以及 capacity 脚本输出的所有 `NUMIND_SANDBOX_*_BYTES` 值。

`provision-sandbox-host.sh` 会输出 `NUMIND_SANDBOX_BROKER_GID=<gid>`；后续用户 API
部署必须使用这个 GID 做 `--group-add`。
