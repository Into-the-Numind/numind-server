package store

import (
	"context"
	"strings"
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
	// per-test 命名的内存库（避免共享 `file::memory:` 在并行/跨测试时数据串），
	// 仿本包 reservation/credit 测试既有模式。
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(
		&model.Announcement{},
		&model.AnnouncementRead{},
		&model.SurveyQuestion{},
		&model.SurveyResponse{},
		&model.SurveyAnswer{},
		&model.User{}, // T2b analytics 需 user 表（target_count / readers / nickname join）
	))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // 同名内存库多连接会锁，限单连接
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

// TestAnnouncementStore_GetByIDExcludesSoftDeleted 验证 GetByID 与 ListAll 均排除软删公告。
func TestAnnouncementStore_GetByIDExcludesSoftDeleted(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	keep := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusPublished)
	require.NoError(t, s.Create(ctx, keep, nil))
	ann := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusPublished)
	require.NoError(t, s.Create(ctx, ann, nil))

	// 软删前 ListAll 应有 2 条
	_, total, err := s.ListAll(ctx, "", "", 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)

	require.NoError(t, s.SoftDelete(ctx, ann.ID))

	_, err = s.GetByID(ctx, ann.ID)
	assert.ErrorIs(t, err, errno.ErrAnnouncementNotFound, "软删公告 GetByID 应返回 ErrAnnouncementNotFound")

	// 软删后 ListAll 应只剩 1 条（FK CASCADE 仅硬删生效，软删靠 deleted_at 过滤）
	rows, total, err := s.ListAll(ctx, "", "", 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total, "软删公告不应出现在 ListAll")
	assert.Len(t, rows, 1)
	assert.Equal(t, keep.ID, rows[0].ID)
}

// ---------- T2b analytics ----------

// seedUser 创建一个用户（caller 设 isAdmin），返回回填了自增 ID 的对象。
// Username 取自 nickname 保证非空唯一（model.User.Username 带 uniqueIndex，空串多行会撞唯一约束）。
func seedUser(t *testing.T, db *gorm.DB, nickname, phone string, isAdmin bool) *model.User {
	t.Helper()
	u := &model.User{Username: nickname, Nickname: nickname, Phone: phone, IsAdmin: isAdmin}
	require.NoError(t, db.Create(u).Error)
	require.NotZero(t, u.ID)
	return u
}

// markReadFor 直接为某用户写入指定 read_at 的已读回执（绕过 MarkRead 以控制时间顺序）。
func markReadFor(t *testing.T, db *gorm.DB, annID uint64, userID uint, readAt time.Time) {
	t.Helper()
	require.NoError(t, db.Create(&model.AnnouncementRead{
		AnnouncementID: annID, UserID: userID, ReadAt: readAt, CreatedAt: readAt,
	}).Error)
}

// TestAnnouncementStore_TargetUserCount 验证 target_count 口径：
// COUNT(user WHERE is_admin=false AND deleted_at IS NULL)——排除 admin 与软删用户。
func TestAnnouncementStore_TargetUserCount(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	seedUser(t, db, "u1", "100", false)
	seedUser(t, db, "u2", "101", false)
	seedUser(t, db, "admin1", "200", true) // 排除：admin
	soft := seedUser(t, db, "u3", "102", false)
	require.NoError(t, db.Delete(&model.User{}, soft.ID).Error) // 软删：排除

	n, err := s.TargetUserCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "仅 2 个非 admin、未软删用户计入 target_count")
}

// TestAnnouncementStore_ReadAndResponseCount 验证 ReadCount / ResponseCount 计数正确。
func TestAnnouncementStore_ReadAndResponseCount(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	ann := makeAnnouncement(model.AnnouncementTypeSurvey, model.AnnouncementStatusPublished)
	now := time.Now()
	ann.PublishedAt = &now
	require.NoError(t, s.Create(ctx, ann, nil))

	other := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusPublished)
	other.PublishedAt = &now
	require.NoError(t, s.Create(ctx, other, nil))

	u1 := seedUser(t, db, "u1", "100", false)
	u2 := seedUser(t, db, "u2", "101", false)

	markReadFor(t, db, ann.ID, u1.ID, now)
	markReadFor(t, db, ann.ID, u2.ID, now)
	markReadFor(t, db, other.ID, u1.ID, now) // 不应计入 ann 的 ReadCount

	rc, err := s.ReadCount(ctx, ann.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), rc)

	// 答卷计数
	require.NoError(t, s.SubmitResponse(ctx,
		&model.SurveyResponse{AnnouncementID: ann.ID, UserID: u1.ID, SubmittedAt: now}, nil))
	require.NoError(t, s.SubmitResponse(ctx,
		&model.SurveyResponse{AnnouncementID: ann.ID, UserID: u2.ID, SubmittedAt: now}, nil))
	require.NoError(t, s.SubmitResponse(ctx,
		&model.SurveyResponse{AnnouncementID: other.ID, UserID: u1.ID, SubmittedAt: now}, nil))

	respc, err := s.ResponseCount(ctx, ann.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), respc)
}

