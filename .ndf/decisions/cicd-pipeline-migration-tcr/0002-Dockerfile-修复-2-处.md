# Dockerfile 修复 2 处

**Date:** 2026-05-18

**Feature:** `cicd-pipeline-migration-tcr`

**Migrated from:** `build-manifest.yaml` decisions[]

---

GOPROXY=https://goproxy.cn,direct（之前 proxy.golang.org 超时）+ 第二个 pip install 加 `--index-url https://pypi.tuna.tsinghua.edu.cn/simple`（之前 PyPI 25 KB/s）。结果：numind-server 首次构建 38min→8min，后续增量 < 90 秒。
