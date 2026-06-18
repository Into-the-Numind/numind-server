// Package meeting 是会议副驾 (Meeting Copilot) 的 HTTP 控制层（meeting-copilot feature）。
//
// 职责边界（controller 硬规则，见 .claude/rules/api-design.md §6）：本层只做参数绑定、
// 鉴权上下文提取、调用 biz、core.WriteResponse 格式化。业务逻辑全在 biz/meeting 层。
// 所有接口校验 session 归属由 biz 层的 getOwnedSession 保证（不存在→404，越权→403）。
package meeting

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/gin-gonic/gin"

	meetingbiz "numind-server/internal/numind/biz/meeting"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// maxSegmentAudioSize 单段音频上传大小上限（~10s 16kHz/mono/16-bit WAV 约 320KB，留足余量）。
const maxSegmentAudioSize = 20 * 1024 * 1024 // 20MB

// Controller 是会议副驾用户端控制器。
type Controller struct {
	biz meetingbiz.IMeetingBiz
}

// NewController 创建会议副驾控制器（沿用 NewXxxController(biz) 模式）。
func NewController(biz meetingbiz.IMeetingBiz) *Controller {
	return &Controller{biz: biz}
}

// currentUserID 从 gin 上下文提取当前用户 ID。
//
// 说明（SPEC §3 写「c.GetUint("userID")」的偏离）：本仓库 AuthMiddleware 只 Set
// "current_user"（*model.User），并未 Set "userID" gin key，故 c.GetUint("userID") 恒为 0。
// 这里遵循本仓库既有 controller 统一约定 middleware.GetCurrentUser(c).ID，效果一致且正确。
func currentUserID(c *gin.Context) (uint, bool) {
	u := middleware.GetCurrentUser(c)
	if u == nil {
		return 0, false
	}
	return u.ID, true
}

// parseSessionID 解析路径参数 :id（会话 ID）。
func parseSessionID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// ---------------------------------------------------------------------------
// 会话生命周期
// ---------------------------------------------------------------------------

// CreateSession 创建会话。POST /v1/meetings
func (ctl *Controller) CreateSession(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req meetingbiz.CreateSessionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	dto, err := ctl.biz.CreateSession(c.Request.Context(), userID, &req)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, dto)
}

