package announcement

import (
	"context"
	"errors"
	"testing"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// ============================================================================
// fakeStore — 手写 fake，实现 store.IAnnouncementStore。
//
// 每个方法是一个可注入的函数字段；未注入时返回零值（或合理默认）。同时记录调用计数
// （markReadCalls / submitCalls 等）以便断言副作用顺序（plan T3 事务回滚校验）。
// ============================================================================

type fakeStore struct {
	// 用户端
	listVisibleFn  func(ctx context.Context, userID uint, offset, limit int) ([]model.Announcement, int64, error)
	getVisibleFn   func(ctx context.Context, id uint64) (*model.Announcement, error)
	countUnreadFn  func(ctx context.Context, userID uint) (int64, error)
	isReadFn       func(ctx context.Context, annID uint64, userID uint) (bool, error)
	markReadFn     func(ctx context.Context, annID uint64, userID uint) error
	getQuestionsFn func(ctx context.Context, annID uint64) ([]model.SurveyQuestion, error)
	hasSubmittedFn func(ctx context.Context, annID uint64, userID uint) (bool, error)
	submitFn       func(ctx context.Context, resp *model.SurveyResponse, answers []model.SurveyAnswer) error

	// admin CRUD
	createFn       func(ctx context.Context, ann *model.Announcement, questions []model.SurveyQuestion) error
	getByIDFn      func(ctx context.Context, id uint64) (*model.Announcement, error)
	updateFn       func(ctx context.Context, ann *model.Announcement) error
	replaceQFn     func(ctx context.Context, annID uint64, questions []model.SurveyQuestion) error
	listAllFn      func(ctx context.Context, status, annType string, offset, limit int) ([]model.Announcement, int64, error)
	updateStatusFn func(ctx context.Context, id uint64, status string, publishedAt *time.Time) error
	softDeleteFn   func(ctx context.Context, id uint64) error

	// analytics
	targetCountFn func(ctx context.Context) (int64, error)
	readCountFn   func(ctx context.Context, annID uint64) (int64, error)
	respCountFn   func(ctx context.Context, annID uint64) (int64, error)
	listReadersFn func(ctx context.Context, annID uint64, readStatus string, offset, limit int) ([]store.ReaderRow, int64, error)
	aggregateFn   func(ctx context.Context, annID uint64) ([]store.QuestionAggregate, error)
	listRespFn    func(ctx context.Context, annID uint64, offset, limit int) ([]store.ResponseRow, int64, error)

	// 调用计数 / 顺序追踪
	markReadCalls int
	submitCalls   int
}

var _ store.IAnnouncementStore = (*fakeStore)(nil)

func (f *fakeStore) ListVisible(ctx context.Context, userID uint, offset, limit int) ([]model.Announcement, int64, error) {
	if f.listVisibleFn != nil {
		return f.listVisibleFn(ctx, userID, offset, limit)
	}
	return nil, 0, nil
}

func (f *fakeStore) GetVisibleByID(ctx context.Context, id uint64) (*model.Announcement, error) {
	if f.getVisibleFn != nil {
		return f.getVisibleFn(ctx, id)
	}
	return &model.Announcement{ID: id, Type: model.AnnouncementTypePlain, Status: model.AnnouncementStatusPublished}, nil
}

func (f *fakeStore) CountUnread(ctx context.Context, userID uint) (int64, error) {
	if f.countUnreadFn != nil {
		return f.countUnreadFn(ctx, userID)
	}
	return 0, nil
}

func (f *fakeStore) IsRead(ctx context.Context, annID uint64, userID uint) (bool, error) {
	if f.isReadFn != nil {
		return f.isReadFn(ctx, annID, userID)
	}
	return false, nil
}

func (f *fakeStore) MarkRead(ctx context.Context, annID uint64, userID uint) error {
	f.markReadCalls++
	if f.markReadFn != nil {
		return f.markReadFn(ctx, annID, userID)
	}
	return nil
}

func (f *fakeStore) GetQuestions(ctx context.Context, annID uint64) ([]model.SurveyQuestion, error) {
	if f.getQuestionsFn != nil {
		return f.getQuestionsFn(ctx, annID)
	}
	return nil, nil
}

func (f *fakeStore) HasSubmitted(ctx context.Context, annID uint64, userID uint) (bool, error) {
	if f.hasSubmittedFn != nil {
		return f.hasSubmittedFn(ctx, annID, userID)
	}
	return false, nil
}

func (f *fakeStore) SubmitResponse(ctx context.Context, resp *model.SurveyResponse, answers []model.SurveyAnswer) error {
	f.submitCalls++
	if f.submitFn != nil {
		return f.submitFn(ctx, resp, answers)
	}
	return nil
}

func (f *fakeStore) Create(ctx context.Context, ann *model.Announcement, questions []model.SurveyQuestion) error {
	if f.createFn != nil {
		return f.createFn(ctx, ann, questions)
	}
	return nil
}

func (f *fakeStore) GetByID(ctx context.Context, id uint64) (*model.Announcement, error) {
	if f.getByIDFn != nil {
		return f.getByIDFn(ctx, id)
	}
	return &model.Announcement{ID: id, Type: model.AnnouncementTypePlain, Status: model.AnnouncementStatusDraft}, nil
}

func (f *fakeStore) Update(ctx context.Context, ann *model.Announcement) error {
	if f.updateFn != nil {
		return f.updateFn(ctx, ann)
	}
	return nil
}

func (f *fakeStore) ReplaceQuestions(ctx context.Context, annID uint64, questions []model.SurveyQuestion) error {
	if f.replaceQFn != nil {
		return f.replaceQFn(ctx, annID, questions)
	}
	return nil
}

func (f *fakeStore) ListAll(ctx context.Context, status, annType string, offset, limit int) ([]model.Announcement, int64, error) {
	if f.listAllFn != nil {
		return f.listAllFn(ctx, status, annType, offset, limit)
	}
	return nil, 0, nil
}

func (f *fakeStore) UpdateStatus(ctx context.Context, id uint64, status string, publishedAt *time.Time) error {
	if f.updateStatusFn != nil {
		return f.updateStatusFn(ctx, id, status, publishedAt)
	}
	return nil
}

func (f *fakeStore) SoftDelete(ctx context.Context, id uint64) error {
	if f.softDeleteFn != nil {
		return f.softDeleteFn(ctx, id)
	}
	return nil
}

func (f *fakeStore) TargetUserCount(ctx context.Context) (int64, error) {
	if f.targetCountFn != nil {
		return f.targetCountFn(ctx)
	}
	return 0, nil
}

func (f *fakeStore) ReadCount(ctx context.Context, annID uint64) (int64, error) {
	if f.readCountFn != nil {
		return f.readCountFn(ctx, annID)
	}
	return 0, nil
}

func (f *fakeStore) ResponseCount(ctx context.Context, annID uint64) (int64, error) {
	if f.respCountFn != nil {
		return f.respCountFn(ctx, annID)
	}
	return 0, nil
}

func (f *fakeStore) ListReaders(ctx context.Context, annID uint64, readStatus string, offset, limit int) ([]store.ReaderRow, int64, error) {
	if f.listReadersFn != nil {
		return f.listReadersFn(ctx, annID, readStatus, offset, limit)
	}
	return nil, 0, nil
}

func (f *fakeStore) SurveyAggregate(ctx context.Context, annID uint64) ([]store.QuestionAggregate, error) {
	if f.aggregateFn != nil {
		return f.aggregateFn(ctx, annID)
	}
	return nil, nil
}

func (f *fakeStore) ListResponses(ctx context.Context, annID uint64, offset, limit int) ([]store.ResponseRow, int64, error) {
	if f.listRespFn != nil {
		return f.listRespFn(ctx, annID, offset, limit)
	}
	return nil, 0, nil
}

// ----- helpers -----

func ptrBool(b bool) *bool    { return &b }
func ptrInt(i int) *int       { return &i }
func ptrStr(s string) *string { return &s }

func optsJSON(opts ...string) datatypes.JSON {
	return encodeOptions(opts)
}

// ============================================================================
// 用户端测试
// ============================================================================

func TestAnnouncementBiz_DetailForUser_NotVisible_NotFound(t *testing.T) {
	fs := &fakeStore{
		getVisibleFn: func(_ context.Context, _ uint64) (*model.Announcement, error) {
			return nil, errno.ErrAnnouncementNotFound
		},
	}
	b := NewWithStore(fs)
	_, err := b.DetailForUser(context.Background(), 1, 99)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrAnnouncementNotFound)
}

