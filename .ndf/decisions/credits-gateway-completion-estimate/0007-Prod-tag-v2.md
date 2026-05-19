# Prod tag v2.1.21 push + CI infra bug + 验证

**Date:** 2026-05-16

**Feature:** `credits-gateway-completion-estimate`

**Migrated from:** `build-manifest.yaml` decisions[]

---

git tag v2.1.21 → push origin v2.1.21 (16:09 CST)。CI deploy_product job 报 failure 但仅是 appleboy/ssh-action command_timeout 早于脚本 6-min healthcheck loop —— docker run 已成功执行 container 860c91f345fd at 16:09:21，docker inspect 显示 image=v2.1.21 + healthy + 真实用户请求处理中（healthz 持续 200 + SOP template list 调用 from user 326）。同 v2.1.18 一类 CI infra bug 不同模式（v2.1.18 是 Docker Hub pull GFW，本次是 SSH session timeout），都需独立 CI cleanup hotfix 修。Prod 验证 post-deploy reservation id=1667/1668 (deepseek-v3.2-thinking, 17:02-17:06): estimated_completion_tokens=2161 (= ceil(1800 × 1.2)) 精确等于 helper 预测值。
