// Package meeting 会议副驾 (Meeting Copilot) v1 业务逻辑层（meeting-copilot feature）。
//
// 全新独立模式，代码高度自包含、可整体删除。覆盖：
//   - 会话生命周期（Create / Get / List / End）
//   - 角色预设 CRUD（含系统内置模板）
//   - 分段近实时转写（transcribe.go：COS 上传 + aiservice.ASR）
//   - 反馈 judge+生成（feedback.go：单次 LLM 调用兼判官，SSE callback）
//   - 结束时结构化纪要（summary.go）
//
// 计费纪律（SPEC §1）：所有 LLM/ASR 调用走 aiservice 统一入口（保证 Langfuse +
// UsageRecord 自动记录），但**不**调用 credits 的 Reserve/Reconcile、**不**做会员门槛
// 校验。内部试用阶段「仅记录用量、不扣积分、不拦截」。实现见 internalCallCtx。
package meeting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// SSEHandler 是反馈 SSE 事件推送回调（SPEC §3.1）。
// eventType ∈ {token, skip, done, error}；data 为对应 payload，由 controller 序列化为
// `data: {"type":...,"data":...}\n\n` 帧。返回 error 表示下游（客户端连接）已断开，
// 生成逻辑应尽快停止。
type SSEHandler func(eventType string, data interface{}) error

// ---------------------------------------------------------------------------
// 请求 / DTO 类型（json 与 SPEC §2/§3 对齐：snake_case，与 model 一致）
// ---------------------------------------------------------------------------

// CreateSessionReq 创建会话请求（SPEC §3 POST /v1/meetings）。
type CreateSessionReq struct {
	RolePrompt          string  `json:"role_prompt" binding:"required"`
	PresetID            *uint64 `json:"preset_id"`
	AutoIntervalSeconds *int    `json:"auto_interval_seconds"`
	Title               string  `json:"title"`
}

// IngestSegmentReq 分段转写请求（SPEC §3 POST /v1/meetings/:id/segments）。
// 音频字节由 controller 从 multipart 读出后传入；Seq/StartMs 为可选。
type IngestSegmentReq struct {
	AudioBytes []byte
	// Seq 前端给定的顺序；<=0 时由后端按 max(seq)+1 自增。
	Seq int
	// StartMs 相对会议开始的毫秒偏移（best-effort）。
	StartMs int
}

// FeedbackReq 反馈请求（SPEC §3.1 POST /v1/meetings/:id/feedback）。
type FeedbackReq struct {
	Trigger string `json:"trigger" binding:"required"`
}

// SavePresetReq 存预设请求（SPEC §3 POST /v1/meetings/presets）。
type SavePresetReq struct {
	Name                string `json:"name" binding:"required"`
	RolePrompt          string `json:"role_prompt" binding:"required"`
	AutoIntervalSeconds *int   `json:"auto_interval_seconds"`
}