func TestAnnouncementBiz_MarkRead_ReturnsUnreadAndCallsStore(t *testing.T) {
	fs := &fakeStore{
		getVisibleFn: func(_ context.Context, id uint64) (*model.Announcement, error) {
			return &model.Announcement{ID: id, Type: model.AnnouncementTypePlain, Status: model.AnnouncementStatusPublished}, nil
		},
		countUnreadFn: func(_ context.Context, _ uint) (int64, error) { return 2, nil },
	}
	b := NewWithStore(fs)

	// 第一次 MarkRead
	dto, err := b.MarkRead(context.Background(), 7, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(2), dto.UnreadCount)
	assert.Equal(t, 1, fs.markReadCalls)

	// 幂等：再次调用 store.MarkRead 仍被调用且不报错（store 层 OnConflict DoNothing 兜底）
	_, err = b.MarkRead(context.Background(), 7, 10)
	require.NoError(t, err)
	assert.Equal(t, 2, fs.markReadCalls)
}

func TestAnnouncementBiz_MarkRead_NotVisible_NotFound(t *testing.T) {
	fs := &fakeStore{
		getVisibleFn: func(_ context.Context, _ uint64) (*model.Announcement, error) {
			return nil, errno.ErrAnnouncementNotFound
		},
	}
	b := NewWithStore(fs)
	_, err := b.MarkRead(context.Background(), 1, 5)
	assert.ErrorIs(t, err, errno.ErrAnnouncementNotFound)
	assert.Equal(t, 0, fs.markReadCalls, "不可见公告不应触发 MarkRead")
}

