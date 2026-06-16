package document

import (
	"time"

	"numind-server/internal/pkg/model"
)

// maxContentBytes 是 content_md 的大小上限（2MB）。
const maxContentBytes = 2 * 1024 * 1024

// OpenReq 是「打开/懒建档」请求体。
type OpenReq struct {
	SourceURL string  `json:"source_url" binding:"required"`
	Filename  string  `json:"filename" binding:"required"`
	Mime      string  `json:"mime"`
	RunID     *uint64 `json:"run_id"`
}

// SaveReq 是「保存」请求体。
type SaveReq struct {
	ContentMD string `json:"content_md"`
	Title     string `json:"title"`
}

// DocumentDTO 是文档对外返回结构。
type DocumentDTO struct {
	ID              uint64    `json:"id"`
	Title           string    `json:"title"`
	ContentMD       string    `json:"content_md"`
	SourceMime      string    `json:"source_mime,omitempty"`
	SourceObjectKey string    `json:"source_object_key"`
	ParseMethod     string    `json:"parse_method"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// toDTO 把 model.Document 转为对外 DTO。
func toDTO(d *model.Document) *DocumentDTO {
	return &DocumentDTO{
		ID:              d.ID,
		Title:           d.Title,
		ContentMD:       d.ContentMD,
		SourceMime:      d.SourceMime,
		SourceObjectKey: d.SourceObjectKey,
		ParseMethod:     d.ParseMethod,
		CreatedAt:       d.CreatedAt,
		UpdatedAt:       d.UpdatedAt,
	}
}
