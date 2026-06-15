package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReaderRow 是 admin 端「已读/未读用户列表」(ListReaders) 的单行结果。
// read_status=read 时 ReadAt 有值；read_status=unread 时 ReadAt 为 nil（该用户无回执）。
type ReaderRow struct {
	UserID   uint       `json:"user_id"`
	Nickname string     `json:"nickname"`
	Phone    string     `json:"phone"`
	ReadAt   *time.Time `json:"read_at"`
}

// OptionCount 是单/多选题某个选项的计数（SurveyAggregate 用）。
type OptionCount struct {
	Option string `json:"option"`
	Count  int64  `json:"count"`
}

// RatingBucket 是评分题某个分值的计数（SurveyAggregate 用）。
type RatingBucket struct {
	Value int   `json:"value"`
	Count int64 `json:"count"`
}

// TextAnswerRow 是开放文本题的单条答案（SurveyAggregate 用）。
type TextAnswerRow struct {
	UserID      uint      `json:"user_id"`
	Nickname    string    `json:"nickname"`
	Text        string    `json:"text"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// QuestionAggregate 是单题的聚合结果（SurveyAggregate 用）。
// 按 QuestionType 取用对应字段：single/multi → OptionCounts；
// rating → Distribution + Average；text → TextAnswers。
type QuestionAggregate struct {
	QuestionID   uint64          `json:"question_id"`
	Title        string          `json:"title"`
	QuestionType string          `json:"question_type"`
	OptionCounts []OptionCount   `json:"option_counts,omitempty"`
	Distribution []RatingBucket  `json:"distribution,omitempty"`
	Average      float64         `json:"average"` // rating 题始终输出（0 表示暂无答卷）
	TextAnswers  []TextAnswerRow `json:"text_answers,omitempty"`
}

// ResponseRow 是 admin 端「按用户下钻答卷」(ListResponses) 的单行结果（含该用户所有答案）。
type ResponseRow struct {
	UserID      uint                 `json:"user_id"`
	Nickname    string               `json:"nickname"`
	SubmittedAt time.Time            `json:"submitted_at"`
	Answers     []model.SurveyAnswer `json:"answers"`
}

// IAnnouncementStore 定义通知中心（公告/问卷）的数据库操作接口。
//
// 方法分三组：用户端可见性查询 / 已读 / 答卷；admin CRUD；analytics 统计。
// T2a 实现用户端 + CRUD 全部方法；analytics 方法在 T2a 仅 stub，真实实现见 T2b。
// 接口全量声明以便 T3 biz 层基于完整契约编译。
type IAnnouncementStore interface {
	// ---------- 用户端 ----------

	// ListVisible 返回对用户可见的公告（status=published 且未过期且未软删），按 published_at DESC 分页。
	ListVisible(ctx context.Context, userID uint, offset, limit int) ([]model.Announcement, int64, error)
	// GetVisibleByID 返回单条对用户可见的公告；不可见（草稿/归档/过期/软删）返回 ErrAnnouncementNotFound。
	GetVisibleByID(ctx context.Context, id uint64) (*model.Announcement, error)
	// CountUnread 返回可见且当前用户未读的公告数。
	CountUnread(ctx context.Context, userID uint) (int64, error)
	// IsRead 返回当前用户是否已读指定公告。
	IsRead(ctx context.Context, annID uint64, userID uint) (bool, error)
	// MarkRead 幂等 upsert 已读回执；二次调用不改 read_at 也不报错（read_at first-write-wins）。
	MarkRead(ctx context.Context, annID uint64, userID uint) error
	// GetQuestions 返回指定公告的全部题目，按 order_index 升序。
	GetQuestions(ctx context.Context, annID uint64) ([]model.SurveyQuestion, error)
	// HasSubmitted 返回当前用户是否已提交指定问卷的答卷。
	HasSubmitted(ctx context.Context, annID uint64, userID uint) (bool, error)
	// SubmitResponse 在单个事务内写入 survey_response + 全部 survey_answer；任一失败回滚。
	SubmitResponse(ctx context.Context, resp *model.SurveyResponse, answers []model.SurveyAnswer) error

	// ---------- admin CRUD ----------

	// Create 在事务内创建公告及其题目（survey）；处理 default:1 required bool 坑（false 正确落库）。
	Create(ctx context.Context, ann *model.Announcement, questions []model.SurveyQuestion) error
	// GetByID 返回单条公告（排除软删除），不存在返回 ErrAnnouncementNotFound。
	GetByID(ctx context.Context, id uint64) (*model.Announcement, error)
	// Update 更新公告主表字段（不含 questions）。
	Update(ctx context.Context, ann *model.Announcement) error
	// ReplaceQuestions 删除旧题目并插入新题目（仅 draft 编辑用）；事务保证原子。
	ReplaceQuestions(ctx context.Context, annID uint64, questions []model.SurveyQuestion) error
	// ListAll 返回 admin 端公告列表（可按 status / type 过滤），按 created_at DESC 分页。
	ListAll(ctx context.Context, status, annType string, offset, limit int) ([]model.Announcement, int64, error)
	// UpdateStatus 更新公告状态（publish/archive）；publishedAt 非 nil 时一并写入 published_at。
	UpdateStatus(ctx context.Context, id uint64, status string, publishedAt *time.Time) error
	// SoftDelete 软删除公告（写 deleted_at）。
	SoftDelete(ctx context.Context, id uint64) error

	// ---------- analytics（T2a stub，T2b 实现）----------

	// TargetUserCount 返回目标用户数：COUNT(user WHERE is_admin=false AND deleted_at IS NULL)。
	TargetUserCount(ctx context.Context) (int64, error)
	// ReadCount 返回指定公告的已读回执数。
	ReadCount(ctx context.Context, annID uint64) (int64, error)
	// ResponseCount 返回指定公告的答卷数。
	ResponseCount(ctx context.Context, annID uint64) (int64, error)
	// ListReaders 返回已读(read)或未读(unread，反连接)用户列表，分页。
	ListReaders(ctx context.Context, annID uint64, readStatus string, offset, limit int) ([]ReaderRow, int64, error)
	// SurveyAggregate 返回问卷各题聚合（选项计数/评分分布+均值/文本列表）。
	SurveyAggregate(ctx context.Context, annID uint64) ([]QuestionAggregate, error)
	// ListResponses 返回按用户下钻的答卷列表（含每人全部答案），分页。
	ListResponses(ctx context.Context, annID uint64, offset, limit int) ([]ResponseRow, int64, error)
}

type announcementStore struct {
	db *gorm.DB
}

var _ IAnnouncementStore = (*announcementStore)(nil)

// NewAnnouncementStore 创建一个实现了 IAnnouncementStore 的实例。
func NewAnnouncementStore(db *gorm.DB) *announcementStore {
	return &announcementStore{db: db}
}

// visibleScope 应用用户端可见性过滤：status='published' AND (expires_at IS NULL OR expires_at > now)。
// deleted_at IS NULL 由 GORM 软删除 scope 自动处理（Announcement 含 gorm.DeletedAt）。
func visibleScope(db *gorm.DB) *gorm.DB {
	return db.Where("status = ?", model.AnnouncementStatusPublished).
		Where("expires_at IS NULL OR expires_at > ?", time.Now())
}

// ListVisible 返回对用户可见的公告，按 published_at DESC 分页。
func (s *announcementStore) ListVisible(ctx context.Context, userID uint, offset, limit int) ([]model.Announcement, int64, error) {
	var list []model.Announcement
	var total int64

	q := visibleScope(s.db.WithContext(ctx).Model(&model.Announcement{}))
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("ListVisible: count: %w", err)
	}
	if err := q.Order("published_at DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("ListVisible: find: %w", err)
	}
	return list, total, nil
}

// GetVisibleByID 返回单条对用户可见的公告；不可见返回 ErrAnnouncementNotFound。
func (s *announcementStore) GetVisibleByID(ctx context.Context, id uint64) (*model.Announcement, error) {
	var ann model.Announcement
	err := visibleScope(s.db.WithContext(ctx).Model(&model.Announcement{})).
		Where("id = ?", id).First(&ann).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errno.ErrAnnouncementNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetVisibleByID: %w", err)
	}
	return &ann, nil
}

// CountUnread 返回可见且当前用户未读的公告数。
// 反连接：可见公告中无该用户已读回执的数量。依赖 idx_annread_user。
func (s *announcementStore) CountUnread(ctx context.Context, userID uint) (int64, error) {
	var n int64
	err := visibleScope(s.db.WithContext(ctx).Model(&model.Announcement{})).
		Where("NOT EXISTS (SELECT 1 FROM announcement_read ar WHERE ar.announcement_id = announcement.id AND ar.user_id = ?)", userID).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("CountUnread: %w", err)
	}
	return n, nil
}

// IsRead 返回当前用户是否已读指定公告。
func (s *announcementStore) IsRead(ctx context.Context, annID uint64, userID uint) (bool, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.AnnouncementRead{}).
		Where("announcement_id = ? AND user_id = ?", annID, userID).
		Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("IsRead: %w", err)
	}
	return n > 0, nil
}

// MarkRead 幂等 upsert 已读回执。
// 用 clause.OnConflict{DoNothing:true}：命中 UNIQUE(announcement_id,user_id) 时不更新任何列，
// read_at 保留首次写入值（first-write-wins），二次调用不报错也不改时间。
func (s *announcementStore) MarkRead(ctx context.Context, annID uint64, userID uint) error {
	now := time.Now()
	row := model.AnnouncementRead{
		AnnouncementID: annID,
		UserID:         userID,
		ReadAt:         now,
		CreatedAt:      now,
	}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "announcement_id"}, {Name: "user_id"}},
			DoNothing: true,
		}).
		Create(&row).Error
	if err != nil {
		return fmt.Errorf("MarkRead: %w", err)
	}
	return nil
}

// GetQuestions 返回指定公告的全部题目，按 order_index 升序。
func (s *announcementStore) GetQuestions(ctx context.Context, annID uint64) ([]model.SurveyQuestion, error) {
	var qs []model.SurveyQuestion
	err := s.db.WithContext(ctx).
		Where("announcement_id = ?", annID).
		Order("order_index ASC, id ASC").
		Find(&qs).Error
	if err != nil {
		return nil, fmt.Errorf("GetQuestions: %w", err)
	}
	return qs, nil
}

// HasSubmitted 返回当前用户是否已提交指定问卷的答卷。
func (s *announcementStore) HasSubmitted(ctx context.Context, annID uint64, userID uint) (bool, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.SurveyResponse{}).
		Where("announcement_id = ? AND user_id = ?", annID, userID).
		Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("HasSubmitted: %w", err)
	}
	return n > 0, nil
}

// SubmitResponse 在单个事务内写入 survey_response + 全部 survey_answer。
// 先插 response 拿到自增 ID，回填到每条 answer 的 ResponseID，再批量插 answers；
// 任一步报错事务回滚（response 不残留）。UNIQUE(announcement_id,user_id) 兜底一人一答竞态。
func (s *announcementStore) SubmitResponse(ctx context.Context, resp *model.SurveyResponse, answers []model.SurveyAnswer) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(resp).Error; err != nil {
			return fmt.Errorf("SubmitResponse: create response: %w", err)
		}
		if len(answers) > 0 {
			for i := range answers {
				answers[i].ResponseID = resp.ID
			}
			if err := tx.Create(&answers).Error; err != nil {
				return fmt.Errorf("SubmitResponse: create answers: %w", err)
			}
		}
		return nil
	})
}

// Create 在事务内创建公告及其题目。
//
// default:1 required bool 坑（database.md §6）：GORM Create 把结构体 bool 零值 false 视为
// "未设置" → INSERT 跳过该列 → DB DEFAULT(1) 生效，导致 required=false 被静默写成 true。
// 这里在事务内 INSERT 后，对每条 required=false 的题目用 UpdateColumn 强制回写 false 修正。
// UpdateColumn 不触发 hook/不刷 updated_at（题目无 updated_at 列），符合 database.md §6 模式。
func (s *announcementStore) Create(ctx context.Context, ann *model.Announcement, questions []model.SurveyQuestion) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ann).Error; err != nil {
			return fmt.Errorf("Create: create announcement: %w", err)
		}
		// 注：is_important 用 gorm:"default:0"，零值 false 与 DB 默认一致，GORM 会把列写进
		// INSERT，无 default:true 坑，无需 fixup（database.md §6 仅 default:true/1 才触发）。
		for i := range questions {
			questions[i].AnnouncementID = ann.ID
			wantRequired := questions[i].Required
			if err := tx.Create(&questions[i]).Error; err != nil {
				return fmt.Errorf("Create: create question %d: %w", i, err)
			}
			// default:1 fixup —— required=false 必须落库为 false
			if !wantRequired && questions[i].Required {
				if err := tx.Model(&questions[i]).UpdateColumn("required", false).Error; err != nil {
					return fmt.Errorf("Create: fixup required on question %d: %w", i, err)
				}
				questions[i].Required = false
			}
		}
		return nil
	})
}

// GetByID 返回单条公告（GORM 软删除 scope 自动排除 deleted_at IS NOT NULL）。
// 不存在返回 ErrAnnouncementNotFound。
func (s *announcementStore) GetByID(ctx context.Context, id uint64) (*model.Announcement, error) {
	var ann model.Announcement
	err := s.db.WithContext(ctx).First(&ann, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errno.ErrAnnouncementNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetByID: %w", err)
	}
	return &ann, nil
}

// Update 更新公告主表字段（不含 questions）。
// 用 Save：GORM v2 Save 走 SELECT "*" 全列更新，is_important=false 等零值 bool 安全落库（database.md §6b）。
func (s *announcementStore) Update(ctx context.Context, ann *model.Announcement) error {
	if err := s.db.WithContext(ctx).Save(ann).Error; err != nil {
		return fmt.Errorf("Update: %w", err)
	}
	return nil
}

// ReplaceQuestions 删除旧题目并插入新题目（仅 draft 编辑用）。
// 物理删除旧题（survey_question 无软删除字段），再批量插新题；事务保证原子。
// 同样处理 default:1 required bool 坑。
func (s *announcementStore) ReplaceQuestions(ctx context.Context, annID uint64, questions []model.SurveyQuestion) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("announcement_id = ?", annID).
			Delete(&model.SurveyQuestion{}).Error; err != nil {
			return fmt.Errorf("ReplaceQuestions: delete old: %w", err)
		}
		for i := range questions {
			questions[i].ID = 0 // 确保 INSERT 新行
			questions[i].AnnouncementID = annID
			wantRequired := questions[i].Required
			if err := tx.Create(&questions[i]).Error; err != nil {
				return fmt.Errorf("ReplaceQuestions: create question %d: %w", i, err)
			}
			if !wantRequired && questions[i].Required {
				if err := tx.Model(&questions[i]).UpdateColumn("required", false).Error; err != nil {
					return fmt.Errorf("ReplaceQuestions: fixup required on question %d: %w", i, err)
				}
				questions[i].Required = false
			}
		}
		return nil
	})
}

// ListAll 返回 admin 端公告列表，可按 status / type 过滤（空串表示不过滤），按 created_at DESC 分页。
// GORM 软删除 scope 自动排除已软删公告。
func (s *announcementStore) ListAll(ctx context.Context, status, annType string, offset, limit int) ([]model.Announcement, int64, error) {
	var list []model.Announcement
	var total int64

	q := s.db.WithContext(ctx).Model(&model.Announcement{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if annType != "" {
		q = q.Where("type = ?", annType)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("ListAll: count: %w", err)
	}
	if err := q.Order("created_at DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("ListAll: find: %w", err)
	}
	return list, total, nil
}

// UpdateStatus 更新公告状态。publishedAt 非 nil 时一并写入 published_at。
// 用 map 形式 Updates（database.md §6b：map 形式总是包含 key，避免零值被吞）。
func (s *announcementStore) UpdateStatus(ctx context.Context, id uint64, status string, publishedAt *time.Time) error {
	updates := map[string]interface{}{"status": status}
	if publishedAt != nil {
		updates["published_at"] = *publishedAt
	}
	res := s.db.WithContext(ctx).Model(&model.Announcement{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("UpdateStatus: %w", res.Error)
	}
	return nil
}

// SoftDelete 软删除公告（GORM 默认 Delete 对含 gorm.DeletedAt 的模型即软删，写 deleted_at）。
func (s *announcementStore) SoftDelete(ctx context.Context, id uint64) error {
	if err := s.db.WithContext(ctx).Delete(&model.Announcement{}, id).Error; err != nil {
		return fmt.Errorf("SoftDelete: %w", err)
	}
	return nil
}

// ---------- analytics（T2b 实现） ----------

// TargetUserCount 返回目标用户数：COUNT(user WHERE is_admin = false)。
// GORM 软删除 scope（model.User 含 gorm.Model → DeletedAt）自动追加 deleted_at IS NULL，
// 故无需显式写 deleted_at 条件（spec §5 口径）。
func (s *announcementStore) TargetUserCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.User{}).
		Where("is_admin = ?", false).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("TargetUserCount: %w", err)
	}
	return n, nil
}

// ReadCount 返回指定公告的已读回执数。
func (s *announcementStore) ReadCount(ctx context.Context, annID uint64) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.AnnouncementRead{}).
		Where("announcement_id = ?", annID).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("ReadCount: %w", err)
	}
	return n, nil
}

// ResponseCount 返回指定公告的答卷数。
func (s *announcementStore) ResponseCount(ctx context.Context, annID uint64) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.SurveyResponse{}).
		Where("announcement_id = ?", annID).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("ResponseCount: %w", err)
	}
	return n, nil
}

// ListReaders 返回已读(read)或未读(unread)用户列表，分页。
//
// 反连接选型（plan §99-100 / spec §3.2）：
//   - read：INNER JOIN announcement_read 取有回执的目标用户，按 read_at DESC。
//   - unread：目标用户中 NOT EXISTS 对应回执者。选用 NOT EXISTS 子查询（而非
//     LEFT JOIN ... WHERE ar.id IS NULL），因为 NOT EXISTS 语义更直白、命中
//     idx_annread_user (user_id) 上的半连接，且不会因 JOIN 产生重复行需 DISTINCT。
//
// 两路径都过滤 user.is_admin=false；user.deleted_at IS NULL 由 GORM 软删除 scope
// （model.User 含 gorm.Model）自动追加，admin/软删用户均不出现在列表与 total 中。
func (s *announcementStore) ListReaders(ctx context.Context, annID uint64, readStatus string, offset, limit int) ([]ReaderRow, int64, error) {
	var rows []ReaderRow
	var total int64

	switch readStatus {
	case "read":
		q := s.db.WithContext(ctx).Model(&model.User{}).
			Joins("JOIN announcement_read ar ON ar.user_id = user.id AND ar.announcement_id = ?", annID).
			Where("user.is_admin = ?", false)
		if err := q.Count(&total).Error; err != nil {
			return nil, 0, fmt.Errorf("ListReaders(read): count: %w", err)
		}
		if err := q.
			Select("user.id AS user_id, user.nickname AS nickname, user.phone AS phone, ar.read_at AS read_at").
			Order("ar.read_at DESC").
			Offset(offset).Limit(limit).
			Scan(&rows).Error; err != nil {
			return nil, 0, fmt.Errorf("ListReaders(read): scan: %w", err)
		}
	case "unread":
		q := s.db.WithContext(ctx).Model(&model.User{}).
			Where("user.is_admin = ?", false).
			Where("NOT EXISTS (SELECT 1 FROM announcement_read ar WHERE ar.user_id = user.id AND ar.announcement_id = ?)", annID)
		if err := q.Count(&total).Error; err != nil {
			return nil, 0, fmt.Errorf("ListReaders(unread): count: %w", err)
		}
		// read_at 为 nil（未读用户无回执）；ReaderRow.ReadAt 是 *time.Time，Scan 时留空即 nil。
		if err := q.
			Select("user.id AS user_id, user.nickname AS nickname, user.phone AS phone").
			Order("user.id ASC").
			Offset(offset).Limit(limit).
			Scan(&rows).Error; err != nil {
			return nil, 0, fmt.Errorf("ListReaders(unread): scan: %w", err)
		}
	default:
		return nil, 0, fmt.Errorf("ListReaders: invalid read_status %q (want read|unread)", readStatus)
	}
	return rows, total, nil
}

// SurveyAggregate 返回问卷各题聚合（spec §3.2 survey-results）。
//
// 设计：JSON 数组聚合一律在 Go 内完成（plan/spec 明确禁止 MySQL JSON SQL 函数——它们
// 在 sqlite 测试库不可用，且问卷 N 小）。流程：
//  1. 按 order_index 取题目（含 options 顺序，作为输出顺序与零计数选项的来源）。
//  2. 一次性取该公告所有答卷的答案（JOIN survey_response 拿 user_id/submitted_at），
//     连带 user.nickname；在内存中按 question_id 归集。
//  3. 逐题按题型聚合：single/multi 解析 answer_options JSON 数组计数；rating 累加分布+均值；
//     text 收集非空文本。
func (s *announcementStore) SurveyAggregate(ctx context.Context, annID uint64) ([]QuestionAggregate, error) {
	questions, err := s.GetQuestions(ctx, annID)
	if err != nil {
		return nil, fmt.Errorf("SurveyAggregate: questions: %w", err)
	}

	// answerJoin 是一条答案 + 其所属答卷/用户信息（内存聚合用）。
	type answerJoin struct {
		QuestionID    uint64
		AnswerOptions datatypes.JSON
		AnswerRating  *int
		AnswerText    *string
		UserID        uint
		Nickname      string
		SubmittedAt   time.Time
	}
	var joined []answerJoin
	// survey_answer → survey_response（取 user_id/submitted_at）→ user（取 nickname）。
	// user 用 LEFT JOIN：即便用户被软删（GORM 软删 scope 不作用于显式 JOIN 表），
	// 答案仍计入聚合（已收集的答卷数据不因用户后续删除而丢失）。
	err = s.db.WithContext(ctx).
		Table("survey_answer AS sa").
		Select("sa.question_id AS question_id, sa.answer_options AS answer_options, "+
			"sa.answer_rating AS answer_rating, sa.answer_text AS answer_text, "+
			"sr.user_id AS user_id, sr.submitted_at AS submitted_at, u.nickname AS nickname").
		Joins("JOIN survey_response sr ON sr.id = sa.response_id").
		Joins("LEFT JOIN user u ON u.id = sr.user_id").
		Where("sr.announcement_id = ?", annID).
		Order("sr.submitted_at ASC, sa.id ASC").
		Scan(&joined).Error
	if err != nil {
		return nil, fmt.Errorf("SurveyAggregate: answers: %w", err)
	}

	// 按 question_id 归集答案。
	byQuestion := make(map[uint64][]answerJoin, len(questions))
	for _, a := range joined {
		byQuestion[a.QuestionID] = append(byQuestion[a.QuestionID], a)
	}

	out := make([]QuestionAggregate, 0, len(questions))
	for _, q := range questions {
		agg := QuestionAggregate{
			QuestionID:   q.ID,
			Title:        q.Title,
			QuestionType: q.QuestionType,
		}
		answers := byQuestion[q.ID]

		switch q.QuestionType {
		case model.SurveyQuestionTypeSingle, model.SurveyQuestionTypeMulti:
			// 解析题目 options 顺序，保证输出按题目选项顺序且含零计数项。
			var optList []string
			if len(q.Options) > 0 {
				if uerr := json.Unmarshal(q.Options, &optList); uerr != nil {
					return nil, fmt.Errorf("SurveyAggregate: unmarshal question %d options: %w", q.ID, uerr)
				}
			}
			counts := make(map[string]int64, len(optList))
			for _, a := range answers {
				// 跳过空 / JSON null（MySQL NULL 列经 datatypes.JSON 可能回传 []byte("null") 而非 nil）。
				if len(a.AnswerOptions) == 0 || string(a.AnswerOptions) == "null" {
					continue
				}
				var chosen []string
				if uerr := json.Unmarshal(a.AnswerOptions, &chosen); uerr != nil {
					return nil, fmt.Errorf("SurveyAggregate: unmarshal answer_options (q=%d): %w", q.ID, uerr)
				}
				for _, c := range chosen {
					counts[c]++
				}
			}
			agg.OptionCounts = make([]OptionCount, 0, len(optList))
			for _, opt := range optList {
				agg.OptionCounts = append(agg.OptionCounts, OptionCount{Option: opt, Count: counts[opt]})
			}
		case model.SurveyQuestionTypeRating:
			max := 0
			if q.RatingMax != nil {
				max = *q.RatingMax
			}
			dist := make(map[int]int64, max)
			var sum, n int64
			for _, a := range answers {
				if a.AnswerRating == nil {
					continue
				}
				v := *a.AnswerRating
				dist[v]++
				sum += int64(v)
				n++
			}
			agg.Distribution = make([]RatingBucket, 0, max)
			for v := 1; v <= max; v++ {
				agg.Distribution = append(agg.Distribution, RatingBucket{Value: v, Count: dist[v]})
			}
			if n > 0 {
				agg.Average = float64(sum) / float64(n)
			}
		case model.SurveyQuestionTypeText:
			agg.TextAnswers = make([]TextAnswerRow, 0, len(answers))
			for _, a := range answers {
				if a.AnswerText == nil || *a.AnswerText == "" {
					continue
				}
				agg.TextAnswers = append(agg.TextAnswers, TextAnswerRow{
					UserID:      a.UserID,
					Nickname:    a.Nickname,
					Text:        *a.AnswerText,
					SubmittedAt: a.SubmittedAt,
				})
			}
		}
		out = append(out, agg)
	}
	return out, nil
}

// ListResponses 返回按用户下钻的答卷列表（含每人全部答案），分页（spec §3.2 responses）。
//
// 先分页 survey_response（按 submitted_at DESC），拿到本页 response_id 集合，再一次性
// 查这些答卷的全部 survey_answer（避免 per-response N+1），并补上 user.nickname。
func (s *announcementStore) ListResponses(ctx context.Context, annID uint64, offset, limit int) ([]ResponseRow, int64, error) {
	var total int64
	if err := s.db.WithContext(ctx).Model(&model.SurveyResponse{}).
		Where("announcement_id = ?", annID).
		Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("ListResponses: count: %w", err)
	}

	// 分页本页答卷（带 nickname；user 用 LEFT JOIN 容忍软删用户）。
	type respRow struct {
		ID          uint64
		UserID      uint
		Nickname    string
		SubmittedAt time.Time
	}
	var resps []respRow
	err := s.db.WithContext(ctx).
		Table("survey_response AS sr").
		Select("sr.id AS id, sr.user_id AS user_id, sr.submitted_at AS submitted_at, u.nickname AS nickname").
		Joins("LEFT JOIN user u ON u.id = sr.user_id").
		Where("sr.announcement_id = ?", annID).
		Order("sr.submitted_at DESC, sr.id DESC").
		Offset(offset).Limit(limit).
		Scan(&resps).Error
	if err != nil {
		return nil, 0, fmt.Errorf("ListResponses: page responses: %w", err)
	}
	if len(resps) == 0 {
		return []ResponseRow{}, total, nil
	}

	// 一次性取本页所有答卷的答案，按 response_id 归集（避免 N+1）。
	respIDs := make([]uint64, 0, len(resps))
	for _, r := range resps {
		respIDs = append(respIDs, r.ID)
	}
	var answers []model.SurveyAnswer
	if err := s.db.WithContext(ctx).
		Where("response_id IN ?", respIDs).
		Order("response_id ASC, id ASC").
		Find(&answers).Error; err != nil {
		return nil, 0, fmt.Errorf("ListResponses: answers: %w", err)
	}
	byResp := make(map[uint64][]model.SurveyAnswer, len(resps))
	for _, a := range answers {
		byResp[a.ResponseID] = append(byResp[a.ResponseID], a)
	}

	out := make([]ResponseRow, 0, len(resps))
	for _, r := range resps {
		ans := byResp[r.ID]
		if ans == nil {
			ans = []model.SurveyAnswer{}
		}
		out = append(out, ResponseRow{
			UserID:      r.UserID,
			Nickname:    r.Nickname,
			SubmittedAt: r.SubmittedAt,
			Answers:     ans,
		})
	}
	return out, total, nil
}
