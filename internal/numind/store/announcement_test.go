package store

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newAnnouncementTestDB 创建公告 store 测试用的内存 SQLite DB。
// 直接 AutoMigrate 5 个通知模型——它们都不依赖 MySQL 特有类型（longtext/json 在 sqlite
// 下退化为 TEXT，datatypes.JSON 兼容），可放心用 AutoMigrate 建表（含 default:1 required 列，
// 用于复现 GORM Create default-bool 坑）。
func newAnnouncementTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(
		&model.Announcement{},
		&model.AnnouncementRead{},
		&model.SurveyQuestion{},
		&model.SurveyResponse{},
		&model.SurveyAnswer{},
	))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// makeAnnouncement 构造一条公告（默认 plain / draft），caller 可改字段。
func makeAnnouncement(typ, status string) *model.Announcement {
	return &model.Announcement{
		Type:      typ,
		Title:     "测试标题",
		Content:   "正文 markdown",
		Audience:  model.AnnouncementAudienceAll,
		Status:    status,
		CreatedBy: 1,
	}
}

func TestAnnouncementStore_CreatePlainAndGet(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	ann := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusDraft)
	require.NoError(t, s.Create(ctx, ann, nil))
	require.NotZero(t, ann.ID, "Create 应回填自增 ID")

	got, err := s.GetByID(ctx, ann.ID)
	require.NoError(t, err)
	assert.Equal(t, ann.ID, got.ID)
	assert.Equal(t, model.AnnouncementTypePlain, got.Type)
	assert.Equal(t, "测试标题", got.Title)

	qs, err := s.GetQuestions(ctx, ann.ID)
	require.NoError(t, err)
	assert.Empty(t, qs, "plain 公告无题目")
}

func TestAnnouncementStore_CreateSurveyWithQuestions(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	ann := makeAnnouncement(model.AnnouncementTypeSurvey, model.AnnouncementStatusDraft)
	questions := []model.SurveyQuestion{
		{
			OrderIndex:   0,
			QuestionType: model.SurveyQuestionTypeSingle,
			Title:        "第一题",
			Options:      datatypes.JSON([]byte(`["A","B"]`)),
			Required:     true,
		},
		{
			OrderIndex:   1,
			QuestionType: model.SurveyQuestionTypeText,
			Title:        "第二题",
			Required:     true,
		},
	}
	require.NoError(t, s.Create(ctx, ann, questions))
	require.NotZero(t, ann.ID)

	qs, err := s.GetQuestions(ctx, ann.ID)
	require.NoError(t, err)
	require.Len(t, qs, 2)
	assert.Equal(t, "第一题", qs[0].Title)
	assert.Equal(t, ann.ID, qs[0].AnnouncementID, "AnnouncementID 应被回填")
	assert.Equal(t, 0, qs[0].OrderIndex)
	assert.Equal(t, 1, qs[1].OrderIndex, "应按 order_index 升序")
}

// TestAnnouncementStore_DefaultBoolFalsePersists 是 database.md §6 的 GORM Create 路径
// default-bool 坑回归测试：required 带 gorm:"default:1"，若 Create 跳过零值 false 列，
// DB DEFAULT(1) 会把 required=false 静默写成 true。此测试在缺少 UpdateColumn fixup 时必然 FAIL。
func TestAnnouncementStore_DefaultBoolFalsePersists(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	// is_important=false（default:0，理应天然安全，但一并断言防御回归）
	ann := makeAnnouncement(model.AnnouncementTypeSurvey, model.AnnouncementStatusDraft)
	ann.IsImportant = false

	// required=false 的题目（这是真正的 default:1 坑）
	questions := []model.SurveyQuestion{
		{
			OrderIndex:   0,
			QuestionType: model.SurveyQuestionTypeText,
			Title:        "非必答题",
			Required:     false,
		},
	}
	require.NoError(t, s.Create(ctx, ann, questions))

	// 从 DB 重新读回，确认落库值
	var reAnn model.Announcement
	require.NoError(t, db.First(&reAnn, ann.ID).Error)
	assert.False(t, reAnn.IsImportant, "is_important=false 必须落库为 false")

	var reQ model.SurveyQuestion
	require.NoError(t, db.Where("announcement_id = ?", ann.ID).First(&reQ).Error)
	assert.False(t, reQ.Required, "required=false 必须落库为 false（default:1 坑回归）")
}

// TestAnnouncementStore_ReplaceQuestionsDefaultBool 确保 draft 编辑路径同样修正 default:1 坑。
func TestAnnouncementStore_ReplaceQuestionsDefaultBool(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	ann := makeAnnouncement(model.AnnouncementTypeSurvey, model.AnnouncementStatusDraft)
	require.NoError(t, s.Create(ctx, ann, []model.SurveyQuestion{
		{OrderIndex: 0, QuestionType: model.SurveyQuestionTypeText, Title: "旧题", Required: true},
	}))

	// 替换为一条 required=false 的新题
	require.NoError(t, s.ReplaceQuestions(ctx, ann.ID, []model.SurveyQuestion{
		{OrderIndex: 0, QuestionType: model.SurveyQuestionTypeText, Title: "新题", Required: false},
	}))

	qs, err := s.GetQuestions(ctx, ann.ID)
	require.NoError(t, err)
	require.Len(t, qs, 1, "旧题应被删除")
	assert.Equal(t, "新题", qs[0].Title)
	assert.False(t, qs[0].Required, "ReplaceQuestions 也须修正 default:1 坑")
}

