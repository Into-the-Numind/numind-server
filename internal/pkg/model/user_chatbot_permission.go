package model

import "time"

// UserChatbotPermission 子账号 chatbot 运行权限白名单（spec §3.1 / §3.3）。
// 对称于 UserTemplatePermission（SOP 模板权限），但不使用软删除：
// DELETE 直接真删行，语义上"0 记录 = deny-all"（default-deny）。
type UserChatbotPermission struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	SubUserID uint      `gorm:"not null;uniqueIndex:uk_ucp_sub_chatbot" json:"sub_user_id"`
	ChatbotID uint      `gorm:"not null;uniqueIndex:uk_ucp_sub_chatbot;index:idx_ucp_chatbot" json:"chatbot_id"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName 返回底层 MySQL 表名。
func (UserChatbotPermission) TableName() string {
	return "user_chatbot_permission"
}
