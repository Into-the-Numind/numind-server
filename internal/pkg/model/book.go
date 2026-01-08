package model

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

// BookM 卡册表
type BookM struct {
	gorm.Model
	UserID        uint       `gorm:"index;not null" json:"user_id"`                  // 创建用户ID
	Title         string     `gorm:"size:255;index:idx_title_tags" json:"title"`     // 卡册标题（可为空）
	OriginalText  string     `gorm:"type:text" json:"original_text"`                 // 用户原始输入文字（包含OCR结果）
	ProcessedText string     `gorm:"type:text" json:"processed_text"`                // AI处理后的markdown
	CategoryID    *uint      `gorm:"index" json:"category_id"`                       // 分类ID（可为空）
	CategoryName  string     `gorm:"size:100" json:"category_name"`                  // 分类名称（兼容旧字段）
	Tags          string     `gorm:"size:255;index:idx_title_tags" json:"tags"`      // 标签，逗号分隔
	Keywords      []string   `gorm:"type:json" json:"keywords"`                      // 自动生成的关键词，JSON数组
	KeywordsText  string     `gorm:"size:500;index:idx_keywords_text" json:"-"`      // 关键词的文本表示（用于索引）
	CardCount     int        `gorm:"default:0" json:"card_count"`                    // 卡片数量
	ViewTime      *time.Time `gorm:"type:datetime(3)" json:"view_time"`              // 查看时间
	ImageUrl      string     `gorm:"size:255" json:"image_url"`                      // 封面图片URL
	Status        string     `gorm:"size:20;default:'creating';index" json:"status"` // 创建状态：creating, success, failed
	AIPolish      int        `gorm:"default:0" json:"ai_polish"`                     // AI润色设置 0=关闭 1=开启
	BookType      string     `gorm:"size:20;default:'text';index" json:"book_type"`  // 笔记类型：text, text_with_image, todo, done

	// 关联关系
	//User     User       `gorm:"foreignKey:UserID" json:"user_id"`                       // 创建用户ID
	Category *CategoryM `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
	Images   []ImageM   `gorm:"foreignKey:BookID" json:"images,omitempty"` // 关联的图片
	Cards    []CardM    `gorm:"foreignKey:BookID" json:"cards,omitempty"`  // 关联的卡片
}

func (BookM) TableName() string {
	return "book"
}

// GetCategoryName 获取分类名称
func (b *BookM) GetCategoryName() string {
	if b.Category != nil {
		return b.Category.Name
	}
	return b.CategoryName
}

// GetTitle 获取书籍标题（实现BookMatcher接口）
func (b *BookM) GetTitle() string {
	return b.Title
}

// GetTags 获取书籍标签（实现BookMatcher接口）
func (b *BookM) GetTags() string {
	return b.Tags
}

// GetKeywords 获取书籍关键词（实现BookMatcher接口）
func (b *BookM) GetKeywords() []string {
	if b.Keywords == nil {
		return []string{}
	}

	// 同步更新KeywordsText字段用于索引
	b.updateKeywordsText()

	return b.Keywords
}

// updateKeywordsText 更新关键词文本字段（用于索引）
func (b *BookM) updateKeywordsText() {
	if len(b.Keywords) > 0 {
		// 将关键词数组转换为逗号分隔的文本
		keywordsText := strings.Join(b.Keywords, ",")
		// 限制长度以避免超出数据库字段限制（500字符）
		if len(keywordsText) > 500 {
			keywordsText = keywordsText[:500]
		}
		b.KeywordsText = keywordsText
	} else {
		b.KeywordsText = ""
	}
}

// SetKeywords 设置关键词并同步更新文本字段
func (b *BookM) SetKeywords(keywords []string) {
	b.Keywords = keywords
	b.updateKeywordsText()
}

// GetID 获取书籍ID（实现BookMatcher接口）
func (b *BookM) GetID() uint {
	return b.ID
}

// BookStatus 定义book状态常量
const (
	BookStatusCreating = "creating" // 创建中
	BookStatusAI       = "ai"       // 等待AI处理
	BookStatusRender   = "render"   // 正在渲染
	BookStatusSuccess  = "success"  // 创建成功
	BookStatusFailed   = "failed"   // 创建失败
)

// BookType 定义笔记类型常量
const (
	BookTypeText          = "text"            // 只带文字
	BookTypeTextWithImage = "text_with_image" // 带图带文字
	BookTypeTodo          = "todo"            // to do（未完成）
	BookTypeDone          = "done"            // to do（已完成）
)
