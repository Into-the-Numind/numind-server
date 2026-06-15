package announcement

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"gorm.io/datatypes"
)

// AnswerInput 是用户提交答卷里单题答案的入参（spec §3.1 submit body）。
// controller 绑定 request 后映射进来；本层不做 gin binding。
type AnswerInput struct {
	QuestionID uint64
	Options    []string // single/multi 选中的选项值
	Rating     *int     // rating 值
	Text       *string  // text 文本
}

// SubmitSurvey 提交答卷（spec §3.1 / §5）。
//
// 流程：
//  1. 载入可见公告（不可见 → ErrAnnouncementNotFound）；必须 type=survey（否则
//     ErrAnnouncementNotSurvey）。
//  2. HasSubmitted → ErrSurveyAlreadySubmitted（一人一答）。
//  3. 载入题目，逐题校验（违规 → ErrSurveyValidation + 细节）。
//  4. 组装 survey_response + survey_answer，store.SubmitResponse（单事务，任一失败回滚）。
//  5. 成功后顺带 MarkRead（已读幂等 upsert）。
//
// ★ 顺序保证（plan T3 事务回滚）：MarkRead 仅在 SubmitResponse 成功后调用；
// SubmitResponse 报错时直接返回（包装），不触发任何已读副作用、不残留答卷。
func (b *announcementBiz) SubmitSurvey(ctx context.Context, userID uint, id uint64, answers []AnswerInput) error {
	ann, err := b.store.GetVisibleByID(ctx, id)
	if err != nil {
		return err // ErrAnnouncementNotFound 透传
	}
	if ann.Type != model.AnnouncementTypeSurvey {
		return errno.ErrAnnouncementNotSurvey
	}

	submitted, err := b.store.HasSubmitted(ctx, id, userID)
	if err != nil {
		return fmt.Errorf("SubmitSurvey: has submitted: %w", err)
	}
	if submitted {
		return errno.ErrSurveyAlreadySubmitted
	}

	questions, err := b.store.GetQuestions(ctx, id)
	if err != nil {
		return fmt.Errorf("SubmitSurvey: questions: %w", err)
	}

	modelAnswers, err := validateAndBuildAnswers(questions, answers)
	if err != nil {
		return err // ErrSurveyValidation（已附细节）
	}

	resp := &model.SurveyResponse{
		AnnouncementID: id,
		UserID:         userID,
		SubmittedAt:    time.Now(),
	}
	if err := b.store.SubmitResponse(ctx, resp, modelAnswers); err != nil {
		// 事务在 store 内回滚；不调用 MarkRead，不残留任何已读/答卷副作用。
		return fmt.Errorf("SubmitSurvey: submit: %w", err)
	}

	// 提交成功 → 顺带标记已读（spec §3.1）。失败不回滚答卷（已读是次要副作用），
	// 但仍向上报错让调用方感知。
	if err := b.store.MarkRead(ctx, id, userID); err != nil {
		return fmt.Errorf("SubmitSurvey: mark read: %w", err)
	}
	return nil
}

// validateAndBuildAnswers 按题型校验答案并构造 model.SurveyAnswer 列表（spec §5）。
//
// 校验规则（违规返回 ErrSurveyValidation + 细节）：
//   - 答案 question_id 必须属于本问卷（未知 id → 拒绝）。
//   - 每个 required 题必须被答（single 恰 1 选项；multi ≥1；rating 有值；text 非空）。
//   - single：恰 1 选项且 ∈ 题目 options。
//   - multi：≥1 选项、全部 ∈ options、无重复。
//   - rating：整数 ∈ [1, rating_max]。
//   - text：trim 后写入；required 时非空。
//   - 非 required 未答 → 允许（不产生 survey_answer 行）。
func validateAndBuildAnswers(questions []model.SurveyQuestion, answers []AnswerInput) ([]model.SurveyAnswer, error) {
	// 题目索引 + 解析 options。
	qByID := make(map[uint64]*model.SurveyQuestion, len(questions))
	optByQ := make(map[uint64]map[string]struct{}, len(questions))
	for i := range questions {
		q := &questions[i]
		qByID[q.ID] = q
		if q.QuestionType == model.SurveyQuestionTypeSingle || q.QuestionType == model.SurveyQuestionTypeMulti {
			opts, err := decodeOptionSlice(q.Options)
			if err != nil {
				return nil, errno.ErrSurveyValidation.SetMessage("题目 %d 选项解析失败", q.ID)
			}
			set := make(map[string]struct{}, len(opts))
			for _, o := range opts {
				set[o] = struct{}{}
			}
			optByQ[q.ID] = set
		}
	}

	// 收集本次提交对各题的答案（同题多次提交 → 拒绝，避免歧义）。
	ansByQ := make(map[uint64]*AnswerInput, len(answers))
	for i := range answers {
		a := &answers[i]
		if _, ok := qByID[a.QuestionID]; !ok {
			return nil, errno.ErrSurveyValidation.SetMessage("答案引用了不属于本问卷的题目 %d", a.QuestionID)
		}
		if _, dup := ansByQ[a.QuestionID]; dup {
			return nil, errno.ErrSurveyValidation.SetMessage("题目 %d 出现多个答案", a.QuestionID)
		}
		ansByQ[a.QuestionID] = a
	}

	out := make([]model.SurveyAnswer, 0, len(questions))
	for i := range questions {
		q := &questions[i]
		a := ansByQ[q.ID] // 可能为 nil（该题未答）

		row, answered, err := validateQuestionAnswer(q, a, optByQ[q.ID])
		if err != nil {
			return nil, err
		}
		if q.Required && !answered {
			return nil, errno.ErrSurveyValidation.SetMessage("必答题「%s」未作答", q.Title)
		}
		if answered {
			row.QuestionID = q.ID
			out = append(out, row)
		}
	}
	return out, nil
}