// TestAnnouncementStore_ListReaders 验证已读/未读用户列表：
// admin 永不出现；read 列表=有回执的非 admin 用户；unread 列表=目标用户减已读；分页 total 正确。
func TestAnnouncementStore_ListReaders(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	ann := makeAnnouncement(model.AnnouncementTypePlain, model.AnnouncementStatusPublished)
	now := time.Now()
	ann.PublishedAt = &now
	require.NoError(t, s.Create(ctx, ann, nil))

	// 4 个非 admin 用户 + 2 个 admin
	u1 := seedUser(t, db, "u1", "100", false)
	u2 := seedUser(t, db, "u2", "101", false)
	u3 := seedUser(t, db, "u3", "102", false)
	seedUser(t, db, "u4", "103", false)
	a1 := seedUser(t, db, "admin1", "200", true)
	seedUser(t, db, "admin2", "201", true)

	// u1, u2 已读（read_at 递增以验证 DESC 排序）；admin a1 也"已读"——但 admin 必须被过滤掉。
	t0 := now.Add(-2 * time.Minute)
	t1 := now.Add(-1 * time.Minute)
	markReadFor(t, db, ann.ID, u1.ID, t0)
	markReadFor(t, db, ann.ID, u2.ID, t1)
	markReadFor(t, db, ann.ID, a1.ID, now)

	// read 列表：仅 u1,u2（admin a1 被排除），按 read_at DESC → u2 在前。
	readRows, readTotal, err := s.ListReaders(ctx, ann.ID, "read", 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(2), readTotal, "read total = 2 个非 admin 回执")
	require.Len(t, readRows, 2)
	assert.Equal(t, u2.ID, readRows[0].UserID, "read_at DESC：u2(较新)在前")
	assert.Equal(t, u1.ID, readRows[1].UserID)
	require.NotNil(t, readRows[0].ReadAt, "read 行 read_at 非空")
	assert.Equal(t, "u2", readRows[0].Nickname)
	assert.Equal(t, "101", readRows[0].Phone)

	// unread 列表：目标用户(4) 减已读(2) = u3,u4；admin 永不出现。
	unreadRows, unreadTotal, err := s.ListReaders(ctx, ann.ID, "unread", 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(2), unreadTotal, "unread total = target(4) - read(2)")
	require.Len(t, unreadRows, 2)
	unreadIDs := map[uint]bool{}
	for _, r := range unreadRows {
		unreadIDs[r.UserID] = true
		assert.Nil(t, r.ReadAt, "unread 行 read_at 必须为 nil")
	}
	assert.True(t, unreadIDs[u3.ID], "u3 未读应在 unread 列表")
	// u4 也未读
	var u4 model.User
	require.NoError(t, db.Where("nickname = ?", "u4").First(&u4).Error)
	assert.True(t, unreadIDs[u4.ID], "u4 未读应在 unread 列表")

	// 确认 admin 既不在 read 也不在 unread
	for _, r := range readRows {
		assert.NotEqual(t, a1.ID, r.UserID, "admin 不应出现在 read 列表")
	}
	for _, r := range unreadRows {
		assert.NotEqual(t, a1.ID, r.UserID, "admin 不应出现在 unread 列表")
	}

	// 分页：unread limit=1 → 1 行，total 仍为 2
	page1, total, err := s.ListReaders(ctx, ann.ID, "unread", 0, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total, "分页 total 不受 limit 影响")
	assert.Len(t, page1, 1)

	// 软删一个未读的非 admin 用户 → 应从 unread 集合剔除（GORM 软删 scope 自动 deleted_at IS NULL）。
	u5 := seedUser(t, db, "u5", "104", false)
	_, beforeTotal, err := s.ListReaders(ctx, ann.ID, "unread", 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(3), beforeTotal, "新增未读用户 u5 后 unread = 3")
	require.NoError(t, db.Delete(&model.User{}, u5.ID).Error) // 软删
	afterRows, afterTotal, err := s.ListReaders(ctx, ann.ID, "unread", 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(2), afterTotal, "软删用户必须从 unread 剔除")
	for _, r := range afterRows {
		assert.NotEqual(t, u5.ID, r.UserID, "软删用户不应出现在 unread 列表")
	}

	// 非法 status 报错
	_, _, err = s.ListReaders(ctx, ann.ID, "bogus", 0, 50)
	assert.Error(t, err)
}

