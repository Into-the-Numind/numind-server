package xhs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/datatypes"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// 采集摄入的校验上限。
const (
	maxNotesPerIngest = 50    // 单次摄入的笔记条数上限
	maxTextBytes      = 65536 // 单个长文本字段（content / video_transcript 等）字节上限 = 64KB
	maxComments       = 100   // 每条笔记保留的顶层评论条数上限
	maxReplies        = 30    // 每条评论保留的回复条数上限
	maxCommentBytes   = 2000  // 单条评论 text 字节上限（超出截断；中文约 660 字）
)

// CommentPayload 是插件上送的单条热门评论。
type CommentPayload struct {
	Author     string           `json:"author"`
	Text       string           `json:"text"`
	LikeCount  int              `json:"like_count"`
	IPLocation string           `json:"ip_location"`
	Replies    []CommentPayload `json:"replies"`
}

// NotePayload 是浏览器插件上送的单条小红书笔记的结构化数据。
//
// 不含 user_id —— user_id 从鉴权上下文获取，由 controller 传入 Ingest，
// 避免插件伪造他人归属（多租户隔离）。
// parseFlexibleTime 宽松解析小红书给的时间字符串（纯日期 / 日期时间 / RFC3339），解析不出返回 nil。
// 小红书发布时间常为纯日期 "2026-06-23"，直接当 *time.Time 反序列化会 RFC3339 解析失败。
func parseFlexibleTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	layouts := []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02", "2006/01/02"}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return &t
		}
	}
	return nil
}

type NotePayload struct {
	XhsNoteID       string           `json:"xhs_note_id"`
	NoteType        string           `json:"note_type"` // normal/video，空 = normal
	Title           string           `json:"title"`
	Content         string           `json:"content"`
	Tags            []string         `json:"tags"`
	CoverURL        string           `json:"cover_url"`
	Images          []string         `json:"images"`
	NoteURL         string           `json:"note_url"`
	PublishedAt     string           `json:"published_at"`
	VideoURL        string           `json:"video_url"`
	VideoTranscript *string          `json:"video_transcript"`
	LikeCount       int              `json:"like_count"`
	CollectCount    int              `json:"collect_count"`
	CommentCount    int              `json:"comment_count"`
	ShareCount      int              `json:"share_count"`
	Comments        []CommentPayload `json:"comments"`
	AuthorName      string           `json:"author_name"`
	AuthorLink      string           `json:"author_link"`
	AuthorFollowers int              `json:"author_followers"`
	CollectedAt     string           `json:"collected_at"`
}

// Ingest 批量摄入插件上送的小红书笔记到 userID 的私有累积选题库。
//
// 流程：校验（≤50 条、xhs_note_id 必填、长文本 ≤64KB、评论 ≤10 条且单条 ≤200 字截断）
// → 计算 content_hash=SHA256(title+content+video_url) → store.UpsertByUserNote
// 去重 upsert。内容变化（hashChanged=true）或新增时把 enrich_status 置为 pending、
// 清空旧富化字段，触发对新内容的重新富化；内容未变化则保留已有记录与富化结果，
// 避免重复富化重复扣分（防重复扣分回归点）。
//
// 返回成功摄入的条数与对应的笔记主键（按入参顺序）。
//
// 两阶段语义（避免部分提交导致的误导性返回值）：
//   - 阶段一：先对全部 payload 跑 buildNote 校验，任一条非法立即返回 ErrBind，
//     一行都不落库（校验层面 all-or-nothing）。
//   - 阶段二：逐条 upsert。若中途某条 DB 出错，返回 (已成功提交的条数, 已成功的主键, err)
//     —— 不再谎报 0；幂等设计下调用方可安全整批重试。
//
// 富化投递（T3b）：当且仅当笔记为新增或内容变化（store 返回 hashChanged=true、
// enrich_status 被重置为 pending）时投递富化队列。内容未变化（hashChanged=false）
// 时保留已有富化结果、不重复投递，避免重复富化重复扣分。enricher 为 nil 时跳过
// 投递（由 ListPendingEnrich 扫描兜底）。
func (b *XhsBiz) Ingest(ctx context.Context, userID uint, payloads []NotePayload) (ingested int, ids []uint64, err error) {
	if len(payloads) == 0 {
		return 0, nil, errno.ErrBind.SetMessage("notes 不能为空")
	}
	if len(payloads) > maxNotesPerIngest {
		return 0, nil, errno.ErrBind.SetMessage("单次最多摄入 %d 条笔记，收到 %d 条", maxNotesPerIngest, len(payloads))
	}

	// 阶段一：全量校验（不触 DB）。任一条非法即整批拒绝，保证校验层面 all-or-nothing。
	notes := make([]*model.XhsTopicNote, len(payloads))
	for i := range payloads {
		note, vErr := buildNote(userID, &payloads[i])
		if vErr != nil {
			return 0, nil, vErr
		}
		notes[i] = note
	}

	// 阶段二：逐条 upsert。DB 出错时返回已实际提交的条数/主键（非谎报 0）。
	ids = make([]uint64, 0, len(notes))
	for _, note := range notes {
		hashChanged, uErr := b.store.UpsertByUserNote(ctx, note)
		if uErr != nil {
			return ingested, ids, errno.InternalServerError.SetMessage("Ingest: upsert 笔记 %s 失败: %v", note.XhsNoteID, uErr)
		}

		ingested++
		ids = append(ids, note.ID)

		// 仅新增或内容变化（已重置为 pending）的笔记才投递富化队列，避免重复富化扣分。
		if hashChanged && b.enricher != nil {
			b.enricher.Enqueue(userID, note.ID)
		}
	}

	return ingested, ids, nil
}

