package model

import (
	"context"
	"encoding/json"
	"time"

	"numind-server/internal/pkg/util"
)

// BookResponse 书籍响应结构体，包含书籍基本信息和分页后的卡片信息
type BookResponse struct {
	// 书籍基本信息
	ID            uint       `json:"id"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	UserID        uint       `json:"user_id"`
	Title         string     `json:"title"`
	OriginalText  string     `json:"original_text"`  // 用户原始输入文字
	ProcessedText string     `json:"processed_text"` // AI处理后的markdown
	CategoryID    *uint      `json:"category_id,omitempty"`
	CategoryName  string     `json:"category_name,omitempty"`
	Tags          string     `json:"tags"`
	CardCount     int        `json:"card_count"`
	ViewTime      *time.Time `json:"view_time,omitempty"`
	ImageUrl      string     `json:"image_url"`
	Status        string     `json:"status"`
	AIPolish      int        `json:"ai_polish"` // AI润色设置 0=关闭 1=开启
	BookType      string     `json:"book_type"` // 笔记类型：text, text_with_image, todo, done

	// 关联的图片信息
	Images []ImageResponse `json:"images,omitempty"`
	// 分页后的卡片信息
	Cards []CardResponse `json:"cards,omitempty"`
}

// ImageResponse 图片响应结构体
type ImageResponse struct {
	ID          uint       `json:"id"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
	UserID      uint       `json:"user_id"`
	BookID      *uint      `json:"book_id,omitempty"`
	OriginalURL string     `json:"original_url"`
	ThumbURL    string     `json:"thumb_url"`
	FileName    string     `json:"file_name"`
	FileSize    int64      `json:"file_size"`
	ImageType   string     `json:"image_type"`
	Width       int        `json:"width"`
	Height      int        `json:"height"`
	Status      string     `json:"status"`
}

// CardResponse 卡片响应结构体
type CardResponse struct {
	ID            uint        `json:"id"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	DeletedAt     *time.Time  `json:"deleted_at,omitempty"`
	UserID        uint        `json:"user_id"`
	BookID        uint        `json:"book_id"`
	ProcessedText interface{} `json:"process_text"`   // 解析后的分页数据，JSON字段名为process_text
	RenderedImage string      `json:"rendered_image"` // 渲染后的图片URL
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
		ID:            book.ID,
		CreatedAt:     book.CreatedAt,
		UpdatedAt:     book.UpdatedAt,
		DeletedAt:     deletedAt,
		UserID:        book.UserID,
		Title:         book.Title,
		OriginalText:  book.OriginalText,
		ProcessedText: book.ProcessedText,
		CategoryID:    book.CategoryID,
		CategoryName:  book.CategoryName,
		Tags:          book.Tags,
		CardCount:     book.CardCount,
		ViewTime:      book.ViewTime,
		Status:        book.Status,
		AIPolish:      book.AIPolish,
		BookType:      book.BookType,
		// 优先使用COS链接，如果获取失败则使用本地路径
		ImageUrl: util.GetBookImageWithCOS(context.Background(), book.ID, book.ImageUrl),
		Images:   []ImageResponse{},
		Cards:    []CardResponse{},
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
		SortOrder: card.SortOrder,
		Tags:      card.Tags,
		// 优先使用COS链接，如果获取失败则使用本地路径
		RenderedImage: util.GetCardImageWithCOS(context.Background(), card.ID, card.RenderedImage),
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

// AddImage 添加图片到响应中
func (br *BookResponse) AddImage(image *ImageM) {
	var deletedAt *time.Time
	if image.DeletedAt.Valid {
		deletedAt = &image.DeletedAt.Time
	}

	imageResp := ImageResponse{
		ID:          image.ID,
		CreatedAt:   image.CreatedAt,
		UpdatedAt:   image.UpdatedAt,
		DeletedAt:   deletedAt,
		UserID:      image.UserID,
		BookID:      image.BookID,
		OriginalURL: image.OriginalURL,
		ThumbURL:    image.ThumbURL,
		FileName:    image.FileName,
		FileSize:    image.FileSize,
		ImageType:   image.ImageType,
		Width:       image.Width,
		Height:      image.Height,
		Status:      image.Status,
	}

	br.Images = append(br.Images, imageResp)
}

// AddImages 批量添加图片到响应中
func (br *BookResponse) AddImages(images []*ImageM) {
	for _, image := range images {
		br.AddImage(image)
	}
}

// AddCards 批量添加卡片到响应中
func (br *BookResponse) AddCards(cards []*CardM) {
	for _, card := range cards {
		br.AddCard(card)
	}
}
