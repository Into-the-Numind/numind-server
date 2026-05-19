# prod 502 nginx 修复

**Date:** 2026-05-19

**Feature:** `cicd-pipeline-migration-tcr`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户反馈 prod 部署期间 https://youshu.asia 偶发 502 + 10 秒内反复刷新都失败。根因：宝塔外层 nginx 的 proxy.conf 全局 `proxy_next_upstream error timeout invalid_header http_500 http_503 http_404` + 单 upstream `proxy_pass http://localhost:9202/`，容器重启的 1-2 秒内一次失败就标记 dead 10 秒，期间所有请求 502。修复：改用 upstream block + `max_fails=0`，10 秒锁死窗口 → 1-2 秒短抖动。备份 youshu.asia.conf.bak.20260519-135916-maxfails-fix，nginx -t + reload，零中断。
