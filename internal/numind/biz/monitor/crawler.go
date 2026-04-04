package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/spf13/viper"
	"gorm.io/gorm"

	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ========== xhs-service response types ==========

// XhsNoteSummary xhs-service 返回的笔记摘要
type XhsNoteSummary struct {
	NoteID      string `json:"note_id"`
	Title       string `json:"title"`
	NoteType    string `json:"note_type"` // "image" or "video"
	PublishedAt string `json:"published_at"`
}

// XhsNoteDetail xhs-service 返回的完整笔记详情
type XhsNoteDetail struct {
	NoteID      string   `json:"note_id"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	NoteType    string   `json:"note_type"`
	Tags        []string `json:"tags"`
	Likes       int      `json:"likes"`
	Comments    int      `json:"comments"`
	Collects    int      `json:"collects"`
	Shares      int      `json:"shares"`
	Images      []string `json:"images"`
	VideoURL    string   `json:"video_url"`
	PublishedAt string   `json:"published_at"`
}

// xhsServiceBaseURL 获取 xhs-service 的基础 URL
func xhsServiceBaseURL() string {
	url := viper.GetString("monitor.xhs_service.base_url")
	if url == "" {
		return "http://localhost:8100"
	}
	return url
}

// maxConcurrentBloggers 获取最大并发爬取博主数
func maxConcurrentBloggers() int {
	n := viper.GetInt("monitor.crawler.max_concurrent_bloggers")
	if n <= 0 {
		return 3
	}
	return n
}

// requestIntervalSeconds 获取请求间隔秒数（防止限流）
func requestIntervalSeconds() int {
	n := viper.GetInt("monitor.crawler.request_interval_seconds")
	if n <= 0 {
		return 5
	}
	return n
}

// maxConsecutiveFailures 获取最大连续失败次数
func maxConsecutiveFailures() int {
	n := viper.GetInt("monitor.crawler.max_consecutive_failures")
	if n <= 0 {
		return 5
	}
	return n
}

// newXhsHTTPClient 创建用于调用 xhs-service 的 HTTP 客户端
func newXhsHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// fetchUserNotes 从 xhs-service 获取博主的笔记列表
func fetchUserNotes(ctx context.Context, client *http.Client, xhsUserID string) ([]XhsNoteSummary, error) {
	url := fmt.Sprintf("%s/xhs/user-notes/%s", xhsServiceBaseURL(), xhsUserID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetchUserNotes: build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetchUserNotes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetchUserNotes: status %d, body: %s", resp.StatusCode, string(body))
	}

	var summaries []XhsNoteSummary
	if err := json.NewDecoder(resp.Body).Decode(&summaries); err != nil {
		return nil, fmt.Errorf("fetchUserNotes: decode: %w", err)
	}
	return summaries, nil
}

// fetchNoteDetail 从 xhs-service 获取笔记详情
func fetchNoteDetail(ctx context.Context, client *http.Client, noteID string) (*XhsNoteDetail, error) {
	url := fmt.Sprintf("%s/xhs/note-detail/%s", xhsServiceBaseURL(), noteID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetchNoteDetail: build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetchNoteDetail: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetchNoteDetail: status %d, body: %s", resp.StatusCode, string(body))
	}

	var detail XhsNoteDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, fmt.Errorf("fetchNoteDetail: decode: %w", err)
	}
	return &detail, nil
}

// CrawlBloggers 批量爬取博主的最新笔记
// 对每个博主并发执行（受 semaphore 限制），获取笔记列表、去重、插入新笔记、触发转录和分析。
func (mb *MonitorBiz) CrawlBloggers(ctx context.Context, userID uint, bloggerIDs []uint) error {
	// 创建 Langfuse trace
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "monitor-crawl",
		langfuse.WithUserID(userID),
		langfuse.WithTraceInput(map[string]interface{}{
			"blogger_ids": bloggerIDs,
		}),
		langfuse.WithTraceTags("monitor"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	// 注入计费上下文
	ctx = billing.WithBilling(ctx, userID, "monitor_analyze")

	concurrency := maxConcurrentBloggers()
	sem := make(chan struct{}, concurrency)
	interval := time.Duration(requestIntervalSeconds()) * time.Second
	client := newXhsHTTPClient()

	var wg sync.WaitGroup
	for _, bid := range bloggerIDs {
		wg.Add(1)
		go func(bloggerID uint) {
			defer wg.Done()

			// 获取 semaphore slot
			sem <- struct{}{}
			defer func() { <-sem }()

			mb.crawlOneBlogger(ctx, client, userID, bloggerID, interval)
		}(bid)
	}
	wg.Wait()

	return nil
}

// crawlOneBlogger 爬取单个博主的笔记
func (mb *MonitorBiz) crawlOneBlogger(ctx context.Context, client *http.Client, userID, bloggerID uint, interval time.Duration) {
	logger := log.Infow
	_ = logger

	// 1. 获取博主信息
	blogger, err := mb.store.Monitor().GetBlogger(ctx, bloggerID)
	if err != nil {
		log.Errorw("crawlOneBlogger: get blogger failed", "bloggerID", bloggerID, "error", err)
		return
	}

	// 2. 获取笔记列表
	summaries, err := fetchUserNotes(ctx, client, blogger.XhsUserID)
	if err != nil {
		log.Errorw("crawlOneBlogger: fetch user notes failed", "bloggerID", bloggerID, "xhsUserID", blogger.XhsUserID, "error", err)
		mb.updateBloggerFailure(ctx, blogger, err)
		return
	}

	// 请求间隔
	time.Sleep(interval)

	// 3. 遍历笔记，去重 + 获取详情 + 插入
	var newNotes []*model.MonitorNote
	for _, summary := range summaries {
		// 去重：检查 (user_id, xhs_note_id) 是否已存在
		_, err := mb.store.Monitor().GetNoteByXhsID(ctx, userID, summary.NoteID)
		if err == nil {
			// already exists, skip
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Errorw("Failed to check note existence", "error", err)
			continue
		}
		// Not found — this is a new note, proceed

		// 获取笔记详情
		detail, err := fetchNoteDetail(ctx, client, summary.NoteID)
		if err != nil {
			log.Errorw("crawlOneBlogger: fetch note detail failed", "noteID", summary.NoteID, "error", err)
			time.Sleep(interval)
			continue
		}
		time.Sleep(interval)

		// 构建 MonitorNote 并插入
		note, err := mb.buildAndInsertNote(ctx, userID, bloggerID, detail)
		if err != nil {
			log.Errorw("crawlOneBlogger: insert note failed", "noteID", summary.NoteID, "error", err)
			continue
		}
		newNotes = append(newNotes, note)
	}

	// 4. 对视频笔记触发转录
	for _, note := range newNotes {
		if note.NoteType == model.NoteTypeVideo && note.VideoURL != "" {
			if err := mb.TranscribeVideo(ctx, note); err != nil {
				// 转录失败不阻塞整体流程
				log.Warnw("crawlOneBlogger: transcribe video failed", "noteID", note.ID, "error", err)
			}
		}
	}

	// 5. 对新笔记触发 AI 分析（Task 7 实现，目前调用 stub）
	for _, note := range newNotes {
		if err := mb.AnalyzeNote(ctx, userID, note.ID); err != nil {
			log.Warnw("crawlOneBlogger: analyze note failed", "noteID", note.ID, "error", err)
		}
	}

	// 6. 更新博主状态 — 成功
	mb.updateBloggerSuccess(ctx, blogger, newNotes)

	log.Infow("crawlOneBlogger: completed",
		"bloggerID", bloggerID,
		"xhsUserID", blogger.XhsUserID,
		"totalNotes", len(summaries),
		"newNotes", len(newNotes),
	)
}

// buildAndInsertNote 构建 MonitorNote 模型并插入数据库
func (mb *MonitorBiz) buildAndInsertNote(ctx context.Context, userID, bloggerID uint, detail *XhsNoteDetail) (*model.MonitorNote, error) {
	// 解析发布时间
	var publishedAt *time.Time
	if detail.PublishedAt != "" {
		// 尝试多种时间格式
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z"} {
			if t, err := time.Parse(layout, detail.PublishedAt); err == nil {
				publishedAt = &t
				break
			}
		}
	}

	// 序列化 tags 和 images 为 JSON
	tagsJSON, _ := json.Marshal(detail.Tags)
	imagesJSON, _ := json.Marshal(detail.Images)

	note := &model.MonitorNote{
		UserID:      userID,
		BloggerID:   bloggerID,
		XhsNoteID:   detail.NoteID,
		Title:       detail.Title,
		Content:     detail.Content,
		NoteType:    detail.NoteType,
		Tags:        tagsJSON,
		Likes:       uint(detail.Likes),
		Comments:    uint(detail.Comments),
		Collects:    uint(detail.Collects),
		Shares:      uint(detail.Shares),
		Images:      imagesJSON,
		VideoURL:    detail.VideoURL,
		PublishedAt: publishedAt,
	}

	if err := mb.store.Monitor().CreateNote(ctx, note); err != nil {
		return nil, fmt.Errorf("buildAndInsertNote: %w", err)
	}

	return note, nil
}

// updateBloggerSuccess 爬取成功后更新博主状态
func (mb *MonitorBiz) updateBloggerSuccess(ctx context.Context, blogger *model.MonitorBlogger, newNotes []*model.MonitorNote) {
	now := time.Now()
	blogger.LastCheckAt = &now
	blogger.ConsecutiveFailures = 0
	blogger.CheckError = ""

	// 更新最新笔记时间
	if len(newNotes) > 0 {
		var latest time.Time
		for _, n := range newNotes {
			if n.PublishedAt != nil && n.PublishedAt.After(latest) {
				latest = *n.PublishedAt
			}
		}
		if !latest.IsZero() {
			blogger.LastNoteAt = &latest
		}
	}

	if err := mb.store.Monitor().UpdateBlogger(ctx, blogger); err != nil {
		log.Errorw("updateBloggerSuccess: update blogger failed", "bloggerID", blogger.ID, "error", err)
	}
}

// updateBloggerFailure 爬取失败后更新博主状态
func (mb *MonitorBiz) updateBloggerFailure(ctx context.Context, blogger *model.MonitorBlogger, crawlErr error) {
	now := time.Now()
	blogger.LastCheckAt = &now
	blogger.ConsecutiveFailures++
	blogger.CheckError = crawlErr.Error()

	// 连续失败超限，自动停用
	if blogger.ConsecutiveFailures >= uint(maxConsecutiveFailures()) {
		blogger.IsActive = false
		log.Warnw("updateBloggerFailure: auto-deactivating blogger due to consecutive failures",
			"bloggerID", blogger.ID,
			"consecutiveFailures", blogger.ConsecutiveFailures,
		)
	}

	if err := mb.store.Monitor().UpdateBlogger(ctx, blogger); err != nil {
		log.Errorw("updateBloggerFailure: update blogger failed", "bloggerID", blogger.ID, "error", err)
	}
}