// ListSessions 分页列出当前用户会话。GET /v1/meetings?page=&page_size=
func (ctl *Controller) ListSessions(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	page, pageSize := parsePagination(c)
	offset := (page - 1) * pageSize

	list, total, err := ctl.biz.ListSessions(c.Request.Context(), userID, offset, pageSize)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// GetSession 会话详情（含 segments + feedbacks）。GET /v1/meetings/:id
func (ctl *Controller) GetSession(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	id, ok := parseSessionID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	detail, err := ctl.biz.GetSession(c.Request.Context(), userID, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, detail)
}

// EndSession 结束会话。POST /v1/meetings/:id/end
// 可选 body {generate_summary: bool}：缺省/无 body 视为 true（向后兼容、默认生成 AI 纪要）；
// 传 false 则只结束、不生成纪要（summary_status=skipped）。
func (ctl *Controller) EndSession(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	id, ok := parseSessionID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	// 缺省 true（向后兼容）。空 body / 解析失败 → 指针为 nil → 保持默认 true。
	generateSummary := true
	var req struct {
		GenerateSummary *bool `json:"generate_summary"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.GenerateSummary != nil {
		generateSummary = *req.GenerateSummary
	}

	dto, err := ctl.biz.EndSession(c.Request.Context(), userID, id, generateSummary)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{"session": dto})
}

// ---------------------------------------------------------------------------
// 分段转写
// ---------------------------------------------------------------------------

// IngestSegment 分段近实时转写。POST /v1/meetings/:id/segments
//
// multipart/form-data：audio(文件, wav) + seq(int, 可选) + start_ms(int, 可选)。
func (ctl *Controller) IngestSegment(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	id, ok := parseSessionID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	file, header, err := c.Request.FormFile("audio")
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("缺少 audio 音频文件"), nil)
		return
	}
	defer file.Close()

	if header.Size > maxSegmentAudioSize {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("音频段过大（上限 %dMB）", maxSegmentAudioSize/1024/1024), nil)
		return
	}

	audioBytes, err := io.ReadAll(io.LimitReader(file, maxSegmentAudioSize+1))
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("读取音频数据失败"), nil)
		return
	}

	// seq / start_ms 可选；非法值降级为 0（biz 层 seq<=0 时按 max(seq)+1 自增）。
	seq, _ := strconv.Atoi(c.DefaultPostForm("seq", "0"))
	startMs, _ := strconv.Atoi(c.DefaultPostForm("start_ms", "0"))

	req := &meetingbiz.IngestSegmentReq{
		AudioBytes: audioBytes,
		Seq:        seq,
		StartMs:    startMs,
	}

	dto, err := ctl.biz.IngestSegment(c.Request.Context(), userID, id, req)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{"segment": dto})
}

// ---------------------------------------------------------------------------
// 整场录音上传（实时流式路径，SPEC §3）
// ---------------------------------------------------------------------------

// maxRecordingSize 整场录音上传大小上限（webm/opus，长会议留足余量）。
const maxRecordingSize = 200 * 1024 * 1024 // 200MB

// UploadRecording 上传整场录音并回写 recording_url。POST /v1/meetings/:id/recording
//
// multipart/form-data：audio(文件, webm/opus)。流式路径不再逐段存音频，用户结束时一次性上传整段
// blob 供会后回放（SPEC §3）。
func (ctl *Controller) UploadRecording(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	id, ok := parseSessionID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	file, header, err := c.Request.FormFile("audio")
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("缺少 audio 录音文件"), nil)
		return
	}
	defer file.Close()

	if header.Size > maxRecordingSize {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("录音过大（上限 %dMB）", maxRecordingSize/1024/1024), nil)
		return
	}

	audioBytes, err := io.ReadAll(io.LimitReader(file, maxRecordingSize+1))
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("读取录音数据失败"), nil)
		return
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "audio/webm"
	}

	url, err := ctl.biz.UpdateRecordingURL(c.Request.Context(), userID, id, audioBytes, contentType)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{"recording_url": url})
}

// ---------------------------------------------------------------------------
// 反馈（SSE）
// ---------------------------------------------------------------------------

// GenerateFeedback 生成一次反馈（SSE 流式）。POST /v1/meetings/:id/feedback
//
// SSE 协议见 SPEC §3.1：响应头 text/event-stream + no-cache + X-Accel-Buffering:no；
// 帧 `data: {"type":...,"data":...}\n\n` 每帧 flush；事件 token / skip / done / error。
func (ctl *Controller) GenerateFeedback(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	id, ok := parseSessionID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	var req meetingbiz.FeedbackReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	// SSE 响应头（与 salesrag / sop 一致）。
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // 禁用 nginx 缓冲

	w := c.Writer

	// sseHandler 把 biz 推出的 (eventType, data) 序列化为 SSE 帧并 flush。
	// biz 端约定事件类型：token / skip / done / error（SPEC §3.1）。
	sseHandler := func(eventType string, data interface{}) error {
		frame, marshalErr := json.Marshal(map[string]interface{}{
			"type": eventType,
			"data": data,
		})
		if marshalErr != nil {
			return marshalErr
		}
		if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", frame); writeErr != nil {
			return writeErr
		}
		w.Flush()
		return nil
	}

	err := ctl.biz.GenerateFeedback(c.Request.Context(), userID, id, &req, sseHandler)
	if err != nil {
		// 客户端断开：handler 返回 write error，不再写帧、不记业务错误（与 salesrag/sop 一致）。
		if c.Request.Context().Err() != nil {
			log.C(c).Infow("Client disconnected during meeting feedback stream", "error", err)
			return
		}
		// 业务/系统错误：biz 已在 SSE 流内尽力发过 error 事件；这里兜底再发一帧，
		// 保证即使 biz 在拿到 stream 前失败（如归属校验失败）前端也能收到 error。
		errData, _ := json.Marshal(map[string]interface{}{
			"type": "error",
			"data": map[string]string{"message": "反馈生成失败"},
		})
		fmt.Fprintf(w, "data: %s\n\n", errData)
		w.Flush()
	}
}

// ---------------------------------------------------------------------------
// 预设
// ---------------------------------------------------------------------------

// ListPresets 当前用户预设 + 内置（user_id=0）。GET /v1/meetings/presets
func (ctl *Controller) ListPresets(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	list, err := ctl.biz.ListPresets(c.Request.Context(), userID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{"list": list})
}

// SavePreset 存当前用户预设。POST /v1/meetings/presets
func (ctl *Controller) SavePreset(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req meetingbiz.SavePresetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	dto, err := ctl.biz.SavePreset(c.Request.Context(), userID, &req)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, dto)
}

// DeletePreset 删除本人非内置预设。DELETE /v1/meetings/presets/:id
func (ctl *Controller) DeletePreset(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	if err := ctl.biz.DeletePreset(c.Request.Context(), userID, id); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// parsePagination 解析分页参数（api-design.md §4：page 1-based 默认 1；page_size 默认 20 上限 100）。
func parsePagination(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ = strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}