func TestAnnouncementBiz_ListForUser_AssemblesBriefs(t *testing.T) {
	pub := time.Now()
	fs := &fakeStore{
		listVisibleFn: func(_ context.Context, _ uint, _, _ int) ([]model.Announcement, int64, error) {
			return []model.Announcement{
				{ID: 1, Type: model.AnnouncementTypePlain, Title: "P", Status: model.AnnouncementStatusPublished, PublishedAt: &pub},
				{ID: 2, Type: model.AnnouncementTypeSurvey, Title: "S", Status: model.AnnouncementStatusPublished, PublishedAt: &pub},
			}, 2, nil
		},
		isReadFn: func(_ context.Context, annID uint64, _ uint) (bool, error) {
			return annID == 1, nil // 公告1已读，公告2未读
		},
		hasSubmittedFn: func(_ context.Context, annID uint64, _ uint) (bool, error) {
			return annID == 2, nil
		},
		countUnreadFn: func(_ context.Context, _ uint) (int64, error) { return 1, nil },
	}
	b := NewWithStore(fs)
	dto, err := b.ListForUser(context.Background(), 7, 1, 20)
	require.NoError(t, err)
	require.Len(t, dto.List, 2)
	assert.Equal(t, int64(2), dto.Total)
	assert.Equal(t, int64(1), dto.UnreadCount)
	assert.True(t, dto.List[0].IsRead)
	assert.False(t, dto.List[0].IsSurveySubmitted) // plain 不查 submitted
	assert.False(t, dto.List[1].IsRead)
	assert.True(t, dto.List[1].IsSurveySubmitted)
}

// ============================================================================
// Stats（速率计算边界）
// ============================================================================

func TestAnnouncementBiz_Stats_TargetZero_NoDivideByZero(t *testing.T) {
	fs := &fakeStore{
		getByIDFn: func(_ context.Context, id uint64) (*model.Announcement, error) {
			return &model.Announcement{ID: id, Type: model.AnnouncementTypeSurvey}, nil
		},
		targetCountFn: func(_ context.Context) (int64, error) { return 0, nil },
		readCountFn:   func(_ context.Context, _ uint64) (int64, error) { return 5, nil },
		respCountFn:   func(_ context.Context, _ uint64) (int64, error) { return 3, nil },
	}
	b := NewWithStore(fs)
	dto, err := b.Stats(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, float64(0), dto.ReadRate)
	assert.Equal(t, float64(0), dto.ResponseRate)
	assert.Equal(t, int64(0), dto.TargetCount)
	assert.Equal(t, int64(5), dto.ReadCount)
	assert.Equal(t, int64(3), dto.ResponseCount)
}

