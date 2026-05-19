# S1 office-hours grep 发现

**Date:** 2026-05-18

**Feature:** `sop-salesrag-parent-scope`

**Migrated from:** `build-manifest.yaml` decisions[]

---

user_feature_permission 表代码里有 3 个 feature_key：sales_agent / content_monitor / self_service_config。后两个 prod DB 0 行子账户授权但代码同样走 HasFeaturePermission 父账户 bypass。用户明确：content_monitor + self_service_config 是系统级平台功能（content_monitor 未来要废弃），与'租户拥有的资源'概念不同——本需求不动它们，父账户 bypass 保留。HasFeaturePermission 改造仅在 featureKey=='sales_agent' 分支生效。
