# AI Service Manager — prod 配置同步

## 前置条件

- 当前在 develop 分支，所有 feature/ai-service-manager 改动已 merge
- prod 服务器有 SSH 访问权（`PROD_SSH_HOST` / `PROD_SSH_USER` / `PROD_SSH_PASS`）

## 步骤

1. 编辑 config_prod.yaml 新增 `ai_providers:` 段（参考 config_dev.yaml 结构）
2. 填入 prod 凭据（`api_key` 等），确保所有 provider 的密钥正确
3. 部署 — 参考 `docs/deployment/ai-service-manager-migration-runbook.md` 的 prod 部署流程
4. 部署后验证 `/healthz/ai` 返回 `status: ok`
5. 登录管理端确认 AI 服务列表正确（`/v1/admin/ai-services` 返回已注册服务）
