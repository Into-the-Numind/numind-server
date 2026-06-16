package meeting

import (
	"context"
	"fmt"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// asrFn 是 ASR 调用的注入点（生产用 aiservice.ASR；单测可替换以避免外部依赖）。
var asrFn = aiservice.ASR

// IngestSegment 处理一段近实时音频（SPEC §1 分段近实时 / §3 POST /:id/segments）：
//
//  1. 校验会话归属（仅 active 会话接收分段）。
//  2. 确定 seq（前端给定优先；否则后端 max(seq)+1 自增）。
//  3. 上传 wav 到 COS（key meeting-recordings/<userID>/<sessionID>/<seq>.wav）—— 录音回放用。
//  4. aiservice.ASR（FunASR，整段音频→整段文本，传 AudioBytes/format=wav）—— 内部不扣费。
//  5. 写 meeting_segment（**空转写也落库**，保留时间轴）。
//
// 计费纪律：ASR 走 aiservice 统一入口（UsageRecord 自动记录），但用 internalCallCtx
// 剥离扣费/会员门（ASR 非 ChatRequest，网关 ContextBudgetCredits 本就直通；此处统一处理
// 以防未来路由变更引入扣减）。
func (b *meetingBiz) IngestSegment(ctx context.Context, userID uint, sessionID uint64, req *IngestSegmentReq) (*SegmentDTO, error) {
	s, err := b.getOwnedSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	if s.Status != model.MeetingSessionStatusActive {
		return nil, errno.ErrInvalidParameter.SetMessage("会议已结束，无法继续上传转写分段")
	}
	if len(req.AudioBytes) == 0 {
		return nil, errno.ErrInvalidParameter.SetMessage("音频数据不能为空")
	}

	// --- seq 解析 ---
	seq := req.Seq
	if seq <= 0 {
		maxSeq, err := b.ds.Meetings().GetMaxSegmentSeq(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("IngestSegment: max seq: %w", err)
		}
		seq = maxSeq + 1
	}

	// --- 上传音频到 COS（best-effort：COS 未启用返回空 URL，不阻断转写） ---
	objectKey := fmt.Sprintf("meeting-recordings/%d/%d/%d.wav", userID, sessionID, seq)
	audioURL, uploadErr := util.UploadBytesToCOS(ctx, objectKey, "audio/wav", req.AudioBytes)
	if uploadErr != nil {
		// 上传失败不阻断转写（录音回放降级）；记 warn。
		log.C(ctx).Warnw("meeting: segment audio upload failed",
			"session_id", sessionID, "seq", seq, "error", uploadErr)
		audioURL = ""
	}

	// --- ASR 转写（内部不扣费） ---
	text, duration := b.transcribeAudio(ctx, userID, sessionID, seq, req.AudioBytes)

	// --- 落库（空转写也落，保留时间轴） ---
	seg := &model.MeetingSegment{
		SessionID:       sessionID,
		Seq:             seq,
		Text:            text,
		StartMs:         req.StartMs,
		DurationSeconds: duration,
		AudioURL:        audioURL,
	}
	if err := b.ds.Meetings().CreateSegment(ctx, seg); err != nil {
		return nil, fmt.Errorf("IngestSegment: create segment: %w", err)
	}

	dto := toSegmentDTO(seg)
	return &dto, nil
}

// transcribeAudio 调 FunASR 把一段 wav 转成文本，返回 (text, durationSeconds)。
//
// ASR 失败时降级为空文本 + 0 时长（分段仍落库，保留时间轴，符合 SPEC §3「空转写也要落库」）。
// 创建 Langfuse generation 记录调用（含 model / duration / error；优雅降级 if tc != nil）。
func (b *meetingBiz) transcribeAudio(ctx context.Context, userID uint, sessionID uint64, seq int, audio []byte) (string, float64) {
	callCtx := internalCallCtx(ctx, "meeting.transcribe")

	// 创建 Langfuse trace（SPEC §4）：internalCallCtx 不注入 trace，故此处显式创建，
	// 否则 FromContext 恒 nil、generation 永不落库。userID 用于可观测归属（与 billing
	// 的 userID=0 隔离，仅标注 trace owner，不触发会员门/扣费）。优雅降级：Langfuse 关闭时
	// CreateTrace 为 no-op，FromContext 仍返回 nil。
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "meeting-transcribe",
		langfuse.WithUserID(userID),
		langfuse.WithTraceTags("meeting"),
	)
	callCtx = langfuse.WithTrace(callCtx, traceID)

	tc := langfuse.FromContext(callCtx)
	var genID string
	if tc != nil {
		genID = langfuse.SpanID()
		langfuse.CreateGeneration(tc.TraceID, genID,
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("meeting-transcribe"),
			langfuse.WithGenInput(map[string]interface{}{
				"session_id": sessionID,
				"seq":        seq,
				"bytes":      len(audio),
			}),
		)
	}

	resp, err := asrFn(callCtx, profile.MonitorTranscribe, aiservice.ASRRequest{
		AudioBytes:  audio,
		AudioFormat: "wav", // SPEC §1：FunASR 仅接受 16kHz/mono/16-bit WAV
		Language:    "zh",
	})
	if err != nil {
		log.C(ctx).Warnw("meeting: ASR failed, persisting empty segment",
			"session_id", sessionID, "seq", seq, "error", err)
		if tc != nil {
			langfuse.EndGeneration(tc.TraceID, genID,
				langfuse.WithGenError(err.Error()),
			)
		}
		return "", 0
	}

	if tc != nil {
		langfuse.EndGeneration(tc.TraceID, genID,
			langfuse.WithGenModel(resp.Provider),
			langfuse.WithGenOutput(map[string]interface{}{
				"text":             resp.Text,
				"duration_seconds": resp.DurationSeconds,
			}),
		)
	}
	return resp.Text, resp.DurationSeconds
}
