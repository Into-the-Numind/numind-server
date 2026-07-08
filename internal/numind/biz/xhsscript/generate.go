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

const xhsScriptSystemPrompt = `你是小红书视频口播稿的「爆款结构解剖 + 产品转译」编剧。你擅长把参考视频的叙事骨架、心理触发、情绪逻辑、过渡方式、产品承接和口播节奏拆出来，再迁移到用户自己的产品/服务上。

你的任务不是照抄原文，不是简单替换关键词，不是复用原文句式、案例、名人、数据、夸张表达和行业场景，而是保留爆款的结构功能、情绪功能和转化功能，重写出原创、可信、可直接拍摄的中文口播稿。

最终只按固定格式输出标题、描述、标签和口播文稿，不输出分析、拆解、Markdown、原文引用、小节标签或任何过程说明。`

func (s *Service) GenerateScript(ctx context.Context, userID uint, noteID uint64) (*NoteDTO, error) {
	baseProps := map[string]interface{}{
		"note_id": noteID,
	}
	fail := func(stage string, err error) (*NoteDTO, error) {
		s.recordGenerationFail(ctx, userID, baseProps, stage, err)
		return nil, err
	}
	internalFail := func(stage, safeMessage string, err error) (*NoteDTO, error) {
		_ = s.ds.XhsScript().UpdateGenerateStatus(ctx, userID, noteID, model.XhsScriptGenerateFailed, generationFailureLastError(stage))
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
				Content: aiservice.MessageContent{Text: xhsScriptSystemPrompt},
			},
			{
				Role:    aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{Text: buildGenerationPrompt(userProfile.ProfileText, note)},
			},
		},
		Temperature: 0.75,
		MaxTokens:   2200,
		Thinking:    true,
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

func generationFailureLastError(stage string) string {
	switch stage {
	case "quota_account":
		return "quota_account_failed"
	case "profile_load":
		return "profile_load_failed"
	case "mark_generating":
		return "mark_generating_failed"
	case "commit_generation":
		return "generation_commit_failed"
	default:
		return "generation_failed"
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

	return fmt.Sprintf(`请把下方“小红书爆款视频逐字稿”转译成一篇适合“我的产品/服务”的原创口播稿。

【我的产品/服务定位】
%s

【参考爆款信息】
标题：%s
描述：%s

【视频转写】
%s

【最高优先级原则】

当以下要求发生冲突时，按这个顺序执行：

1. 真实可信，不编造；
2. 原创表达，不洗稿；
3. 产品承接自然，不硬广；
4. 情绪逻辑成立，不空喊；
5. 结构功能贴近原文；
6. 字数和段落数量尽量贴近原文。

如果原文某个结构、案例、情绪强度不适合我的产品/服务，可以保留它的功能，但必须换成更适合当前产品的表达方式。不要为了追求 1:1 仿写而牺牲可信度和自然度。

【内部工作流：必须执行，但不要输出拆解过程】

1. 爆款底层逻辑学习

你参考爆款时，不能只学习它说了什么，而要学习它为什么能让用户愿意继续看、愿意相信、愿意行动。

你需要先判断：这篇爆款表面在讲什么，真正卖的是什么底层价值。可能是安全感、确定性、掌控感、效率感、翻身感、身份跃迁、少走弯路、摆脱焦虑、被认可、变强或获得结果。

你还需要识别它击中了用户什么深层情绪：恐惧什么、渴望什么、不甘什么、焦虑什么、羡慕什么、害怕错过什么。

你要拆出它的核心命题：想要获得某个重要结果，不能只靠某种旧方式，而要具备某种关键能力、关键系统、关键认知或关键方法。

迁移到我的产品/服务时，不要直接复用原文概念，不要把原文关键词机械替换成我的产品关键词。你要为我的产品/服务重新提炼一个能支撑整篇文稿的核心概念。这个概念必须能同时连接用户痛点、产品价值和最终行动理由。

2. 爆款结构拆解

先把视频转写按自然段或每 2-3 句话划分为意群小节。

在心里提炼整体框架：开场钩子、反差/痛点、观点推进、案例/证据、转折、方法解释、结论或行动号召。

判断每个小节在全文里的角色：反共识开场、提出问题、放大焦虑、制造对立、提出核心概念、给出方法、举例证明、承上启下、收束号召等。

识别每个小节对应的痛点、痒点、爽点、叙事技巧和跨行业可复用的文案功能。

3. 产品转译映射

从“我的产品/服务定位”中提取本次创作的核心论点、目标人群、核心卖点、可信证据和行动号召。

如果产品信息充足，要优先使用产品定位里的真实信息。

如果产品信息不足，可以使用目标用户常见痛点、行业常见场景和合理经验判断，但不能编造资质、价格、案例结果、收益数字、客户细节、调查数据或不可验证承诺。

4. 结构功能对齐，而不是表层仿写

尽量保持原视频的段落功能、过渡位置、详略分布和情绪节奏，但不要机械复刻原文句式，也不要为了凑段落而硬塞内容。

每个段落都要先判断它在原文里的功能：是制造反差、放大焦虑、建立权威、提出核心概念、解释方法、证明可信度，还是引出行动号召。

新稿对应段落要尽量承担相同功能，但内容必须完全服务于我的产品/服务。

如果原文某个段落无法自然迁移，可以保留它的说服功能，重新组织为更适合当前产品的场景、观点或过渡。

严禁照搬原视频的具体句子、品牌名、人名、案例细节、数据表达或不可验证承诺。

5. 原创口播生成

生成的新文稿必须像小红书视频口播，语气自然，短句多，有停顿感，有画面感，适合直接拍摄。

开头要有明确判断或反差，3秒内抓住人，但不要标题党。

正文要围绕一个核心概念展开，不要观点散乱。分论点之间要共同支撑同一个核心命题。

产品/服务承接要自然，必须从前文观点里长出来，不能最后硬插广告。

最终用户看完后应该感觉：我不是缺一个技巧，而是缺一种关键能力、一套系统方法或一次认知升级。

【输出要求】
- 按指定格式输出，不要输出拆解过程、小节标签、Markdown、解释或任何分析。
- 【标题】要像小红书标题：短、具体、有点击欲，但不要标题党或虚假承诺。
- 【描述】是发布小红书时放在正文区的简短说明，用 2-4 句话概括核心价值，可以自然引导收藏、评论或私信。
- 【标签】输出 3-8 个小红书标签，使用 # 开头，贴合产品人群、场景和内容主题，不要堆无关热词。
- 【口播文稿】是可直接照着念的视频口播稿，语气自然，短句多，有停顿感，适合小红书视频。
- 【口播文稿】字数尽量贴近原视频，但不要为了凑字数重复表达、空喊口号或编造信息。
- 所有具体论据必须真实可信；信息不够时宁可克制，不要编造。

【最终输出格式】
【标题】
这里输出 1 个小红书标题

【描述】
这里输出小红书发布描述

【标签】
#标签1 #标签2 #标签3

【口播文稿】
这里输出完整口播文稿`,
		limitForPrompt(profileText, 3000),
		limitForPrompt(note.Title, 300),
		limitForPrompt(note.Description, 1000),
		limitForPrompt(transcript, 8000),
	)
}