// validateQuestionAnswer 校验单题答案并构造该行（不含 ResponseID/QuestionID）。
// 返回 (row, answered, err)：answered=false 表示该题未作答（非 required 时允许）。
func validateQuestionAnswer(q *model.SurveyQuestion, a *AnswerInput, optSet map[string]struct{}) (model.SurveyAnswer, bool, error) {
	var row model.SurveyAnswer

	switch q.QuestionType {
	case model.SurveyQuestionTypeSingle:
		if a == nil || len(a.Options) == 0 {
			return row, false, nil // 未答
		}
		if len(a.Options) != 1 {
			return row, false, errno.ErrSurveyValidation.SetMessage("单选题「%s」必须且只能选 1 项", q.Title)
		}
		if _, ok := optSet[a.Options[0]]; !ok {
			return row, false, errno.ErrSurveyValidation.SetMessage("单选题「%s」的选项 %q 不在可选范围内", q.Title, a.Options[0])
		}
		row.AnswerOptions = encodeOptions(a.Options)
		return row, true, nil

	case model.SurveyQuestionTypeMulti:
		if a == nil || len(a.Options) == 0 {
			return row, false, nil // 未答
		}
		seen := make(map[string]struct{}, len(a.Options))
		for _, opt := range a.Options {
			if _, dup := seen[opt]; dup {
				return row, false, errno.ErrSurveyValidation.SetMessage("多选题「%s」存在重复选项 %q", q.Title, opt)
			}
			seen[opt] = struct{}{}
			if _, ok := optSet[opt]; !ok {
				return row, false, errno.ErrSurveyValidation.SetMessage("多选题「%s」的选项 %q 不在可选范围内", q.Title, opt)
			}
		}
		row.AnswerOptions = encodeOptions(a.Options)
		return row, true, nil

	case model.SurveyQuestionTypeRating:
		if a == nil || a.Rating == nil {
			return row, false, nil // 未答
		}
		max := 0
		if q.RatingMax != nil {
			max = *q.RatingMax
		}
		v := *a.Rating
		if v < 1 || v > max {
			return row, false, errno.ErrSurveyValidation.SetMessage("评分题「%s」的分值 %d 超出范围 [1, %d]", q.Title, v, max)
		}
		rv := v
		row.AnswerRating = &rv
		return row, true, nil

	case model.SurveyQuestionTypeText:
		if a == nil || a.Text == nil {
			return row, false, nil // 未答
		}
		trimmed := strings.TrimSpace(*a.Text)
		if trimmed == "" {
			// required 题在上层会因 answered=false 报"未作答"；非 required 视为未答。
			return row, false, nil
		}
		t := trimmed
		row.AnswerText = &t
		return row, true, nil

	default:
		return row, false, errno.ErrSurveyValidation.SetMessage("题目「%s」题型 %q 非法", q.Title, q.QuestionType)
	}
}

// ============================================================================
// 题目构造 + JSON 编解码 helper（announcement.go / survey.go 共用）
// ============================================================================