func TestAnnouncementBiz_Stats_NormalRatios(t *testing.T) {
	fs := &fakeStore{
		getByIDFn: func(_ context.Context, id uint64) (*model.Announcement, error) {
			return &model.Announcement{ID: id, Type: model.AnnouncementTypeSurvey}, nil
		},
		targetCountFn: func(_ context.Context) (int64, error) { return 120, nil },
		readCountFn:   func(_ context.Context, _ uint64) (int64, error) { return 80, nil },
		respCountFn:   func(_ context.Context, _ uint64) (int64, error) { return 45, nil },
	}
	b := NewWithStore(fs)
	dto, err := b.Stats(context.Background(), 1)
	require.NoError(t, err)
	assert.InDelta(t, 80.0/120.0, dto.ReadRate, 1e-9)
	assert.InDelta(t, 45.0/120.0, dto.ResponseRate, 1e-9)
}

func TestAnnouncementBiz_Stats_PlainNoResponseRate(t *testing.T) {
	fs := &fakeStore{
		getByIDFn: func(_ context.Context, id uint64) (*model.Announcement, error) {
			return &model.Announcement{ID: id, Type: model.AnnouncementTypePlain}, nil
		},
		targetCountFn: func(_ context.Context) (int64, error) { return 100, nil },
		readCountFn:   func(_ context.Context, _ uint64) (int64, error) { return 50, nil },
		respCountFn: func(_ context.Context, _ uint64) (int64, error) {
			t.Fatal("plain 公告不应查 response count")
			return 0, nil
		},
	}
	b := NewWithStore(fs)
	dto, err := b.Stats(context.Background(), 1)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, dto.ReadRate, 1e-9)
	assert.Equal(t, int64(0), dto.ResponseCount)
	assert.Equal(t, float64(0), dto.ResponseRate)
}

// ============================================================================
// Publish / Update 状态机
// ============================================================================

func TestAnnouncementBiz_Publish_NonDraft_Status(t *testing.T) {
	fs := &fakeStore{
		getByIDFn: func(_ context.Context, id uint64) (*model.Announcement, error) {
			return &model.Announcement{ID: id, Type: model.AnnouncementTypePlain, Status: model.AnnouncementStatusPublished}, nil
		},
	}
	b := NewWithStore(fs)
	_, err := b.Publish(context.Background(), 1)
	assert.ErrorIs(t, err, errno.ErrAnnouncementStatus)
}

func TestAnnouncementBiz_Publish_Draft_SetsPublishedAt(t *testing.T) {
	var capturedStatus string
	var capturedPubAt *time.Time
	calls := 0
	fs := &fakeStore{
		getByIDFn: func(_ context.Context, id uint64) (*model.Announcement, error) {
			calls++
			if calls == 1 {
				return &model.Announcement{ID: id, Type: model.AnnouncementTypePlain, Status: model.AnnouncementStatusDraft}, nil
			}
			now := time.Now()
			return &model.Announcement{ID: id, Type: model.AnnouncementTypePlain, Status: model.AnnouncementStatusPublished, PublishedAt: &now}, nil
		},
		updateStatusFn: func(_ context.Context, _ uint64, status string, publishedAt *time.Time) error {
			capturedStatus = status
			capturedPubAt = publishedAt
			return nil
		},
	}
	b := NewWithStore(fs)
	dto, err := b.Publish(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, model.AnnouncementStatusPublished, capturedStatus)
	require.NotNil(t, capturedPubAt)
	assert.Equal(t, model.AnnouncementStatusPublished, dto.Status)
}

func TestAnnouncementBiz_Update_QuestionsOnPublished_Status(t *testing.T) {
	fs := &fakeStore{
		getByIDFn: func(_ context.Context, id uint64) (*model.Announcement, error) {
			return &model.Announcement{ID: id, Type: model.AnnouncementTypeSurvey, Status: model.AnnouncementStatusPublished}, nil
		},
	}
	b := NewWithStore(fs)
	_, err := b.Update(context.Background(), 1, UpdateInput{
		Questions: []QuestionInput{
			{QuestionType: model.SurveyQuestionTypeText, Title: "Q"},
		},
	})
	assert.ErrorIs(t, err, errno.ErrAnnouncementStatus)
}

// ============================================================================
// ListReaders 校验
// ============================================================================

func TestAnnouncementBiz_ListReaders_InvalidStatus(t *testing.T) {
	b := NewWithStore(&fakeStore{})
	_, err := b.ListReaders(context.Background(), 1, "bogus", 1, 20)
	assert.ErrorIs(t, err, errno.ErrSurveyValidation)
}

