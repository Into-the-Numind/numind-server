-- Rollback: 还原 user_model_preference.thinking 到 0
-- ⚠ 注意：本 rollback 会把 hotfix 后用户所有 thinking 偏好清零，
-- 只在"整个 hotfix-default-thinking-mode 特性需要回滚"时使用。

UPDATE user_model_preference SET thinking = 0 WHERE thinking = 1;