// TestAnnouncementStore_SurveyAggregate 验证问卷聚合：
// single/multi 选项计数（含零计数选项）；rating 分布+均值；text 文本列表。
func TestAnnouncementStore_SurveyAggregate(t *testing.T) {
	db := newAnnouncementTestDB(t)
	s := NewAnnouncementStore(db)
	ctx := context.Background()

	ann := makeAnnouncement(model.AnnouncementTypeSurvey, model.AnnouncementStatusPublished)
	now := time.Now()
	ann.PublishedAt = &now
	max := 5
	style := model.SurveyRatingStyleStar
	require.NoError(t, s.Create(ctx, ann, []model.SurveyQuestion{
		{OrderIndex: 0, QuestionType: model.SurveyQuestionTypeSingle, Title: "单选题",
			Options: datatypes.JSON([]byte(`["A","B","C"]`)), Required: true},
		{OrderIndex: 1, QuestionType: model.SurveyQuestionTypeMulti, Title: "多选题",
			Options: datatypes.JSON([]byte(`["X","Y","Z"]`)), Required: true},
		{OrderIndex: 2, QuestionType: model.SurveyQuestionTypeRating, Title: "评分题",
			RatingMax: &max, RatingStyle: &style, Required: true},
		{OrderIndex: 3, QuestionType: model.SurveyQuestionTypeText, Title: "文本题", Required: false},
	}))
	qs, err := s.GetQuestions(ctx, ann.ID)
	require.NoError(t, err)
	require.Len(t, qs, 4)
	qSingle, qMulti, qRating, qText := qs[0], qs[1], qs[2], qs[3]

	u1 := seedUser(t, db, "u1", "100", false)
	u2 := seedUser(t, db, "u2", "101", false)
	u3 := seedUser(t, db, "u3", "102", false)

	r := func(v int) *int { return &v }
	txt := func(v string) *string { return &v }

	// u1: single=A, multi=[X,Y], rating=5, text="很好"
	require.NoError(t, s.SubmitResponse(ctx,
		&model.SurveyResponse{AnnouncementID: ann.ID, UserID: u1.ID, SubmittedAt: now.Add(-2 * time.Minute)},
		[]model.SurveyAnswer{
			{QuestionID: qSingle.ID, AnswerOptions: datatypes.JSON([]byte(`["A"]`))},
			{QuestionID: qMulti.ID, AnswerOptions: datatypes.JSON([]byte(`["X","Y"]`))},
			{QuestionID: qRating.ID, AnswerRating: r(5)},
			{QuestionID: qText.ID, AnswerText: txt("很好")},
		}))
	// u2: single=A, multi=[X], rating=3, text=""（空文本应被忽略）
	require.NoError(t, s.SubmitResponse(ctx,
		&model.SurveyResponse{AnnouncementID: ann.ID, UserID: u2.ID, SubmittedAt: now.Add(-1 * time.Minute)},
		[]model.SurveyAnswer{
			{QuestionID: qSingle.ID, AnswerOptions: datatypes.JSON([]byte(`["A"]`))},
			{QuestionID: qMulti.ID, AnswerOptions: datatypes.JSON([]byte(`["X"]`))},
			{QuestionID: qRating.ID, AnswerRating: r(3)},
			{QuestionID: qText.ID, AnswerText: txt("")},
		}))
	// u3: single=B, multi=[Y,Z], rating=5, text="不错"
	require.NoError(t, s.SubmitResponse(ctx,
		&model.SurveyResponse{AnnouncementID: ann.ID, UserID: u3.ID, SubmittedAt: now},
		[]model.SurveyAnswer{
			{QuestionID: qSingle.ID, AnswerOptions: datatypes.JSON([]byte(`["B"]`))},
			{QuestionID: qMulti.ID, AnswerOptions: datatypes.JSON([]byte(`["Y","Z"]`))},
			{QuestionID: qRating.ID, AnswerRating: r(5)},
			{QuestionID: qText.ID, AnswerText: txt("不错")},
		}))

	aggs, err := s.SurveyAggregate(ctx, ann.ID)
	require.NoError(t, err)
	require.Len(t, aggs, 4, "聚合按 order_index 返回 4 题")

	// 单选题：A=2, B=1, C=0（零计数选项保留，且顺序=题目 options 顺序）
	single := aggs[0]
	assert.Equal(t, model.SurveyQuestionTypeSingle, single.QuestionType)
	require.Len(t, single.OptionCounts, 3)
	assert.Equal(t, OptionCount{Option: "A", Count: 2}, single.OptionCounts[0])
	assert.Equal(t, OptionCount{Option: "B", Count: 1}, single.OptionCounts[1])
	assert.Equal(t, OptionCount{Option: "C", Count: 0}, single.OptionCounts[2], "零计数选项必须保留")

	// 多选题：X=2(u1,u2), Y=2(u1,u3), Z=1(u3)
	multi := aggs[1]
	require.Len(t, multi.OptionCounts, 3)
	assert.Equal(t, OptionCount{Option: "X", Count: 2}, multi.OptionCounts[0])
	assert.Equal(t, OptionCount{Option: "Y", Count: 2}, multi.OptionCounts[1])
	assert.Equal(t, OptionCount{Option: "Z", Count: 1}, multi.OptionCounts[2])

	// 评分题：分布 1..5，其中 3→1, 5→2；均值 (5+3+5)/3 = 4.333...
	rating := aggs[2]
	require.Len(t, rating.Distribution, 5, "分布覆盖 1..rating_max")
	distMap := map[int]int64{}
	for _, b := range rating.Distribution {
		distMap[b.Value] = b.Count
	}
	assert.Equal(t, int64(1), distMap[3])
	assert.Equal(t, int64(2), distMap[5])
	assert.Equal(t, int64(0), distMap[1])
	assert.InDelta(t, float64(13)/float64(3), rating.Average, 1e-9, "均值 = (5+3+5)/3")

	// 文本题：u1,u3 两条非空（u2 空文本被忽略）
	text := aggs[3]
	require.Len(t, text.TextAnswers, 2, "空文本应被过滤")
	texts := map[string]string{}
	for _, ta := range text.TextAnswers {
		texts[ta.Nickname] = ta.Text
	}
	assert.Equal(t, "很好", texts["u1"])
	assert.Equal(t, "不错", texts["u3"])
}