// buildQuestions 校验 admin 创建/编辑题目入参并构造 model.SurveyQuestion 列表（spec §3.2）。
//
// 规则：
//   - plain 公告不应携带题目（携带 → 拒绝）。
//   - survey 必须 ≥1 题。
//   - single/multi：≥2 个 options（spec），text 无 options。
//   - rating：rating_max ∈ [2,10]，rating_style ∈ {star,nps}。
//   - Required *bool：nil → true（默认必答）；非 nil → 显式值（false 经 store fixup 落库）。
func buildQuestions(annType string, in []QuestionInput) ([]model.SurveyQuestion, error) {
	if annType != model.AnnouncementTypeSurvey {
		if len(in) > 0 {
			return nil, errno.ErrSurveyValidation.SetMessage("非问卷类型不能包含题目")
		}
		return nil, nil
	}
	if len(in) == 0 {
		return nil, errno.ErrSurveyValidation.SetMessage("问卷至少需要 1 道题目")
	}

	out := make([]model.SurveyQuestion, 0, len(in))
	for idx, q := range in {
		if q.Title == "" {
			return nil, errno.ErrSurveyValidation.SetMessage("第 %d 题题干不能为空", idx+1)
		}
		mq := model.SurveyQuestion{
			OrderIndex:   q.OrderIndex,
			QuestionType: q.QuestionType,
			Title:        q.Title,
			Required:     q.Required == nil || *q.Required, // nil → true
		}

		switch q.QuestionType {
		case model.SurveyQuestionTypeSingle, model.SurveyQuestionTypeMulti:
			if len(q.Options) < 2 {
				return nil, errno.ErrSurveyValidation.SetMessage("第 %d 题（单/多选）至少需要 2 个选项", idx+1)
			}
			// 选项去重 + 非空校验。
			seen := make(map[string]struct{}, len(q.Options))
			for _, o := range q.Options {
				if strings.TrimSpace(o) == "" {
					return nil, errno.ErrSurveyValidation.SetMessage("第 %d 题存在空选项", idx+1)
				}
				if _, dup := seen[o]; dup {
					return nil, errno.ErrSurveyValidation.SetMessage("第 %d 题存在重复选项 %q", idx+1, o)
				}
				seen[o] = struct{}{}
			}
			mq.Options = encodeOptions(q.Options)

		case model.SurveyQuestionTypeRating:
			if q.RatingMax == nil || *q.RatingMax < 2 || *q.RatingMax > 10 {
				return nil, errno.ErrSurveyValidation.SetMessage("第 %d 题（评分）rating_max 必须在 [2,10]", idx+1)
			}
			if q.RatingStyle == nil ||
				(*q.RatingStyle != model.SurveyRatingStyleStar && *q.RatingStyle != model.SurveyRatingStyleNPS) {
				return nil, errno.ErrSurveyValidation.SetMessage("第 %d 题（评分）rating_style 必须为 star 或 nps", idx+1)
			}
			rm := *q.RatingMax
			rs := *q.RatingStyle
			mq.RatingMax = &rm
			mq.RatingStyle = &rs

		case model.SurveyQuestionTypeText:
			if len(q.Options) > 0 {
				return nil, errno.ErrSurveyValidation.SetMessage("第 %d 题（文本）不应包含选项", idx+1)
			}

		default:
			return nil, errno.ErrSurveyValidation.SetMessage("第 %d 题题型 %q 非法（须为 single/multi/rating/text）", idx+1, q.QuestionType)
		}

		out = append(out, mq)
	}
	return out, nil
}

// mapQuestions 把 store 题目映射为对外 QuestionDTO（spec §3.1/§3.2）。
// Options 解析为 []string（无选项 → nil → JSON null）。
func mapQuestions(qs []model.SurveyQuestion) ([]QuestionDTO, error) {
	out := make([]QuestionDTO, 0, len(qs))
	for i := range qs {
		q := &qs[i]
		opts, err := decodeOptions(q.Options)
		if err != nil {
			return nil, fmt.Errorf("mapQuestions: question %d options: %w", q.ID, err)
		}
		out = append(out, QuestionDTO{
			ID:           q.ID,
			OrderIndex:   q.OrderIndex,
			QuestionType: q.QuestionType,
			Title:        q.Title,
			Required:     q.Required,
			Options:      opts,
			RatingMax:    q.RatingMax,
			RatingStyle:  q.RatingStyle,
		})
	}
	return out, nil
}

// encodeOptions 把 []string 编码为 datatypes.JSON。
func encodeOptions(opts []string) datatypes.JSON {
	b, _ := json.Marshal(opts) // []string 永远可序列化
	return datatypes.JSON(b)
}

// decodeOptions 把 datatypes.JSON 解码为 interface{}（[]string 或 nil）。
// 用于 DTO 的 options 字段：nil/空/JSON null → nil（→ JSON null），否则 []string。
func decodeOptions(raw datatypes.JSON) (interface{}, error) {
	opts, err := decodeOptionSlice(raw)
	if err != nil {
		return nil, err
	}
	if opts == nil {
		return nil, nil
	}
	return opts, nil
}

// decodeOptionSlice 把 datatypes.JSON 解码为 []string（nil/空/JSON null → nil）。
func decodeOptionSlice(raw datatypes.JSON) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var opts []string
	if err := json.Unmarshal(raw, &opts); err != nil {
		return nil, fmt.Errorf("decodeOptionSlice: %w", err)
	}
	return opts, nil
}
