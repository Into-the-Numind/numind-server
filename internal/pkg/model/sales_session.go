package model

import (
	"time"

	"gorm.io/gorm"
)

// SalesSession 销售智能体对话会话表
type SalesSession struct {
	gorm.Model
	UserID       uint   `gorm:"index;not null" json:"user_id"`
	Title        string `gorm:"size:255" json:"title"`
	Status       string `gorm:"size:20;default:'active'" json:"status"` // active, closed
	MessageCount int    `gorm:"default:0" json:"message_count"`

	// 销售特有字段
	SalesStage      string `gorm:"size:20" json:"sales_stage"`         // 销售阶段: ""(未选择), 初次接触, 了解业务, 方案介绍, 成交推进, 售后服务
	DocumentIDs     string `gorm:"type:text" json:"document_ids"`      // JSON array: ["1","2","3"] (旧字段，向后兼容)
	ProductDocIDs   string `gorm:"type:text" json:"product_doc_ids"`   // 产品文档 JSON array
	CaseDocIDs      string `gorm:"type:text" json:"case_doc_ids"`      // 成功案例 JSON array
	FAQDocIDs       string `gorm:"type:text" json:"faq_doc_ids"`       // 百问百答 JSON array
	OpinionDocIDs   string `gorm:"type:text" json:"opinion_doc_ids"`   // 用户上传观点文档 JSON array
	OpinionTrackIDs string `gorm:"type:text" json:"opinion_track_ids"` // 系统赛道 ID JSON array (最多2个)
	DeepThinking    bool   `gorm:"default:false" json:"deep_thinking"`
	CustomerProfile string `gorm:"type:text" json:"customer_profile"` // Markdown 格式的分析结果
	LastQuery       string `gorm:"type:text" json:"last_query"`

	// 置顶功能字段
	IsPinned bool       `gorm:"default:false;index" json:"is_pinned"`
	PinnedAt *time.Time `gorm:"index" json:"pinned_at,omitempty"`

	// 关联关系
	User     User           `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Messages []SalesMessage `gorm:"foreignKey:SessionID" json:"messages,omitempty"`
}

func (SalesSession) TableName() string {
	return "sales_session"
}

// SalesMessage 销售智能体对话消息表
type SalesMessage struct {
	gorm.Model
	SessionID uint   `gorm:"index;not null" json:"session_id"`
	UserID    uint   `gorm:"index;not null" json:"user_id"`
	Role      string `gorm:"size:20;not null" json:"role"` // user, assistant, system
	Content   string `gorm:"type:text;not null" json:"content"`
	Status    string `gorm:"size:20;default:'sent'" json:"status"`

	// 销售特有字段（仅assistant角色有这些字段）
	Verdict  string `gorm:"type:mediumtext" json:"verdict,omitempty"` // 知识引用JSON，可能较大
	Thinking string `gorm:"type:text" json:"thinking,omitempty"` // 思维链内容
	Images   string `gorm:"type:text" json:"images,omitempty"`   // 图片链接列表，JSON 数组格式

	// 关联关系
	Session SalesSession `gorm:"foreignKey:SessionID" json:"session,omitempty"`
	User    User         `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (SalesMessage) TableName() string {
	return "sales_message"
}