// TestAnnouncementStore_ListResponses 验证按用户下钻答卷：分页 + 每人答案正确。
func TestAnnouncementStore_ListResponses(t *testing.T) {
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

	u1 := seedUser(t, db, "u1", "100", false)
	u2 := seedUser(t, db, "u2", "101", false)
	u3 := seedUser(t, db, "u3", "102", false)

	txt := func(v string) *string { return &v }
	// 提交顺序 u1(最早) → u2 → u3(最新)；ListResponses 按 submitted_at DESC → u3,u2,u1
	require.NoError(t, s.SubmitResponse(ctx,
		&model.SurveyResponse{AnnouncementID: ann.ID, UserID: u1.ID, SubmittedAt: now.Add(-2 * time.Minute)},
		[]model.SurveyAnswer{
			{QuestionID: qs[0].ID, AnswerOptions: datatypes.JSON([]byte(`["A"]`))},
			{QuestionID: qs[1].ID, AnswerText: txt("u1文本")},
		}))
	require.NoError(t, s.SubmitResponse(ctx,
		&model.SurveyResponse{AnnouncementID: ann.ID, UserID: u2.ID, SubmittedAt: now.Add(-1 * time.Minute)},
		[]model.SurveyAnswer{
			{QuestionID: qs[0].ID, AnswerOptions: datatypes.JSON([]byte(`["B"]`))},
			{QuestionID: qs[1].ID, AnswerText: txt("u2文本")},
		}))
	require.NoError(t, s.SubmitResponse(ctx,
		&model.SurveyResponse{AnnouncementID: ann.ID, UserID: u3.ID, SubmittedAt: now},
		[]model.SurveyAnswer{
			{QuestionID: qs[0].ID, AnswerOptions: datatypes.JSON([]byte(`["A"]`))},
			{QuestionID: qs[1].ID, AnswerText: txt("u3文本")},
		}))

	// 全量
	rows, total, err := s.ListResponses(ctx, ann.ID, 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, rows, 3)
	assert.Equal(t, u3.ID, rows[0].UserID, "submitted_at DESC：u3 最新在前")
	assert.Equal(t, u2.ID, rows[1].UserID)
	assert.Equal(t, u1.ID, rows[2].UserID)
	assert.Equal(t, "u3", rows[0].Nickname)
	require.Len(t, rows[0].Answers, 2, "每份答卷含 2 条答案")

	// 分页：limit=2 → 2 行（u3,u2），total 仍 3
	page1, total, err := s.ListResponses(ctx, ann.ID, 0, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, page1, 2)
	assert.Equal(t, u3.ID, page1[0].UserID)
	assert.Equal(t, u2.ID, page1[1].UserID)

	// 第二页：offset=2 limit=2 → 1 行（u1）
	page2, _, err := s.ListResponses(ctx, ann.ID, 2, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, u1.ID, page2[0].UserID)

	// 空公告（无答卷）→ 空列表、total=0
	empty := makeAnnouncement(model.AnnouncementTypeSurvey, model.AnnouncementStatusPublished)
	empty.PublishedAt = &now
	require.NoError(t, s.Create(ctx, empty, nil))
	emptyRows, emptyTotal, err := s.ListResponses(ctx, empty.ID, 0, 50)
	require.NoError(t, err)
	assert.Equal(t, int64(0), emptyTotal)
	assert.Empty(t, emptyRows)
}
