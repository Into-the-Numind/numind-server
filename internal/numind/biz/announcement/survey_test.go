package announcement

import (
	"context"
	"testing"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// surveyFake 构造一个 type=survey 的可见公告 + 给定题目集的 fakeStore。
func surveyFake(questions []model.SurveyQuestion) *fakeStore {
	return &fakeStore{
		getVisibleFn: func(_ context.Context, id uint64) (*model.Announcement, error) {
			return &model.Announcement{ID: id, Type: model.AnnouncementTypeSurvey, Status: model.AnnouncementStatusPublished}, nil
		},
		getQuestionsFn: func(_ context.Context, _ uint64) ([]model.SurveyQuestion, error) {
			return questions, nil
		},
	}
}

func TestAnnouncementBiz_SubmitSurvey_NotVisible_NotFound(t *testing.T) {
	fs := &fakeStore{
		getVisibleFn: func(_ context.Context, _ uint64) (*model.Announcement, error) {
			return nil, errno.ErrAnnouncementNotFound
		},
	}
	b := NewWithStore(fs)
	err := b.SubmitSurvey(context.Background(), 1, 99, nil)
	assert.ErrorIs(t, err, errno.ErrAnnouncementNotFound)
}

func TestAnnouncementBiz_SubmitSurvey_NotSurvey_Error(t *testing.T) {
	fs := &fakeStore{
		getVisibleFn: func(_ context.Context, id uint64) (*model.Announcement, error) {
			return &model.Announcement{ID: id, Type: model.AnnouncementTypePlain, Status: model.AnnouncementStatusPublished}, nil
		},
	}
	b := NewWithStore(fs)
	err := b.SubmitSurvey(context.Background(), 1, 1, nil)
	assert.ErrorIs(t, err, errno.ErrAnnouncementNotSurvey)
}

func TestAnnouncementBiz_SubmitSurvey_AlreadySubmitted(t *testing.T) {
	fs := surveyFake(nil)
	fs.hasSubmittedFn = func(_ context.Context, _ uint64, _ uint) (bool, error) { return true, nil }
	b := NewWithStore(fs)
	err := b.SubmitSurvey(context.Background(), 1, 1, nil)
	assert.ErrorIs(t, err, errno.ErrSurveyAlreadySubmitted)
	assert.Equal(t, 0, fs.submitCalls, "已提交不应再写答卷")
}

// ---- 校验：required 缺答 ----
func TestAnnouncementBiz_SubmitSurvey_RequiredMissing(t *testing.T) {
	qs := []model.SurveyQuestion{
		{ID: 10, QuestionType: model.SurveyQuestionTypeSingle, Title: "必答单选", Options: optsJSON("A", "B"), Required: true},
	}
	fs := surveyFake(qs)
	b := NewWithStore(fs)
	err := b.SubmitSurvey(context.Background(), 1, 1, []AnswerInput{}) // 不答
	assert.ErrorIs(t, err, errno.ErrSurveyValidation)
	assert.Equal(t, 0, fs.submitCalls)
}

// ---- 校验：single 选项数 != 1 ----
func TestAnnouncementBiz_SubmitSurvey_SingleWrongCount(t *testing.T) {
	qs := []model.SurveyQuestion{
		{ID: 10, QuestionType: model.SurveyQuestionTypeSingle, Title: "单选", Options: optsJSON("A", "B", "C"), Required: true},
	}
	fs := surveyFake(qs)
	b := NewWithStore(fs)
	err := b.SubmitSurvey(context.Background(), 1, 1, []AnswerInput{
		{QuestionID: 10, Options: []string{"A", "B"}}, // 选了 2 个
	})
	assert.ErrorIs(t, err, errno.ErrSurveyValidation)
}

// ---- 校验：multi 选项越界（不在选项集合中）----
func TestAnnouncementBiz_SubmitSurvey_MultiOptionNotInSet(t *testing.T) {
	qs := []model.SurveyQuestion{
		{ID: 10, QuestionType: model.SurveyQuestionTypeMulti, Title: "多选", Options: optsJSON("A", "B"), Required: true},
	}
	fs := surveyFake(qs)
	b := NewWithStore(fs)
	err := b.SubmitSurvey(context.Background(), 1, 1, []AnswerInput{
		{QuestionID: 10, Options: []string{"A", "Z"}}, // Z 不在集合
	})
	assert.ErrorIs(t, err, errno.ErrSurveyValidation)
}

// ---- 校验：rating 越界 [1,max] ----
func TestAnnouncementBiz_SubmitSurvey_RatingOutOfRange(t *testing.T) {
	qs := []model.SurveyQuestion{
		{ID: 10, QuestionType: model.SurveyQuestionTypeRating, Title: "评分", RatingMax: ptrInt(5), RatingStyle: ptrStr(model.SurveyRatingStyleStar), Required: true},
	}
	fs := surveyFake(qs)
	b := NewWithStore(fs)
	for _, v := range []int{0, 6} {
		err := b.SubmitSurvey(context.Background(), 1, 1, []AnswerInput{
			{QuestionID: 10, Rating: ptrInt(v)},
		})
		assert.ErrorIs(t, err, errno.ErrSurveyValidation, "rating=%d should fail", v)
	}
}

// ---- 校验：required text 空 ----
func TestAnnouncementBiz_SubmitSurvey_TextEmptyWhenRequired(t *testing.T) {
	qs := []model.SurveyQuestion{
		{ID: 10, QuestionType: model.SurveyQuestionTypeText, Title: "必答文本", Required: true},
	}
	fs := surveyFake(qs)
	b := NewWithStore(fs)
	err := b.SubmitSurvey(context.Background(), 1, 1, []AnswerInput{
		{QuestionID: 10, Text: ptrStr("   ")}, // 全空白
	})
	assert.ErrorIs(t, err, errno.ErrSurveyValidation)
}

// ---- 校验：未知 question_id ----
func TestAnnouncementBiz_SubmitSurvey_UnknownQuestionID(t *testing.T) {
	qs := []model.SurveyQuestion{
		{ID: 10, QuestionType: model.SurveyQuestionTypeText, Title: "T", Required: false},
	}
	fs := surveyFake(qs)
	b := NewWithStore(fs)
	err := b.SubmitSurvey(context.Background(), 1, 1, []AnswerInput{
		{QuestionID: 999, Text: ptrStr("hi")}, // 该题不属于本问卷
	})
	assert.ErrorIs(t, err, errno.ErrSurveyValidation)
}

// ---- happy path：合法提交 → SubmitResponse + MarkRead 都被调用 ----
func TestAnnouncementBiz_SubmitSurvey_Success_MarksReadAfter(t *testing.T) {
	qs := []model.SurveyQuestion{
		{ID: 10, QuestionType: model.SurveyQuestionTypeSingle, Title: "单选", Options: optsJSON("A", "B"), Required: true},
		{ID: 11, QuestionType: model.SurveyQuestionTypeText, Title: "可选文本", Required: false},
	}
	var capturedAnswers []model.SurveyAnswer
	fs := surveyFake(qs)
	fs.submitFn = func(_ context.Context, resp *model.SurveyResponse, answers []model.SurveyAnswer) error {
		capturedAnswers = answers
		assert.Equal(t, uint(7), resp.UserID)
		return nil
	}
	b := NewWithStore(fs)
	err := b.SubmitSurvey(context.Background(), 7, 1, []AnswerInput{
		{QuestionID: 10, Options: []string{"A"}},
		// 题 11 非 required，不答 → 允许，不产生 answer 行
	})
	require.NoError(t, err)
	assert.Equal(t, 1, fs.submitCalls)
	assert.Equal(t, 1, fs.markReadCalls, "成功提交后须 MarkRead")
	require.Len(t, capturedAnswers, 1, "非 required 未答题不产生 answer 行")
	assert.Equal(t, uint64(10), capturedAnswers[0].QuestionID)
}

// ============================================================================
// 事务回滚（plan T3）：SubmitResponse 报错 → 错误向上包装 + 不调 MarkRead
// ============================================================================

func TestAnnouncementBiz_SubmitSurvey_TransactionRollback_NoMarkRead(t *testing.T) {
	qs := []model.SurveyQuestion{
		{ID: 10, QuestionType: model.SurveyQuestionTypeSingle, Title: "单选", Options: optsJSON("A", "B"), Required: true},
	}
	fs := surveyFake(qs)
	fs.submitFn = func(_ context.Context, _ *model.SurveyResponse, _ []model.SurveyAnswer) error {
		// 模拟 store 事务内（如第 3 条 answer insert）报错并回滚 → response 不被创建
		return errStoreBoom
	}
	b := NewWithStore(fs)
	err := b.SubmitSurvey(context.Background(), 7, 1, []AnswerInput{
		{QuestionID: 10, Options: []string{"A"}},
	})

	// 错误被包装向上传播（errors.Is 透到底层 sentinel）
	require.Error(t, err)
	assert.ErrorIs(t, err, errStoreBoom)

	// SubmitResponse 被调用过（1 次），但因失败回滚，MarkRead 绝不能被调用
	assert.Equal(t, 1, fs.submitCalls)
	assert.Equal(t, 0, fs.markReadCalls, "答卷事务回滚后不应有 MarkRead 副作用")
}
