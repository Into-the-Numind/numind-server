package model

import "time"

// UserChatbotPermission 子账号 chatbot 运行权限白名单（spec §3.1 / §3.3）。
//
// 设计决策：
//   - 对称于 UserTemplatePermission（SOP 模板权限），但**不嵌入 gorm.Model** —— 无 UpdatedAt / DeletedAt
//   - 语义上是**append-only 白名单**：授权 = INSERT，撤权 = 物理 DELETE，永不 UPDATE
//   - "0 记录 = deny-all"（default-deny），由 biz 层 HasChatbotPermission 判定
//   - ID 类型用 uint（对齐 gorm.Model.ID 约定，GORM 内部映射到 SQL BIGINT UNSIGNED）
type UserChatbotPermission struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	SubUserID uint      `gorm:"not null;uniqueIndex:uk_ucp_sub_chatbot" json:"sub_user_id"`
	ChatbotID uint      `gorm:"not null;uniqueIndex:uk_ucp_sub_chatbot;index:idx_ucp_chatbot" json:"chatbot_id"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName 返回底层 MySQL 表名。
func (UserChatbotPermission) TableName() string {
	return "user_chatbot_permission"
}
