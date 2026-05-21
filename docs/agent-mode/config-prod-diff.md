# config_prod.yaml Diff for #14 — Operator Reference

> ⚠️ 本文档**不是** auto-apply patch。User 上 prod 时根据本文档手动核对 prod config 文件。
> 本 feature 范围内 `config_prod.yaml` zero diff（autopilot 红线：AI 永不修改 config_prod.yaml）。

---

## 新增字段（如已配可跳过）

### Langfuse prod 凭据

v1.0-final 需要 Langfuse tracing 可用。请在 prod 服务器的 `config_prod.yaml` 确认已有：

```yaml
langfuse:
  enabled: true
  base_url: https://langfuse.youshu.asia   # 确认与实际 prod Langfuse 域名一致
  public_key: ${LANGFUSE_PUBLIC_KEY}       # via env，不要硬编码
  secret_key: ${LANGFUSE_SECRET_KEY}       # via env，不要硬编码
```

如果尚未配置，需同时在部署机环境变量（Docker Compose env / systemd env）中设置：

```bash
LANGFUSE_PUBLIC_KEY=pk-lf-...
LANGFUSE_SECRET_KEY=sk-lf-...
```

### aiservice task profile 路由（可选）

migration `20260521_180000_agent_task_profiles_seed.sql` 已在 DB 写入 7 条 task profile 行。
如 DB seed 正常，无需改 config_prod.yaml。

7 个新 task id 及默认路由：

| task_id | 默认 model | 说明 |
|---------|-----------|------|
| `agent.run` | qwen-turbo | 主对话 |
| `agent.sync_turn` | qwen-turbo | 同步轮次 |
| `agent.narration_fallback` | qwen-turbo | 旁白降级 |
| `agent.injection_check` | qwen-turbo | 注入检测 |
| `agent.permission_check` | qwen-turbo | 权限校验 |
| `agent.compact` | qwen-plus | Compact 摘要（需较强推理）|
| `agent.embed` | text-embedding-v4 | 向量化 |

如果 DB Registry 未包含上述 task_id，可通过 admin-web → AI 服务管理 → Task Profile 手动添加，无需改 config 文件。

### agent 模块启用开关（如有）

v1.0-final 未引入新的全局 feature flag（agent 模块跟随后端启动自动生效）。
如有灰度需要，可在 DB 层通过 `agent_definition.is_active` 控制单个 agent 的可见性，无需停服。

---

## 不变更字段（红线）

以下字段由早期 feature 锁定，**#14 不动，prod 上线后也不动**：

- `credit_transaction.source_type` CHECK constraint（`chk_ct_source_type`，#12 锁定，已包含 `admin_test`）
- `agent_run.state` + `state_reason` CHECK constraint（`chk_ar_state_reason`，#2 + #9 锁定，19 个 reason）
- config_prod.yaml 中任何支付相关配置（绝对禁止 AI 修改）

---

## Out-of-scope 配置项（v2 时处理）

以下配置 v1.0-final 未实现，运维可忽略：

| 项目 | 说明 | 预计 |
|------|------|------|
| Sandbox iptables allowlist | Docker bridge 无额外限制，v2 收紧 | v2 |
| L1 memory TTL cron | expires_at 写入但无清理 cron | v2 |
| Sandbox 网络出口白名单 | v1 不限 | v2 |

---

## 验证

部署完成后，可 diff 确认 config 文件未被意外修改：

```bash
# 在部署机上：
diff /backup/config_prod.yaml.pre-v2.2.0 /app/numind-server/config_prod.yaml
# 预期输出：无 diff（或仅包含上述 Langfuse 手动新增）
```
