-- Migration: default 深度思考开启
-- Context: 前端 ModelSelector 的深度思考开关已隐藏，GetPreferences 默认返回
--          Thinking=false 导致前端 store 盖掉 ?? true 回退，用户 selectModel 时
--          实际保存 thinking=false。本迁移将存量 user_model_preference 全部
--          翻转为 thinking=1，与 hotfix-default-thinking-mode 后端默认值对齐。
-- Reversible: 配套 rollback 文件（但会丢失 hotfix 后用户的真实偏好，谨慎使用）。

UPDATE user_model_preference SET thinking = 1 WHERE thinking = 0;
