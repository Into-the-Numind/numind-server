# Prod deploy 异常 + 手动恢复

**Date:** 2026-05-18

**Feature:** `credits-trial-grant-bypass-fix`

**Migrated from:** `build-manifest.yaml` decisions[]

---

CI deploy_product 阶段 docker.io 直连 + Tencent mirror DNS + 1ms.run mirror 均超时；set -e 让脚本在 pull 失败后退出，老 v2.1.21 容器未被 stop（无 outage 风险）。手动恢复：SSH prod 用 dockerproxy.net mirror 拉到镜像 retag 后 stop+rm 老容器并启新 v2.1.22 容器，healthz 185s 内 green。后端实际中断约 3 分钟（container swap 期）。GitHub Actions UI 上 deploy_product job 仍显示 failure。
