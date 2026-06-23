package xhs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/datatypes"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// 列表查询的分页约束（api-design.md §4）。
const (
	defaultPage     = 1
	defaultPageSize = 20
	maxPageSize     = 100
)

// CommentItem 是 NoteItem 出参中的单条热门评论（与摄入侧 CommentPayload 对称）。
type CommentItem struct {
	Author     string `json:"author"`
	Text       string `json:"text"`
	LikeCount  int    `json:"like_count"`
	IPLocation string `json:"ip_location"`
}

// NoteItem 是选题库列表 / 详情的出参 DTO，包含全部展示字段 + enrich_status。
//
// 与 model.XhsTopicNote 解耦：不暴露 content_hash（内部去重用），并把 datatypes.JSON
// 的 tags/comments 解析为前端可直接渲染的 []string / []CommentItem，避免前端二次解析。
type NoteItem struct {
	ID        uint64 `json:"id"`
	XhsNoteID string `json:"xhs_note_id"`

	NoteType        string        `json:"note_type"`
	Title           string        `json:"title"`
	Content         string        `json:"content"`
	Tags            []string      `json:"tags"`
	CoverURL        string        `json:"cover_url"`
	NoteURL         string        `json:"note_url"`
	PublishedAt     *time.Time    `json:"published_at"`
	VideoURL        string        `json:"video_url"`
	VideoTranscript *string       `json:"video_transcript"`
	LikeCount       int           `json:"like_count"`
	CollectCount    int           `json:"collect_count"`
	CommentCount    int           `json:"comment_count"`
	ShareCount      int           `json:"share_count"`
	Comments        []CommentItem `json:"comments"`
	AuthorName      string        `json:"author_name"`
	AuthorLink      string        `json:"author_link"`
	AuthorFollowers int           `json:"author_followers"`

	// 6 个 LLM 分析字段。
	AITopicAngle     string `json:"ai_topic_angle"`
	AIViralReason    string `json:"ai_viral_reason"`
	AIBorrowable     string `json:"ai_borrowable"`
	AITargetAudience string `json:"ai_target_audience"`
	AITitleFormula   string `json:"ai_title_formula"`
	AIOneLine        string `json:"ai_one_line"`

	EnrichStatus string     `json:"enrich_status"`
	CollectedAt  *time.Time `json:"collected_at"`
	CrawledAt    time.Time  `json:"crawled_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ListFilter 是 ListNotes 的过滤/排序入参（从 controller query 透传）。
//
// 排序仅放行白名单字段（防 SQL 注入 / 任意列排序），未识别值在 store 层回退默认排序。
type ListFilter struct {
	NoteType     string // normal/video，空 = 不过滤
	Keyword      string // 标题/正文模糊匹配，空 = 不过滤
	EnrichStatus string // enrich_status 枚举，空 = 不过滤
	Sort         string // 预留排序参数（当前 store 固定按 crawled_at DESC）
}

// ListNotes 分页查询 userID 的私有选题库。
//
// page/pageSize 做边界归一化（page<1→1；pageSize<1→默认 20；pageSize>100→100），
// 计算 offset 后委托 store（store 已强制 WHERE user_id 隔离），把 model 行映射为 NoteItem。
// 返回 (列表, 总数, error)，total 供前端分页。
func (b *XhsBiz) ListNotes(ctx context.Context, userID uint, filter ListFilter, page, pageSize int) ([]NoteItem, int64, error) {
	page, pageSize = normalizePagination(page, pageSize)
	offset := (page - 1) * pageSize

	rows, total, err := b.store.ListNotes(ctx, userID, store.XhsNoteFilter{
		NoteType:     filter.NoteType,
		EnrichStatus: filter.EnrichStatus,
		Keyword:      filter.Keyword,
	}, offset, pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("ListNotes: %w", err)
	}

	items := make([]NoteItem, 0, len(rows))
	for i := range rows {
		items = append(items, toNoteItem(&rows[i]))
	}
	return items, total, nil
}

// GetNote 按 (userID, id) 获取单条笔记详情。不存在返回 store 的 errno.ErrXhsNoteNotFound
// （已带 user 隔离：他人笔记视为不存在，防越权读取）。
func (b *XhsBiz) GetNote(ctx context.Context, userID uint, id uint64) (*NoteItem, error) {
	row, err := b.store.GetNote(ctx, userID, id)
	if err != nil {
		// store 返回的 errno.ErrXhsNoteNotFound 直接透传给 controller（保留 HTTP 404 语义）。
		return nil, err
	}
	item := toNoteItem(row)
	return &item, nil
}

// DeleteNote 按 (userID, id) 删除单条笔记（store 带 user 隔离：删不到他人笔记）。
func (b *XhsBiz) DeleteNote(ctx context.Context, userID uint, id uint64) error {
	if err := b.store.DeleteNote(ctx, userID, id); err != nil {
		return fmt.Errorf("DeleteNote: %w", err)
	}
	return nil
}

// normalizePagination 归一化分页参数到合法区间。
func normalizePagination(page, pageSize int) (int, int) {
	if page < 1 {
		page = defaultPage
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

// toNoteItem 把 model.XhsTopicNote 映射为出参 NoteItem，并把 datatypes.JSON 解析为切片。
func toNoteItem(n *model.XhsTopicNote) NoteItem {
	return NoteItem{
		ID:               n.ID,
		XhsNoteID:        n.XhsNoteID,
		NoteType:         n.NoteType,
		Title:            n.Title,
		Content:          n.Content,
		Tags:             unmarshalTags(n.Tags),
		CoverURL:         n.CoverURL,
		NoteURL:          n.NoteURL,
		PublishedAt:      n.PublishedAt,
		VideoURL:         n.VideoURL,
		VideoTranscript:  n.VideoTranscript,
		LikeCount:        n.LikeCount,
		CollectCount:     n.CollectCount,
		CommentCount:     n.CommentCount,
		ShareCount:       n.ShareCount,
		Comments:         unmarshalComments(n.Comments),
		AuthorName:       n.AuthorName,
		AuthorLink:       n.AuthorLink,
		AuthorFollowers:  n.AuthorFollowers,
		AITopicAngle:     n.AITopicAngle,
		AIViralReason:    n.AIViralReason,
		AIBorrowable:     n.AIBorrowable,
		AITargetAudience: n.AITargetAudience,
		AITitleFormula:   n.AITitleFormula,
		AIOneLine:        n.AIOneLine,
		EnrichStatus:     n.EnrichStatus,
		CollectedAt:      n.CollectedAt,
		CrawledAt:        n.CrawledAt,
		CreatedAt:        n.CreatedAt,
		UpdatedAt:        n.UpdatedAt,
	}
}

// unmarshalTags 把 datatypes.JSON 反解为 []string；空 / 非法 JSON 返回空切片（不返回 nil
// 以便前端拿到稳定的 [] 而非 null）。
func unmarshalTags(raw datatypes.JSON) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var tags []string
	if err := json.Unmarshal(raw, &tags); err != nil {
		return []string{}
	}
	return tags
}

// unmarshalComments 把 datatypes.JSON 反解为 []CommentItem；空 / 非法 JSON 返回空切片。
func unmarshalComments(raw datatypes.JSON) []CommentItem {
	if len(raw) == 0 {
		return []CommentItem{}
	}
	var comments []CommentItem
	if err := json.Unmarshal(raw, &comments); err != nil {
		return []CommentItem{}
	}
	return comments
}
