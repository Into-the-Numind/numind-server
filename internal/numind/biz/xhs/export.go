package xhs

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// 导出 CSV 的约束与常量。
const (
	// maxExportIDs 单次导出的笔记数上限（与 API 契约 design §3 一致）。超过即拒（ErrBind），
	// 防一次性导出过大产生超大 CSV / COS 对象。
	maxExportIDs = 200

	// exportURLExpirySeconds 导出 CSV 下载链接的有效期（1 小时，design §3）。
	exportURLExpirySeconds int64 = 3600

	// csvTimeLayout 时间字段在 CSV 中的格式（RFC3339，便于 Excel / 脚本二次解析）。
	csvTimeLayout = time.RFC3339
)

// exportCSVHeader 是导出 CSV 的列头（源字段 + images + comments，AI 分析已移除）。
// 顺序即 buildExportCSV 写每行的字段顺序，二者必须保持一致。
var exportCSVHeader = []string{
	"id",
	"xhs_note_id",
	"note_type",
	"title",
	"content",
	"tags",
	"cover_url",
	"note_url",
	"published_at",
	"video_url",
	"video_transcript",
	"like_count",
	"collect_count",
	"comment_count",
	"share_count",
	"author_name",
	"author_link",
	"author_followers",
	"enrich_status",
	"collected_at",
	"images",
	"comments",
}

// Export 把 userID 选中的若干条笔记导出为 CSV，上传 COS 后返回 1 小时有效的签名下载链接。
//
// 流程：参数校验（≤200、非空）→ GetByIDs（store 已强制 WHERE user_id，天然 user 隔离，
// 他人 id 直接查不到、不会进 CSV）→ 拼 CSV（UTF-8 BOM 便于 Excel 正确识别中文）→
// UploadBytesToCOS → GenerateSignedDownloadURL(1h)。
//
// 计费：导出不扣分（design §1：采集/入库/看列表/CSV 导出均不扣，仅 AI 分析 + ASR 扣）。
//
// 越权防护：ids 全部经 store.GetByIDs(userID, ids) 过滤，传入他人 id 不会命中，
// 仅导出自己拥有的笔记（user 隔离的唯一裁决点在 store）。
func (b *XhsBiz) Export(ctx context.Context, userID uint, ids []uint64) (downloadURL string, err error) {
	if len(ids) == 0 {
		return "", errno.ErrBind.SetMessage("ids 不能为空")
	}
	if len(ids) > maxExportIDs {
		return "", errno.ErrBind.SetMessage("单次导出最多 %d 条，当前 %d 条", maxExportIDs, len(ids))
	}

	rows, err := b.store.GetByIDs(ctx, userID, ids)
	if err != nil {
		return "", fmt.Errorf("Export GetByIDs: %w", err)
	}
	if len(rows) == 0 {
		// 选中的 id 全部不属于该用户（或已删除）→ 无可导出内容，视为参数错误而非空 CSV，
		// 给前端明确反馈（避免下载一个只有表头的空文件）。
		return "", errno.ErrXhsNoteNotFound
	}

	csvBytes, err := buildExportCSV(rows)
	if err != nil {
		return "", fmt.Errorf("Export buildCSV: %w", err)
	}

	objectKey := fmt.Sprintf("xhs-export/%d/%d.csv", userID, time.Now().UnixNano())
	if _, err := util.UploadBytesToCOS(ctx, objectKey, "text/csv; charset=utf-8", csvBytes); err != nil {
		return "", fmt.Errorf("Export upload: %w", err)
	}

	filename := fmt.Sprintf("小红书选题_%s.csv", time.Now().Format("20060102_150405"))
	url, err := util.GenerateSignedDownloadURL(ctx, objectKey, filename, exportURLExpirySeconds)
	if err != nil {
		return "", fmt.Errorf("Export sign: %w", err)
	}
	return url, nil
}

// formatCommentsForCSV 把评论（含一层回复）序列化成单元格：
// "作者：正文（回复：作者：正文；…）"，多条评论用 " ||| " 分隔。
func formatCommentsForCSV(comments []CommentItem) string {
	if len(comments) == 0 {
		return ""
	}
	parts := make([]string, 0, len(comments))
	for _, c := range comments {
		seg := c.Author + "：" + c.Text
		if len(c.Replies) > 0 {
			rs := make([]string, 0, len(c.Replies))
			for _, r := range c.Replies {
				rs = append(rs, r.Author+"："+r.Text)
			}
			seg += "（回复：" + strings.Join(rs, "；") + "）"
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, " ||| ")
}

// buildExportCSV 把笔记行拼成 UTF-8（带 BOM）CSV 字节。
//
// 纯函数，无 COS / 网络依赖，便于单测断言 CSV 内容（列头 + 选中字段）。
// 写 BOM（EF BB BF）使 Excel 默认按 UTF-8 解析，避免中文乱码。
// tags / comments 等 JSON 字段复用 toNoteItem 的解析（与列表 / 详情出参口径一致），
// tags 用 “;” 连接成单元格；published_at/collected_at/crawled_at 用 RFC3339。
func buildExportCSV(rows []model.XhsTopicNote) ([]byte, error) {
	var buf bytes.Buffer
	// UTF-8 BOM，便于 Excel 正确识别中文编码。
	buf.Write([]byte{0xEF, 0xBB, 0xBF})

	w := csv.NewWriter(&buf)
	if err := w.Write(exportCSVHeader); err != nil {
		return nil, fmt.Errorf("write header: %w", err)
	}

	for i := range rows {
		item := toNoteItem(&rows[i])
		record := []string{
			strconv.FormatUint(item.ID, 10),
			item.XhsNoteID,
			item.NoteType,
			item.Title,
			item.Content,
			joinTags(item.Tags),
			item.CoverURL,
			item.NoteURL,
			formatTimePtr(item.PublishedAt),
			item.VideoURL,
			derefString(item.VideoTranscript),
			strconv.Itoa(item.LikeCount),
			strconv.Itoa(item.CollectCount),
			strconv.Itoa(item.CommentCount),
			strconv.Itoa(item.ShareCount),
			item.AuthorName,
			item.AuthorLink,
			strconv.Itoa(item.AuthorFollowers),
			item.EnrichStatus,
			formatTimePtr(item.CollectedAt),
			joinTags(item.Images),
			formatCommentsForCSV(item.Comments),
		}
		if err := w.Write(record); err != nil {
			return nil, fmt.Errorf("write row %d: %w", item.ID, err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("flush csv: %w", err)
	}
	return buf.Bytes(), nil
}

// joinTags 把标签切片用 “;” 连接为单个 CSV 单元格（空切片 → 空串）。
func joinTags(tags []string) string {
	out := ""
	for i, t := range tags {
		if i > 0 {
			out += ";"
		}
		out += t
	}
	return out
}

// formatTimePtr 把可空时间格式化为 RFC3339；nil → 空串。
func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(csvTimeLayout)
}

// derefString 解引用可空字符串；nil → 空串。
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