// SessionDTO 会话 DTO（SPEC §3：时间字段 ISO8601 由 time.Time json 默认编码保证）。
type SessionDTO struct {
	ID                  uint64     `json:"id"`
	Title               string     `json:"title"`
	RolePrompt          string     `json:"role_prompt"`
	PresetID            *uint64    `json:"preset_id,omitempty"`
	Status              string     `json:"status"`
	AutoIntervalSeconds int        `json:"auto_interval_seconds"`
	DurationSeconds     int        `json:"duration_seconds"`
	Summary             string     `json:"summary,omitempty"`
	SummaryStatus       string     `json:"summary_status"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	EndedAt             *time.Time `json:"ended_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// SegmentDTO 转写分段 DTO（SPEC §3 segments 返回体）。
type SegmentDTO struct {
	ID              uint64    `json:"id"`
	Seq             int       `json:"seq"`
	Text            string    `json:"text"`
	StartMs         int       `json:"start_ms"`
	DurationSeconds float64   `json:"duration_seconds"`
	AudioURL        string    `json:"audio_url,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// FeedbackDTO 反馈 DTO（SPEC §3.1 done 事件 payload）。
type FeedbackDTO struct {
	ID        uint64    `json:"id"`
	Trigger   string    `json:"trigger"`
	AnchorSeq int       `json:"anchor_seq"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// PresetDTO 预设 DTO（SPEC §3 presets 返回体）。
type PresetDTO struct {
	ID                  uint64    `json:"id"`
	UserID              uint      `json:"user_id"`
	Name                string    `json:"name"`
	RolePrompt          string    `json:"role_prompt"`
	AutoIntervalSeconds int       `json:"auto_interval_seconds"`
	IsBuiltin           bool      `json:"is_builtin"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// SessionDetailDTO 会话详情（SPEC §3 GET /v1/meetings/:id）。
type SessionDetailDTO struct {
	Session   SessionDTO    `json:"session"`
	Segments  []SegmentDTO  `json:"segments"`
	Feedbacks []FeedbackDTO `json:"feedbacks"`
}

// ---------------------------------------------------------------------------
// 业务接口
// ---------------------------------------------------------------------------

// IMeetingBiz 会议副驾业务层接口（SPEC §4）。所有方法以 userID 做归属校验。
type IMeetingBiz interface {
	// --- 会话生命周期 ---
	CreateSession(ctx context.Context, userID uint, req *CreateSessionReq) (*SessionDTO, error)
	GetSession(ctx context.Context, userID uint, id uint64) (*SessionDetailDTO, error)
	ListSessions(ctx context.Context, userID uint, offset, limit int) ([]SessionDTO, int64, error)
	EndSession(ctx context.Context, userID uint, id uint64, generateSummary bool) (*SessionDTO, error)

	// --- 分段转写 ---
	IngestSegment(ctx context.Context, userID uint, sessionID uint64, req *IngestSegmentReq) (*SegmentDTO, error)

	// --- 实时流式转写（SPEC §2/§5）---
	// StartRealtimeASR 开一条实时 ASR 编排会话（供 ws controller 调用）。校验会话归属 + active，
	// 失败返回 error（controller 据此拒绝 ws 升级）。
	StartRealtimeASR(ctx context.Context, userID uint, sessionID uint64, handlers RealtimeASRHandlers) (IRealtimeASR, error)

	// --- 整场录音持久化（SPEC §3）---
	// UpdateRecordingURL 把整场录音上传到 COS 并回写 meeting_session.recording_url，返回新 URL。
	UpdateRecordingURL(ctx context.Context, userID uint, sessionID uint64, audio []byte, contentType string) (string, error)

	// --- 反馈（SSE） ---
	GenerateFeedback(ctx context.Context, userID uint, sessionID uint64, req *FeedbackReq, h SSEHandler) error

	// --- 预设 ---
	ListPresets(ctx context.Context, userID uint) ([]PresetDTO, error)
	SavePreset(ctx context.Context, userID uint, req *SavePresetReq) (*PresetDTO, error)
	DeletePreset(ctx context.Context, userID uint, id uint64) error
}

// meetingBiz 是 IMeetingBiz 的实现。
type meetingBiz struct {
	ds store.IStore
}

var _ IMeetingBiz = (*meetingBiz)(nil)

// NewMeetingBiz 创建会议副驾业务层实例。
func NewMeetingBiz(ds store.IStore) IMeetingBiz {
	return &meetingBiz{ds: ds}
}

// defaultAutoIntervalSeconds 自动反馈间隔默认值（未提供时；FEEDBACK_V2_SPEC §1 前端默认 15）。
const defaultAutoIntervalSeconds = 15

// autoIntervalMinSeconds / autoIntervalMaxSeconds 自动反馈间隔合法区间（FEEDBACK_V2_SPEC §1：
// 用户自设 5-60 秒，去档位；后端校验放宽到 5-60，越界 clamp 而非拒绝）。
const (
	autoIntervalMinSeconds = 5
	autoIntervalMaxSeconds = 60
)

// clampAutoInterval 把提供的间隔 clamp 到 [5,60]（FEEDBACK_V2_SPEC §1）。<=0 视为未提供，
// 由调用方回退默认值。
func clampAutoInterval(v int) int {
	if v < autoIntervalMinSeconds {
		return autoIntervalMinSeconds
	}
	if v > autoIntervalMaxSeconds {
		return autoIntervalMaxSeconds
	}
	return v
}

// ---------------------------------------------------------------------------
// 会话生命周期
// ---------------------------------------------------------------------------

// CreateSession 创建会话。若 preset_id 给定则校验其可用性（本人或内置）。
func (b *meetingBiz) CreateSession(ctx context.Context, userID uint, req *CreateSessionReq) (*SessionDTO, error) {
	interval := defaultAutoIntervalSeconds
	if req.AutoIntervalSeconds != nil && *req.AutoIntervalSeconds > 0 {
		// 放宽到 5-60，越界 clamp（FEEDBACK_V2_SPEC §1：用户自设 5-60，去档位）。
		interval = clampAutoInterval(*req.AutoIntervalSeconds)
	}

	// preset_id 给定时校验存在 + 归属（本人或内置 user_id=0），但 role_prompt 以请求为准
	// （前端载入预设后可能已编辑）。
	if req.PresetID != nil {
		preset, err := b.ds.Meetings().GetPreset(ctx, *req.PresetID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errno.ErrInvalidParameter.SetMessage("预设不存在")
			}
			return nil, fmt.Errorf("CreateSession: get preset: %w", err)
		}
		if preset.UserID != userID && preset.UserID != 0 {
			return nil, errno.ErrForbidden.SetMessage("无权使用该预设")
		}
	}

	now := time.Now()
	title := req.Title
	if title == "" {
		title = fmt.Sprintf("未命名会议 %s", now.Format("2006-01-02 15:04"))
	}

	s := &model.MeetingSession{
		UserID:              userID,
		Title:               title,
		RolePrompt:          req.RolePrompt,
		PresetID:            req.PresetID,
		Status:              model.MeetingSessionStatusActive,
		AutoIntervalSeconds: interval,
		SummaryStatus:       model.MeetingSummaryStatusNone,
		StartedAt:           &now,
	}
	if err := b.ds.Meetings().CreateSession(ctx, s); err != nil {
		return nil, fmt.Errorf("CreateSession: %w", err)
	}

	dto := toSessionDTO(s)
	return &dto, nil
}

// GetSession 返回会话详情（含 segments + feedbacks），校验归属。
func (b *meetingBiz) GetSession(ctx context.Context, userID uint, id uint64) (*SessionDetailDTO, error) {
	s, err := b.getOwnedSession(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	segs, err := b.ds.Meetings().ListSegmentsBySession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("GetSession: list segments: %w", err)
	}
	fbs, err := b.ds.Meetings().ListFeedbacksBySession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("GetSession: list feedbacks: %w", err)
	}

	detail := &SessionDetailDTO{
		Session:   toSessionDTO(s),
		Segments:  toSegmentDTOs(segs),
		Feedbacks: toFeedbackDTOs(fbs),
	}
	return detail, nil
}

// ListSessions 分页列出用户会话（按 created_at DESC，由 store 保证）。
func (b *meetingBiz) ListSessions(ctx context.Context, userID uint, offset, limit int) ([]SessionDTO, int64, error) {
	list, total, err := b.ds.Meetings().ListSessions(ctx, userID, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("ListSessions: %w", err)
	}
	out := make([]SessionDTO, 0, len(list))
	for i := range list {
		out = append(out, toSessionDTO(&list[i]))
	}
	return out, total, nil
}

// asyncSummarySpawn 是异步纪要生成的派发点（FEEDBACK_V2_SPEC §3.1）。生产用 go func 真异步；
// 单测可替换为同步执行以断言状态机（generating→done/failed）。
var asyncSummarySpawn = func(fn func()) { go fn() }

// EndSession 结束会话：两段式（FEEDBACK_V2_SPEC §3.1）。generateSummary=false 时只结束、
// 不生成纪要（summary_status=skipped，跳过异步段）。
//
//	同步段（秒回）：校验归属 → 置 status=ended + ended_at + duration + summary_status
//	  （generateSummary=true → generating；false → skipped）→ 持久化 → 立即返回 DTO
//	  （绝不被请求 ctx 取消打断、不阻塞等纪要）。
//	异步段（仅 generateSummary=true）：spawn 后台 goroutine（独立 context.Background() + recover）
//	  → generateFinalSummary（优先基于 running_summary，无则回退读全稿）→ 回写 summary +
//	  summary_status=done；失败置 failed 并 log。
//
// 已结束会话再次调用是幂等的：直接返回当前 DTO（不重复生成纪要）。
func (b *meetingBiz) EndSession(ctx context.Context, userID uint, id uint64, generateSummary bool) (*SessionDTO, error) {
	s, err := b.getOwnedSession(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	if s.Status == model.MeetingSessionStatusEnded {
		dto := toSessionDTO(s)
		return &dto, nil
	}

	// --- 同步段（秒回）---
	now := time.Now()
	s.Status = model.MeetingSessionStatusEnded
	s.EndedAt = &now
	s.DurationSeconds = computeDurationSeconds(s.StartedAt, now)
	// generateSummary=false：用户只想结束、不生成纪要 → 置 skipped，跳过异步生成。
	if generateSummary {
		s.SummaryStatus = model.MeetingSummaryStatusGenerating // 纪要后台生成中
	} else {
		s.SummaryStatus = model.MeetingSummaryStatusSkipped
	}

	if err := b.ds.Meetings().UpdateSession(ctx, s); err != nil {
		return nil, fmt.Errorf("EndSession: update: %w", err)
	}

	// --- 异步段：后台生成纪要（独立 ctx + recover；不被请求取消打断）---
	if generateSummary {
		asyncSummarySpawn(func() {
			b.finalizeSummaryAsync(userID, id)
		})
	}

	dto := toSessionDTO(s)
	return &dto, nil
}

// finalizeSummaryAsync 在后台生成最终纪要并回写（FEEDBACK_V2_SPEC §3.1 异步段）。
//
// 独立 context.Background()（不受 EndSession 请求 ctx 取消影响），recover() 防 panic。
// 重新读 session（拿最新 running_summary）+ 全部分段 → generateFinalSummary → 定向写 summary +
// summary_status=done；任一步失败置 failed 并 log。
//
// 写库用定向列更新（UpdateSessionSummary，只碰 summary+summary_status）而非全行 Save：与并发跑
// 的 runRollingSummaryFold 共存时，避免把它刚写的 running_summary 用本 goroutine 读到的旧值覆盖。
//
// recover 范围收窄：用 success 标记区分「done 已成功落库」与「真正失败」。done 写成功后置 success，
// defer recover 仅在 !success 时才 markSummaryFailed——否则一个发生在 done 落库**之后**的 panic
// 会把已成功的 done 误翻成 failed。
func (b *meetingBiz) finalizeSummaryAsync(userID uint, id uint64) {
	success := false
	defer func() {
		if rec := recover(); rec != nil {
			log.Errorw("meeting: finalize summary panicked", "session_id", id, "panic", rec)
			if !success {
				b.markSummaryFailed(id)
			}
		}
	}()

	ctx := context.Background()

	s, err := b.ds.Meetings().GetSession(ctx, id)
	if err != nil {
		log.Warnw("meeting: finalize summary load session failed", "session_id", id, "error", err)
		b.markSummaryFailed(id)
		return
	}

	segs, segErr := b.ds.Meetings().ListSegmentsBySession(ctx, id)
	if segErr != nil {
		log.Warnw("meeting: finalize summary list segments failed", "session_id", id, "error", segErr)
		b.markSummaryFailed(id)
		return
	}

	summaryMD, sumErr := b.generateFinalSummary(ctx, userID, s, segs)
	if sumErr != nil || strings.TrimSpace(summaryMD) == "" {
		log.Warnw("meeting: finalize summary generate failed", "session_id", id, "error", sumErr)
		b.markSummaryFailed(id)
		return
	}

	if err := b.ds.Meetings().UpdateSessionSummary(ctx, id, summaryMD, model.MeetingSummaryStatusDone); err != nil {
		log.Warnw("meeting: finalize summary persist failed", "session_id", id, "error", err)
		b.markSummaryFailed(id)
		return
	}
	// done 已成功落库：此后任何 panic 都不应把它翻成 failed。
	success = true
}

// markSummaryFailed 把纪要状态置 failed（后台失败收尾）。定向只更 summary_status 一列
// （MarkSummaryStatus），不读全行也不全行 Save——避免覆盖并发 fold 写的 running_summary。
// 失败仅 log。
func (b *meetingBiz) markSummaryFailed(id uint64) {
	ctx := context.Background()
	if err := b.ds.Meetings().MarkSummaryStatus(ctx, id, model.MeetingSummaryStatusFailed); err != nil {
		log.Warnw("meeting: mark summary failed persist failed", "session_id", id, "error", err)
	}
}

// ---------------------------------------------------------------------------
// 预设
// ---------------------------------------------------------------------------

// ListPresets 列出当前用户预设 + 系统内置（user_id=0）。
func (b *meetingBiz) ListPresets(ctx context.Context, userID uint) ([]PresetDTO, error) {
	list, err := b.ds.Meetings().ListPresetsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("ListPresets: %w", err)
	}
	out := make([]PresetDTO, 0, len(list))
	for i := range list {
		out = append(out, toPresetDTO(&list[i]))
	}
	return out, nil
}

// SavePreset 存当前用户预设（始终 is_builtin=0，user_id=当前用户）。
func (b *meetingBiz) SavePreset(ctx context.Context, userID uint, req *SavePresetReq) (*PresetDTO, error) {
	interval := defaultAutoIntervalSeconds
	if req.AutoIntervalSeconds != nil && *req.AutoIntervalSeconds > 0 {
		interval = clampAutoInterval(*req.AutoIntervalSeconds)
	}
	p := &model.MeetingPreset{
		UserID:              userID,
		Name:                req.Name,
		RolePrompt:          req.RolePrompt,
		AutoIntervalSeconds: interval,
		IsBuiltin:           false,
	}
	if err := b.ds.Meetings().CreatePreset(ctx, p); err != nil {
		return nil, fmt.Errorf("SavePreset: %w", err)
	}
	dto := toPresetDTO(p)
	return &dto, nil
}

// DeletePreset 删除本人非内置预设。store.DeletePreset 已带 (id,user_id,is_builtin=0) 守卫，
// RowsAffected==0 → ErrRecordNotFound（不存在 / 越权 / 内置统一映射为 404）。
func (b *meetingBiz) DeletePreset(ctx context.Context, userID uint, id uint64) error {
	err := b.ds.Meetings().DeletePreset(ctx, id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrInvalidParameter.SetMessage("预设不存在或不可删除")
		}
		return fmt.Errorf("DeletePreset: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// getOwnedSession 取会话并校验归属当前用户；不存在 → 404，越权 → 403。
func (b *meetingBiz) getOwnedSession(ctx context.Context, userID uint, id uint64) (*model.MeetingSession, error) {
	s, err := b.ds.Meetings().GetSession(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrInvalidParameter.SetMessage("会议不存在")
		}
		return nil, fmt.Errorf("getOwnedSession: %w", err)
	}
	if s.UserID != userID {
		return nil, errno.ErrForbidden.SetMessage("无权访问该会议")
	}
	return s, nil
}

// computeDurationSeconds 计算会议时长（秒）。startedAt 为空或 end 早于 start 时返回 0。
func computeDurationSeconds(startedAt *time.Time, end time.Time) int {
	if startedAt == nil {
		return 0
	}
	d := end.Sub(*startedAt)
	if d < 0 {
		return 0
	}
	return int(d.Seconds())
}

// ---------------------------------------------------------------------------
// DTO 映射
// ---------------------------------------------------------------------------

func toSessionDTO(s *model.MeetingSession) SessionDTO {
	return SessionDTO{
		ID:                  s.ID,
		Title:               s.Title,
		RolePrompt:          s.RolePrompt,
		PresetID:            s.PresetID,
		Status:              s.Status,
		AutoIntervalSeconds: s.AutoIntervalSeconds,
		DurationSeconds:     s.DurationSeconds,
		Summary:             s.Summary,
		SummaryStatus:       s.SummaryStatus,
		StartedAt:           s.StartedAt,
		EndedAt:             s.EndedAt,
		CreatedAt:           s.CreatedAt,
		UpdatedAt:           s.UpdatedAt,
	}
}

func toSegmentDTO(seg *model.MeetingSegment) SegmentDTO {
	return SegmentDTO{
		ID:              seg.ID,
		Seq:             seg.Seq,
		Text:            seg.Text,
		StartMs:         seg.StartMs,
		DurationSeconds: seg.DurationSeconds,
		AudioURL:        seg.AudioURL,
		CreatedAt:       seg.CreatedAt,
	}
}

func toSegmentDTOs(segs []model.MeetingSegment) []SegmentDTO {
	out := make([]SegmentDTO, 0, len(segs))
	for i := range segs {
		out = append(out, toSegmentDTO(&segs[i]))
	}
	return out
}

func toFeedbackDTO(fb *model.MeetingFeedback) FeedbackDTO {
	return FeedbackDTO{
		ID:        fb.ID,
		Trigger:   fb.Trigger,
		AnchorSeq: fb.AnchorSeq,
		Content:   fb.Content,
		CreatedAt: fb.CreatedAt,
	}
}

func toFeedbackDTOs(fbs []model.MeetingFeedback) []FeedbackDTO {
	out := make([]FeedbackDTO, 0, len(fbs))
	for i := range fbs {
		out = append(out, toFeedbackDTO(&fbs[i]))
	}
	return out
}

func toPresetDTO(p *model.MeetingPreset) PresetDTO {
	return PresetDTO{
		ID:                  p.ID,
		UserID:              p.UserID,
		Name:                p.Name,
		RolePrompt:          p.RolePrompt,
		AutoIntervalSeconds: p.AutoIntervalSeconds,
		IsBuiltin:           p.IsBuiltin,
		CreatedAt:           p.CreatedAt,
		UpdatedAt:           p.UpdatedAt,
	}
}