// TestAnnouncementStore_MarkReadIdempotent 验证已读 upsert 幂等：
// 二次 MarkRead 不增计数、不改 read_at（first-write-wins）。
func TestAnnouncementStore_MarkReadIdempotent(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	ann := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusPublished)
	now := time.Now()
	ann.PublishedAt = &now
	require.NoError(t, s.Create(ctx, ann, nil))

	const uid uint = 7

	// 初始：未读
	read, err := s.IsRead(ctx, ann.ID, uid)
	require.NoError(t, err)
	assert.False(t, read)

	unread, err := s.CountUnread(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, int64(1), unread, "1 条可见且未读")

	// 第一次标记已读
	require.NoError(t, s.MarkRead(ctx, ann.ID, uid))

	var firstRow model.AnnouncementRead
	require.NoError(t, db.Where("announcement_id = ? AND user_id = ?", ann.ID, uid).First(&firstRow).Error)
	firstReadAt := firstRow.ReadAt

	read, err = s.IsRead(ctx, ann.ID, uid)
	require.NoError(t, err)
	assert.True(t, read)

	unread, err = s.CountUnread(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, int64(0), unread, "已读后未读数归零")

	// 第二次标记已读（幂等）：不报错、不增计数、read_at 不变
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, s.MarkRead(ctx, ann.ID, uid))

	var count int64
	require.NoError(t, db.Model(&model.AnnouncementRead{}).
		Where("announcement_id = ? AND user_id = ?", ann.ID, uid).Count(&count).Error)
	assert.Equal(t, int64(1), count, "幂等：仍只有 1 条回执")

	var secondRow model.AnnouncementRead
	require.NoError(t, db.Where("announcement_id = ? AND user_id = ?", ann.ID, uid).First(&secondRow).Error)
	assert.WithinDuration(t, firstReadAt, secondRow.ReadAt, time.Millisecond,
		"read_at 保留首次写入值（first-write-wins）")
}

// TestAnnouncementStore_ListVisibleExcludesNonVisible 验证可见性过滤：
// draft / archived / 过期 / 软删 公告均不出现在 ListVisible / CountUnread，且 GetVisibleByID 拒绝。
func TestAnnouncementStore_ListVisibleExcludesNonVisible(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	// 可见：published + 未过期
	visible := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusPublished)
	visible.PublishedAt = &now
	visible.ExpiresAt = &future
	require.NoError(t, s.Create(ctx, visible, nil))

	// 可见：published + 永不过期
	visibleNoExpiry := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusPublished)
	visibleNoExpiry.PublishedAt = &now
	require.NoError(t, s.Create(ctx, visibleNoExpiry, nil))

	// 不可见：draft
	draft := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusDraft)
	require.NoError(t, s.Create(ctx, draft, nil))

	// 不可见：archived
	archived := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusArchived)
	archived.PublishedAt = &now
	require.NoError(t, s.Create(ctx, archived, nil))

	// 不可见：published 但已过期
	expired := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusPublished)
	expired.PublishedAt = &past
	expired.ExpiresAt = &past
	require.NoError(t, s.Create(ctx, expired, nil))

	// 不可见：published 但软删
	deleted := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusPublished)
	deleted.PublishedAt = &now
	require.NoError(t, s.Create(ctx, deleted, nil))
	require.NoError(t, s.SoftDelete(ctx, deleted.ID))

	const uid uint = 42
	list, total, err := s.ListVisible(ctx, uid, 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total, "仅 2 条可见")
	assert.Len(t, list, 2)

	unread, err := s.CountUnread(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, int64(2), unread, "可见且未读 = 2")

	// GetVisibleByID 对不可见公告返回 ErrAnnouncementNotFound
	for name, id := range map[string]uint64{
		"draft":    draft.ID,
		"archived": archived.ID,
		"expired":  expired.ID,
		"deleted":  deleted.ID,
	} {
		_, err := s.GetVisibleByID(ctx, id)
		assert.Error(t, err, "GetVisibleByID(%s) 应拒绝", name)
	}

	// GetVisibleByID 对可见公告返回成功
	got, err := s.GetVisibleByID(ctx, visible.ID)
	require.NoError(t, err)
	assert.Equal(t, visible.ID, got.ID)
}

