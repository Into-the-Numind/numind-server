package model

import (
	"encoding/json"
	"time"
)

// BookResponse 书籍响应结构体，包含书籍基本信息和分页后的卡片信息
type BookResponse struct {
	// 书籍基本信息
	ID           uint       `json:"id"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
	UserID       uint       `json:"user_id"`
	Title        string     `json:"title"`
	CategoryID   *uint      `json:"category_id,omitempty"`
	CategoryName string     `json:"category_name,omitempty"`
	TemplateID   string     `json:"template_id"`
	Tags         string     `json:"tags"`
	CardCount    int        `json:"card_count"`
	ViewTime     *time.Time `json:"view_time,omitempty"`

	// 分页后的卡片信息
	Cards []CardResponse `json:"cards,omitempty"`
}

// CardResponse 卡片响应结构体
type CardResponse struct {
	ID            uint        `json:"id"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	DeletedAt     *time.Time  `json:"deleted_at,omitempty"`
	UserID        uint        `json:"user_id"`
	BookID        uint        `json:"book_id"`
	ImageID       uint        `json:"image_id"`
	ProcessedText interface{} `json:"processed_text"` // 解析后的分页数据
	SortOrder     int         `json:"sort_order"`
	Tags          string      `json:"tags"`
}

// NewBookResponse 从BookM创建BookResponse
func NewBookResponse(book *BookM) *BookResponse {
	var deletedAt *time.Time
	if book.DeletedAt.Valid {
		deletedAt = &book.DeletedAt.Time
	}

	return &BookResponse{
		ID:           book.ID,
		CreatedAt:    book.CreatedAt,
		UpdatedAt:    book.UpdatedAt,
		DeletedAt:    deletedAt,
		UserID:       book.UserID,
		Title:        book.Title,
		CategoryID:   book.CategoryID,
		CategoryName: book.CategoryName,
		TemplateID:   book.TemplateID,
		Tags:         book.Tags,
		CardCount:    book.CardCount,
		ViewTime:     book.ViewTime,
		Cards:        []CardResponse{},
	}
}

// AddCard 添加卡片到响应中
func (br *BookResponse) AddCard(card *CardM) {
	var deletedAt *time.Time
	if card.DeletedAt.Valid {
		deletedAt = &card.DeletedAt.Time
	}

	cardResp := CardResponse{
		ID:        card.ID,
		CreatedAt: card.CreatedAt,
		UpdatedAt: card.UpdatedAt,
		DeletedAt: deletedAt,
		UserID:    card.UserID,
		BookID:    card.BookID,
		//ImageID:   card.ImageID,
		SortOrder: card.SortOrder,
		Tags:      card.Tags,
	}

	// 解析ProcessedText字段中的JSON数据
	if card.ProcessedText != "" {
		var parsedData interface{}
		if err := json.Unmarshal([]byte(card.ProcessedText), &parsedData); err == nil {
			cardResp.ProcessedText = parsedData
		} else {
			// 如果解析失败，返回原始字符串
			cardResp.ProcessedText = card.ProcessedText
		}
	}

	br.Cards = append(br.Cards, cardResp)
}

// AddCards 批量添加卡片到响应中
func (br *BookResponse) AddCards(cards []*CardM) {
	for _, card := range cards {
		br.AddCard(card)
	}
}
