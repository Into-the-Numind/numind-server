package model

import "time"

// SalesAgentOwner 销售智能体父账户归属表 (owner tag)
//
// 每行表示一个父账户拥有"销售智能体卡片"的访问权。该表是销售智能体的
// owner tag 存储——与 chatbot_config.user_id 对销售智能体的等价概念。
//
// 极简设计 (spec D3): 不启用 GORM soft-delete、无 updated_at。
// 写入仅在 migration 或手工 SQL (无 admin UI); 撤销走 hard DELETE。
// FK 到 user(id) ON DELETE CASCADE 保证父账户被删时无残留。
type SalesAgentOwner struct {
	ParentUserID uint      `gorm:"primaryKey;type:int unsigned" json:"parent_user_id"`
	CreatedAt    time.Time `gorm:"type:datetime(3)" json:"created_at"`
}

// TableName 返回数据库表名
func (SalesAgentOwner) TableName() string {
	return "sales_agent_owner"
}
