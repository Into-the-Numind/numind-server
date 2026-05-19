# 磁盘满 + cleanup 函数

**Date:** 2026-05-19

**Feature:** `cicd-pipeline-migration-tcr`

**Migrated from:** `build-manifest.yaml` decisions[]

---

dev 部署机 40G 100% 满，docker pull 'no space left on device'。手动 docker system prune -a 清回 78%。然后加 cleanup_old_images() 函数：部署成功（或 rollback 成功）后自动 docker rmi 同 repo 所有非当前镜像，按 image ID 比对（不是 tag 名）保留 prod rollback 路径。
