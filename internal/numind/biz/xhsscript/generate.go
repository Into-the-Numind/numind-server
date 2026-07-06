package xhsscript

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	aimw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

var xhsScriptChatFn = aiservice.Chat

func (s *Service) GenerateScript(ctx context.Context, userID uint, noteID uint64) (*NoteDTO, error) {
	baseProps := map[string]interface{}{
		"note_id": noteID,
	}
	fail := func(stage string, err error) (*NoteDTO, error) {
		s.recordGenerationFail(ctx, userID, baseProps, stage, err)
		return nil, err
	}
	internalFail := func(stage, safeMessage string, err error) (*NoteDTO, error) {
		_ = s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateFailed, "generation_commit_failed")
		s.recordGenerationFail(ctx, userID, baseProps, stage, err)
		return nil, errno.ErrInternalServer.SetMessage("%s", safeMessage)
	}

	note, err := s.ds.XhsScript().GetNote(ctx, userID, noteID)
	if err != nil {
		return fail("load_note", errno.ErrXhsScriptNoteNotFound)
	}
	baseProps = generationNoteProperties(note)
	if err := ensureNoteReadyForGeneration(note); err != nil {
		return fail("transcript_not_ready", err)
	}

	account, err := s.ds.XhsScript().CreateOrGetQuotaAccount(ctx, userID)
	if err != nil {
		return internalFail("quota_account", "生成失败，请稍后重试", err)
	}
	if account.FreeRemaining+account.PaidRemaining <= 0 {
		_ = s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateFailed, "quota_insufficient")
		return fail("quota_insufficient", errno.ErrXhsScriptQuotaInsufficient)
	}

	userProfile, err := s.ds.XhsScript().GetOrCreateProfileByUser(ctx, userID)
	if err != nil {
		return internalFail("profile_load", "生成失败，请稍后重试", err)
	}
	if strings.TrimSpace(userProfile.ProfileText) == "" {
		return fail("profile_required", errno.ErrXhsScriptProfileRequired)
	}

	if err := s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateGenerating, ""); err != nil {
		return internalFail("mark_generating", "生成失败，请稍后重试", err)
	}

	aiCtx := aimw.WithUserID(ctx, userID)
	aiCtx = aiservice.WithSkipLegacyBilling(aiCtx)
	resp, err := xhsScriptChatFn(aiCtx, profile.XhsNoteAnalyze, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role:    aiservice.MessageRoleSystem,
				Content: aiservice.MessageContent{Text: "你是资深小红书短视频口播编导，擅长拆解爆款视频的开场钩子、情绪推进、信息密度和成交转化，并把结构迁移到新的产品/服务上。你的输出必须是原创口播稿，不能照抄原文表达。"},
			},
			{
				Role:    aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{Text: buildGenerationPrompt(userProfile.ProfileText, note)},
			},
		},
		Temperature: 0.75,
		MaxTokens:   2200,
	})
	if err != nil {
		s.recordGenerationFail(ctx, userID, baseProps, "chat", err)
		_ = s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateFailed, "generation_failed")
		return nil, errno.ErrInternalServer.SetMessage("生成失败，请稍后重试")
	}
	script := strings.TrimSpace(resp.Content)
	if script == "" {
		_ = s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateFailed, "generation_empty")
		return fail("empty_output", errno.ErrInternalServer.SetMessage("生成结果为空，请稍后重试"))
	}

	commit, err := s.ds.XhsScript().CreateGenerationAndDeductQuota(ctx, userID, noteID, script, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	if err != nil {
		if errors.Is(err, store.ErrXhsScriptQuotaInsufficient) {
			_ = s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateFailed, "quota_insufficient")
			return fail("quota_deduct", errno.ErrXhsScriptQuotaInsufficient)
		}
		_ = s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateFailed, "generation_commit_failed")
		s.recordGenerationFail(ctx, userID, baseProps, "commit_generation", err)
		return nil, errno.ErrInternalServer.SetMessage("生成保存失败，请稍后重试")
	}
	generation := commit.Generation
	successProps := mergeAnalyticsProperties(baseProps, map[string]interface{}{
		"generation_id":      generation.ID,
		"prompt_tokens":      resp.Usage.PromptTokens,
		"completion_tokens":  resp.Usage.CompletionTokens,
		"transcript_length":  noteTranscriptLength(note),
		"script_length":      textLength(script),
		"deducted_quantity":  1,
		"deducted_bucket":    commit.Bucket,
		"free_before":        commit.FreeBefore,
		"paid_before":        commit.PaidBefore,
		"generation_version": generation.Version,
	})
	s.RecordEventBestEffort(ctx, userID, "quota_deducted", successProps)
	s.RecordEventBestEffort(ctx, userID, "generation_success", successProps)
	return s.GetNoteDTO(ctx, userID, noteID)
}

func generationNoteProperties(note *model.XhsScriptNote) map[string]interface{} {
	return map[string]interface{}{
		"note_id":           note.ID,
		"source_note_id":    note.SourceNoteID,
		"transcribe_status": note.TranscribeStatus,
		"generate_status":   note.GenerateStatus,
	}
}

func noteTranscriptLength(note *model.XhsScriptNote) int {
	if note == nil || note.VideoTranscript == nil {
		return 0
	}
	return textLength(*note.VideoTranscript)
}

func (s *Service) recordGenerationFail(ctx context.Context, userID uint, baseProps map[string]interface{}, stage string, err error) {
	s.RecordEventBestEffort(ctx, userID, "generation_fail", mergeAnalyticsProperties(baseProps, map[string]interface{}{
		"stage":          stage,
		"error_category": analyticsErrorCategory(err),
	}))
}

func buildGenerationPrompt(profileText string, note *model.XhsScriptNote) string {
	transcript := ""
	if note.VideoTranscript != nil {
		transcript = *note.VideoTranscript
	}
	comments := commentsFromJSON(note.HotComments)
	var commentLines []string
	for _, c := range comments {
		line := strings.TrimSpace(c.Content)
		if line != "" {
			commentLines = append(commentLines, "- "+limitForPrompt(line, 80))
		}
		if len(commentLines) >= 8 {
			break
		}
	}
	hotComments := "无"
	if len(commentLines) > 0 {
		hotComments = strings.Join(commentLines, "\n")
	}

	return fmt.Sprintf(`请基于下面这条小红书视频笔记，生成一篇适合“我的产品/服务”的原创口播稿。

【我的产品/服务定位】
%s

【参考爆款信息】
标题：%s
描述：%s
数据：点赞 %d，收藏 %d，评论 %d
高赞评论：
%s

【视频转写】
%s

【创作要求】
1. 先在心里拆解参考视频的结构：开头钩子、痛点、反差、案例/证据、行动号召，但不要把拆解过程写出来。
2. 输出一篇可直接照着念的中文口播稿，语气自然、有短句、有停顿感，适合 45-90 秒短视频。
3. 必须迁移结构和情绪节奏，不要照搬原视频的具体句子、品牌名、人名或不可验证承诺。
4. 如果我的产品/服务信息不足，用更稳妥的表达，不要编造具体资质、价格、疗效、收益数字。
5. 最终只输出口播稿正文，不要标题、不要 Markdown、不要解释。`,
		limitForPrompt(profileText, 3000),
		limitForPrompt(note.Title, 300),
		limitForPrompt(note.Description, 1000),
		note.LikeCount,
		note.CollectCount,
		note.CommentCount,
		hotComments,
		limitForPrompt(transcript, 8000),
	)
}
