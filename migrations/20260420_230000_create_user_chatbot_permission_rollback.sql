-- Rollback for migrations/20260420_230000_create_user_chatbot_permission.sql
--
-- WARNING: 执行 DROP TABLE 会丢失所有父账号已授权给子账号的 chatbot 白名单记录。
-- 仅在确认要彻底回滚 child-run-permission feature 时执行。
-- 同时需要 revert 代码中依赖 UserChatbotPermission 模型的逻辑，否则
-- ListVisibleChatbots / HasChatbotPermission 等查询将因表不存在而报错。

DROP TABLE IF EXISTS `user_chatbot_permission`;