// ============================================================================
// ListResponses — remap answer_* → options/rating/text
// ============================================================================

func TestAnnouncementBiz_ListResponses_RemapsAnswerFields(t *testing.T) {
	rating := 4
	text := "great"
	fs := &fakeStore{
		listRespFn: func(_ context.Context, _ uint64, _, _ int) ([]store.ResponseRow, int64, error) {
			return []store.ResponseRow{
				{
					UserID:      7,
					Nickname:    "alice",
					SubmittedAt: time.Now(),
					Answers: []model.SurveyAnswer{
						{QuestionID: 10, AnswerOptions: optsJSON("A")},
						{QuestionID: 11, AnswerRating: &rating},
						{QuestionID: 12, AnswerText: &text},
					},
				},
			}, 1, nil
		},
	}
	b := NewWithStore(fs)
	dto, err := b.ListResponses(context.Background(), 1, 1, 20)
	require.NoError(t, err)
	require.Len(t, dto.List, 1)
	require.Len(t, dto.List[0].Answers, 3)

	// options remap（answer_options → options）
	a0 := dto.List[0].Answers[0]
	assert.Equal(t, uint64(10), a0.QuestionID)
	assert.Equal(t, []string{"A"}, a0.Options)
	assert.Nil(t, a0.Rating)
	assert.Nil(t, a0.Text)

	// rating remap（answer_rating → rating）
	a1 := dto.List[0].Answers[1]
	require.NotNil(t, a1.Rating)
	assert.Equal(t, 4, *a1.Rating)

	// text remap（answer_text → text）
	a2 := dto.List[0].Answers[2]
	require.NotNil(t, a2.Text)
	assert.Equal(t, "great", *a2.Text)
}

// ============================================================================
// SurveyResults — 聚合映射
// ============================================================================

func TestAnnouncementBiz_SurveyResults_MapsAggregate(t *testing.T) {
	fs := &fakeStore{
		getByIDFn: func(_ context.Context, id uint64) (*model.Announcement, error) {
			return &model.Announcement{ID: id, Type: model.AnnouncementTypeSurvey}, nil
		},
		respCountFn: func(_ context.Context, _ uint64) (int64, error) { return 5, nil },
		aggregateFn: func(_ context.Context, _ uint64) ([]store.QuestionAggregate, error) {
			return []store.QuestionAggregate{
				{QuestionID: 10, Title: "Q1", QuestionType: model.SurveyQuestionTypeSingle,
					OptionCounts: []store.OptionCount{{Option: "A", Count: 3}, {Option: "B", Count: 2}}},
				{QuestionID: 11, Title: "Q2", QuestionType: model.SurveyQuestionTypeRating,
					Distribution: []store.RatingBucket{{Value: 1, Count: 1}, {Value: 5, Count: 4}}, Average: 4.2},
				{QuestionID: 12, Title: "Q3", QuestionType: model.SurveyQuestionTypeText,
					TextAnswers: []store.TextAnswerRow{{UserID: 7, Nickname: "a", Text: "hi"}}},
			}, nil
		},
	}
	b := NewWithStore(fs)
	dto, err := b.SurveyResults(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, int64(5), dto.ResponseCount)
	require.Len(t, dto.Questions, 3)
	assert.Len(t, dto.Questions[0].OptionCounts, 2)
	require.NotNil(t, dto.Questions[1].Average)
	assert.InDelta(t, 4.2, *dto.Questions[1].Average, 1e-9)
	assert.Len(t, dto.Questions[2].Answers, 1)
}

// ============================================================================
// Create 校验
// ============================================================================

func TestAnnouncementBiz_Create_SurveyZeroQuestions_Error(t *testing.T) {
	b := NewWithStore(&fakeStore{})
	_, err := b.Create(context.Background(), 1, CreateInput{
		Type:  model.AnnouncementTypeSurvey,
		Title: "T",
	})
	assert.ErrorIs(t, err, errno.ErrSurveyValidation)
}

func TestAnnouncementBiz_Create_SingleLessThanTwoOptions_Error(t *testing.T) {
	b := NewWithStore(&fakeStore{})
	_, err := b.Create(context.Background(), 1, CreateInput{
		Type:  model.AnnouncementTypeSurvey,
		Title: "T",
		Questions: []QuestionInput{
			{QuestionType: model.SurveyQuestionTypeSingle, Title: "Q", Options: []string{"A"}},
		},
	})
	assert.ErrorIs(t, err, errno.ErrSurveyValidation)
}

