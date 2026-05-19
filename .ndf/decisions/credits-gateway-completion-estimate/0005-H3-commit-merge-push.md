# H3 commit + merge + push

**Date:** 2026-05-16

**Feature:** `credits-gateway-completion-estimate`

**Migrated from:** `build-manifest.yaml` decisions[]

---

commit 5299e56 在 fix/credits-gateway-completion-estimate，--no-ff merge 到 develop 得 3046919，push origin develop。CI run 25956011946 image_build success 但 deploy_dev 阶段 Docker Hub GFW 超时失败（同 v2.1.18 prod incident 模式）。手动恢复：SSH dev 用 docker.1ms.run mirror 拉 develop-3046919 镜像 retag 后从 CI ci-cd.yaml line 236-250 的 docker run 命令重新启动 numind-server-dev 容器。healthz green，container Up 在 develop-3046919。Dev outage 0min（CI failure 时老容器未被 stop，状态保留）。Deploy script GFW 失败兜底待 CI cleanup task 修复。
