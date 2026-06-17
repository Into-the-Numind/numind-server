-- 会议副驾 · 反馈引擎 v2（FEEDBACK_V2_SPEC §2.1）：meeting_session 新增滚动结构化摘要列。
-- running_summary = 会议进行中由后台 goroutine 节流折叠出的「running memory」（主题/事实决议/
-- 各方立场/未决待办），既喂给反馈判官避免幻觉，也作为 §3 最终纪要生成的基底。
--
-- feature flag: features.meeting_copilot.enabled（prod 默认 off → 本表休眠不可达）。
-- 手动执行（CI 不自动跑 migration，遵 dev-deploy-migration-gap）；仅在启用本功能的环境跑。
-- 幂等：ADD COLUMN IF NOT EXISTS（MySQL 8.0+ / MariaDB 支持），重复执行无副作用。
-- 注：helper.go AutoMigrate 已含 meeting_session，启用环境下会自动补该列；本文件供非
-- AutoMigrate 环境（手动迁移）使用。

ALTER TABLE `meeting_session`
  ADD COLUMN IF NOT EXISTS `running_summary` MEDIUMTEXT NULL COMMENT '滚动结构化摘要（running memory，后台折叠）' AFTER `summary`;