func TestAnnouncementBiz_Create_RatingMissingMax_Error(t *testing.T) {
	b := NewWithStore(&fakeStore{})
	_, err := b.Create(context.Background(), 1, CreateInput{
		Type:  model.AnnouncementTypeSurvey,
		Title: "T",
		Questions: []QuestionInput{
			{QuestionType: model.SurveyQuestionTypeRating, Title: "Q", RatingStyle: ptrStr(model.SurveyRatingStyleStar)},
		},
	})
	assert.ErrorIs(t, err, errno.ErrSurveyValidation)
}

func TestAnnouncementBiz_Create_RatingMaxOutOfRange_Error(t *testing.T) {
	b := NewWithStore(&fakeStore{})
	for _, max := range []int{1, 11} {
		_, err := b.Create(context.Background(), 1, CreateInput{
			Type:  model.AnnouncementTypeSurvey,
			Title: "T",
			Questions: []QuestionInput{
				{QuestionType: model.SurveyQuestionTypeRating, Title: "Q", RatingMax: ptrInt(max), RatingStyle: ptrStr(model.SurveyRatingStyleStar)},
			},
		})
		assert.ErrorIs(t, err, errno.ErrSurveyValidation, "rating_max=%d should fail", max)
	}
}

func TestAnnouncementBiz_Create_RatingMissingStyle_Error(t *testing.T) {
	b := NewWithStore(&fakeStore{})
	_, err := b.Create(context.Background(), 1, CreateInput{
		Type:  model.AnnouncementTypeSurvey,
		Title: "T",
		Questions: []QuestionInput{
			{QuestionType: model.SurveyQuestionTypeRating, Title: "Q", RatingMax: ptrInt(5)},
		},
	})
	assert.ErrorIs(t, err, errno.ErrSurveyValidation)
}

func TestAnnouncementBiz_Create_ValidSurvey_PublishedSetsPublishedAt(t *testing.T) {
	var capturedAnn *model.Announcement
	var capturedQs []model.SurveyQuestion
	fs := &fakeStore{
		createFn: func(_ context.Context, ann *model.Announcement, questions []model.SurveyQuestion) error {
			ann.ID = 1
			capturedAnn = ann
			capturedQs = questions
			return nil
		},
		getQuestionsFn: func(_ context.Context, _ uint64) ([]model.SurveyQuestion, error) {
			return []model.SurveyQuestion{{ID: 1, QuestionType: model.SurveyQuestionTypeSingle, Title: "Q", Options: optsJSON("A", "B"), Required: true}}, nil
		},
	}
	b := NewWithStore(fs)
	dto, err := b.Create(context.Background(), 42, CreateInput{
		Type:   model.AnnouncementTypeSurvey,
		Title:  "T",
		Status: model.AnnouncementStatusPublished,
		Questions: []QuestionInput{
			{QuestionType: model.SurveyQuestionTypeSingle, Title: "Q", Options: []string{"A", "B"}, Required: ptrBool(false)},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, capturedAnn)
	assert.Equal(t, uint(42), capturedAnn.CreatedBy)
	require.NotNil(t, capturedAnn.PublishedAt, "published 状态须设置 published_at")
	assert.Equal(t, model.AnnouncementAudienceAll, capturedAnn.Audience)
	assert.Equal(t, uint64(1), dto.ID)
	require.Len(t, dto.Questions, 1)
	assert.Equal(t, []string{"A", "B"}, dto.Questions[0].Options)
	// required=false 必须从 biz 透传到 store（spec §7；store 负责 default:1 fixup 落库）
	require.Len(t, capturedQs, 1)
	assert.False(t, capturedQs[0].Required, "biz 须把 required=false 透传给 store（QuestionInput.Required=*bool false）")
}

func TestAnnouncementBiz_Create_PlainWithQuestions_Error(t *testing.T) {
	b := NewWithStore(&fakeStore{})
	_, err := b.Create(context.Background(), 1, CreateInput{
		Type:  model.AnnouncementTypePlain,
		Title: "T",
		Questions: []QuestionInput{
			{QuestionType: model.SurveyQuestionTypeText, Title: "Q"},
		},
	})
	assert.ErrorIs(t, err, errno.ErrSurveyValidation)
}

// sentinel for wrapped-error propagation
var errStoreBoom = errors.New("boom")
