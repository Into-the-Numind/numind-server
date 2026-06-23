-- 飞书集成：用户第三方平台账号加密凭据表（feishu-integration feature）
-- feature flag: features.feishu_integration.enabled（prod 默认 off → 本表休眠不可达）
-- 手动执行（CI 不自动跑 migration，遵 dev-deploy-migration-gap）；仅在启用本功能的环境跑。
-- AutoMigrate 在 flag on 时也会建本表（helper.go 条件迁移）；本文件为权威 schema + 显式 ROW_FORMAT。
--
-- 安全约定（design.md §3 §4）：
--   app_secret / access_token / refresh_token 一律 AES-256-GCM 密文存 BLOB，绝不明文。
--   加解密在 biz/store 边界进行（internal/pkg/crypto），DB 只见密文。

CREATE TABLE IF NOT EXISTS `user_third_party_account` (
  `id`                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`           INT UNSIGNED    NOT NULL,                      -- 凭据归属用户
  `provider`          VARCHAR(32)     NOT NULL,                      -- 第三方平台标识, 首批 "lark"
  `app_id`            VARCHAR(64)     NOT NULL,                      -- 自建应用 app_id (非敏感, 明文)
  `app_secret_enc`    BLOB            NULL,                          -- app_secret 的 AES-256-GCM 密文
  `access_token_enc`  BLOB            NULL,                          -- user_access_token 的 AES-256-GCM 密文
  `refresh_token_enc` BLOB            NULL,                          -- refresh_token 的 AES-256-GCM 密文 (飞书提供时才有)
  `token_expires_at`  DATETIME(3)     NULL,                          -- access_token 过期时间, NULL=未知不主动刷新
  `scopes`            VARCHAR(512)    NULL,                          -- 一次性授权的全部 scope (空格分隔)
  `created_at`        DATETIME(3)     NOT NULL,
  `updated_at`        DATETIME(3)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_user_provider` (`user_id`, `provider`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;
-- ROW_FORMAT=DYNAMIC: 显式声明防旧实例默认 COMPACT (与 document 表同理)。
-- uniq_user_provider: 重复授权走 UPSERT 幂等更新, 而非新建行。