// TestAnnouncementStore_SubmitResponseAtomic 验证答卷事务：response + answers 一并写入，
// HasSubmitted 提交后为 true，answers 行数正确。
func TestAnnouncementStore_SubmitResponseAtomic(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	ann := makeAnnouncement(model.AnnouncementTypeSurvey, model.AnnouncementStatusPublished)
	now := time.Now()
	ann.PublishedAt = &now
	require.NoError(t, s.Create(ctx, ann, []model.SurveyQuestion{
		{OrderIndex: 0, QuestionType: model.SurveyQuestionTypeSingle, Title: "Q1",
			Options: datatypes.JSON([]byte(`["A","B"]`)), Required: true},
		{OrderIndex: 1, QuestionType: model.SurveyQuestionTypeText, Title: "Q2", Required: false},
	}))
	qs, err := s.GetQuestions(ctx, ann.ID)
	require.NoError(t, err)
	require.Len(t, qs, 2)

	const uid uint = 99
	submitted, err := s.HasSubmitted(ctx, ann.ID, uid)
	require.NoError(t, err)
	assert.False(t, submitted)

	txt := "开放回答"
	resp := &model.SurveyResponse{
		AnnouncementID: ann.ID,
		UserID:         uid,
		SubmittedAt:    time.Now(),
	}
	answers := []model.SurveyAnswer{
		{QuestionID: qs[0].ID, AnswerOptions: datatypes.JSON([]byte(`["A"]`))},
		{QuestionID: qs[1].ID, AnswerText: &txt},
	}
	require.NoError(t, s.SubmitResponse(ctx, resp, answers))
	require.NotZero(t, resp.ID, "SubmitResponse 应回填 response ID")

	submitted, err = s.HasSubmitted(ctx, ann.ID, uid)
	require.NoError(t, err)
	assert.True(t, submitted, "提交后 HasSubmitted=true")

	var answerCount int64
	require.NoError(t, db.Model(&model.SurveyAnswer{}).
		Where("response_id = ?", resp.ID).Count(&answerCount).Error)
	assert.Equal(t, int64(2), answerCount, "两条答案均写入")

	// 答案的 ResponseID 被正确回填
	var savedAnswers []model.SurveyAnswer
	require.NoError(t, db.Where("response_id = ?", resp.ID).Find(&savedAnswers).Error)
	require.Len(t, savedAnswers, 2)
	for _, a := range savedAnswers {
		assert.Equal(t, resp.ID, a.ResponseID)
	}
}

// TestAnnouncementStore_AdminCRUD 覆盖 ListAll 过滤 / UpdateStatus / Update。
func TestAnnouncementStore_AdminCRUD(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	draft := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusDraft)
	require.NoError(t, s.Create(ctx, draft, nil))
	survey := makeAnnouncement(model.AnnouncementTypeSurvey, model.AnnouncementStatusDraft)
	require.NoError(t, s.Create(ctx, survey, nil))

	// ListAll 无过滤 → 2 条
	list, total, err := s.ListAll(ctx, "", "", 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, list, 2)

	// 按 type=survey 过滤 → 1 条
	_, total, err = s.ListAll(ctx, "", model.AnnouncementTypeSurvey, 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)

	// UpdateStatus → published + published_at
	now := time.Now()
	require.NoError(t, s.UpdateStatus(ctx, draft.ID, model.AnnouncementStatusPublished, &now))
	got, err := s.GetByID(ctx, draft.ID)
	require.NoError(t, err)
	assert.Equal(t, model.AnnouncementStatusPublished, got.Status)
	require.NotNil(t, got.PublishedAt)

	// 按 status=draft 过滤 → 仅剩 survey
	_, total, err = s.ListAll(ctx, model.AnnouncementStatusDraft, "", 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)

	// Update 主表字段（含 is_important 切换，验证 Save 路径零值 bool 安全）
	got.Title = "改后标题"
	got.IsImportant = true
	require.NoError(t, s.Update(ctx, got))
	reloaded, err := s.GetByID(ctx, draft.ID)
	require.NoError(t, err)
	assert.Equal(t, "改后标题", reloaded.Title)
	assert.True(t, reloaded.IsImportant)

	// 切回 is_important=false 再验证（database.md §6b：Save 走 SELECT "*" 安全落 false）
	reloaded.IsImportant = false
	require.NoError(t, s.Update(ctx, reloaded))
	final, err := s.GetByID(ctx, draft.ID)
	require.NoError(t, err)
	assert.False(t, final.IsImportant, "Update 路径 is_important=false 须落库")
}

// TestAnnouncementStore_GetByIDExcludesSoftDeleted 验证 GetByID 排除软删公告。
func TestAnnouncementStore_GetByIDExcludesSoftDeleted(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	ann := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusPublished)
	require.NoError(t, s.Create(ctx, ann, nil))
	require.NoError(t, s.SoftDelete(ctx, ann.ID))

	_, err := s.GetByID(ctx, ann.ID)
	assert.ErrorIs(t, err, errno.ErrAnnouncementNotFound, "软删公告 GetByID 应返回 ErrAnnouncementNotFound")
}