// buildNote 校验单条 payload 并构造待 upsert 的 model.XhsTopicNote。
//
// content_hash 变化或新增时由 store 用 Save 覆盖全字段，故此处统一把 enrich_status
// 置为 pending、AI 字段留空：内容变化即重置富化状态；内容未变化时 store 比对 hash
// 后保留已有记录（含富化结果），本笔记的 pending 置位不会生效（防重复扣分）。
func buildNote(userID uint, p *NotePayload) (*model.XhsTopicNote, error) {
	if strings.TrimSpace(p.XhsNoteID) == "" {
		return nil, errno.ErrBind.SetMessage("xhs_note_id 不能为空")
	}
	if len(p.Content) > maxTextBytes {
		return nil, errno.ErrBind.SetMessage("笔记 %s 正文超出 %d 字节上限", p.XhsNoteID, maxTextBytes)
	}
	if p.VideoTranscript != nil && len(*p.VideoTranscript) > maxTextBytes {
		return nil, errno.ErrBind.SetMessage("笔记 %s 视频转写超出 %d 字节上限", p.XhsNoteID, maxTextBytes)
	}

	noteType := p.NoteType
	if noteType == "" {
		noteType = model.XhsNoteTypeNormal
	}
	// note_type 必须落在枚举内（normal/video）。非法值会让 T4 富化流水线按
	// note_type=="video" 的 ASR 分支误路由，故此处直接拒绝而非静默落库。
	if noteType != model.XhsNoteTypeNormal && noteType != model.XhsNoteTypeVideo {
		return nil, errno.ErrBind.SetMessage("笔记 %s note_type 非法: %s，仅支持 normal/video", p.XhsNoteID, noteType)
	}

	note := &model.XhsTopicNote{
		UserID:          userID,
		XhsNoteID:       strings.TrimSpace(p.XhsNoteID),
		ContentHash:     contentHash(p.Title, p.Content, p.VideoURL),
		NoteType:        noteType,
		Title:           p.Title,
		Content:         p.Content,
		Tags:            marshalJSON(p.Tags),
		CoverURL:        p.CoverURL,
		Images:          marshalJSON(p.Images),
		NoteURL:         p.NoteURL,
		PublishedAt:     parseFlexibleTime(p.PublishedAt),
		VideoURL:        p.VideoURL,
		VideoTranscript: p.VideoTranscript,
		LikeCount:       p.LikeCount,
		CollectCount:    p.CollectCount,
		CommentCount:    p.CommentCount,
		ShareCount:      p.ShareCount,
		Comments:        marshalJSON(truncateComments(p.Comments)),
		AuthorName:      p.AuthorName,
		AuthorLink:      p.AuthorLink,
		AuthorFollowers: p.AuthorFollowers,
		EnrichStatus:    model.XhsEnrichPending,
		CollectedAt:     parseFlexibleTime(p.CollectedAt),
		CrawledAt:       time.Now(),
	}
	return note, nil
}

// contentHash 计算 SHA256(title+content+video_url)，用于判定笔记内容是否变化、防重复富化扣分。
func contentHash(title, content, videoURL string) string {
	h := sha256.New()
	// 用 \x00 分隔各字段，避免字段边界歧义（如 ("ab","")=("a","b") 误判同 hash）。
	h.Write([]byte(title))
	h.Write([]byte{0})
	h.Write([]byte(content))
	h.Write([]byte{0})
	h.Write([]byte(videoURL))
	return hex.EncodeToString(h.Sum(nil))
}

// truncateComments 截断热门评论：至多 maxComments 条，每条 text 至多 maxCommentBytes 字节。
func truncateComments(comments []CommentPayload) []CommentPayload {
	if len(comments) == 0 {
		return nil
	}
	if len(comments) > maxComments {
		comments = comments[:maxComments]
	}
	out := make([]CommentPayload, len(comments))
	for i, c := range comments {
		if len(c.Text) > maxCommentBytes {
			c.Text = truncateUTF8(c.Text, maxCommentBytes)
		}
		if len(c.Replies) > maxReplies {
			c.Replies = c.Replies[:maxReplies]
		}
		for j := range c.Replies {
			c.Replies[j].Replies = nil // 回复不再嵌套（只保留一层）
			if len(c.Replies[j].Text) > maxCommentBytes {
				c.Replies[j].Text = truncateUTF8(c.Replies[j].Text, maxCommentBytes)
			}
		}
		out[i] = c
	}
	return out
}

// truncateUTF8 把 s 截断到不超过 limit 字节，且不切断多字节 UTF-8 字符（按 rune 边界回退）。
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	// 回退到 <= limit 的 rune 边界（UTF-8 continuation byte 高两位为 10）。
	end := limit
	for end > 0 && s[end]&0xC0 == 0x80 {
		end--
	}
	return s[:end]
}

// marshalJSON 把任意值序列化为 datatypes.JSON；nil 或空切片返回 nil（DB 列存 NULL）。
func marshalJSON(v interface{}) datatypes.JSON {
	switch vv := v.(type) {
	case []string:
		if len(vv) == 0 {
			return nil
		}
	case []CommentPayload:
		if len(vv) == 0 {
			return nil
		}
	case nil:
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return datatypes.JSON(raw)
}
