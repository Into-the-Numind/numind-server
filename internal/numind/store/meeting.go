package store

import (
	"context"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// IMeetingStore 定义会议副驾 (Meeting Copilot) 的数据访问接口（meeting-copilot feature）。
//
// 覆盖 session / segment / feedback / preset 四类实体的 CRUD、分页 List、
// 以及按 session 取 segments/feedbacks。所有读写均传 context。
type IMeetingStore interface {
	// --- session ---
	// CreateSession 创建会话（写回 ID）。
	CreateSession(ctx context.Context, s *model.MeetingSession) error
	// GetSession 按主键查会话；不存在返回 gorm.ErrRecordNotFound。
	GetSession(ctx context.Context, id uint64) (*model.MeetingSession, error)
	// ListSessions 分页列出用户会话，按 created_at DESC。返回 (list, total)。
	ListSessions(ctx context.Context, userID uint, offset, limit int) ([]model.MeetingSession, int64, error)
	// UpdateSession 全量保存会话（含零值字段，用 db.Save，参 .claude/rules/database.md §6b：Save 对零值 bool 安全）。
	UpdateSession(ctx context.Context, s *model.MeetingSession) error

	// --- segment ---
	// CreateSegment 追加一条转写分段（写回 ID）。
	CreateSegment(ctx context.Context, seg *model.MeetingSegment) error
	// ListSegmentsBySession 取某会话全部分段，按 seq ASC。
	ListSegmentsBySession(ctx context.Context, sessionID uint64) ([]model.MeetingSegment, error)
	// GetMaxSegmentSeq 取某会话当前最大 seq，无分段返回 0（供后端自增 seq 兜底）。
	GetMaxSegmentSeq(ctx context.Context, sessionID uint64) (int, error)

	// --- feedback ---
	// CreateFeedback 追加一条反馈事件（写回 ID）。
	CreateFeedback(ctx context.Context, fb *model.MeetingFeedback) error
	// ListFeedbacksBySession 取某会话全部反馈，按 created_at ASC。
	ListFeedbacksBySession(ctx context.Context, sessionID uint64) ([]model.MeetingFeedback, error)

	// --- preset ---
	// CreatePreset 创建预设（写回 ID）。
	CreatePreset(ctx context.Context, p *model.MeetingPreset) error
	// GetPreset 按主键查预设；不存在返回 gorm.ErrRecordNotFound。
	GetPreset(ctx context.Context, id uint64) (*model.MeetingPreset, error)
	// ListPresetsForUser 列出当前用户预设 + 系统内置（user_id=0），内置在前，组内按 created_at ASC。
	ListPresetsForUser(ctx context.Context, userID uint) ([]model.MeetingPreset, error)
	// DeletePreset 硬删除预设；不存在返回 gorm.ErrRecordNotFound。
	// 仅删 (id, user_id) 匹配且非 builtin 的行，归属/内置校验由 biz 层做（此处提供 user_id 守卫）。
	DeletePreset(ctx context.Context, id uint64, userID uint) error
}

// meetingStore 是 IMeetingStore 的 GORM 实现。
type meetingStore struct {
	db *gorm.DB
}

var _ IMeetingStore = (*meetingStore)(nil)

// newMeetingStore 创建一个 IMeetingStore 实例。
func newMeetingStore(db *gorm.DB) *meetingStore {
	return &meetingStore{db: db}
}

// --- session ---

// CreateSession 创建会话。
func (s *meetingStore) CreateSession(ctx context.Context, m *model.MeetingSession) error {
	return s.db.WithContext(ctx).Create(m).Error
}

// GetSession 按主键查会话。
func (s *meetingStore) GetSession(ctx context.Context, id uint64) (*model.MeetingSession, error) {
	var m model.MeetingSession
	if err := s.db.WithContext(ctx).First(&m, id).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// ListSessions 分页列出用户会话，按 created_at DESC。
func (s *meetingStore) ListSessions(ctx context.Context, userID uint, offset, limit int) ([]model.MeetingSession, int64, error) {
	var list []model.MeetingSession
	var total int64

	query := s.db.WithContext(ctx).Model(&model.MeetingSession{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&list).Error; err != nil {
		return nil, 0, err
	}

	return list, total, nil
}

// UpdateSession 全量保存会话。用 db.Save（含 SELECT "*"，对零值 bool 安全，见 .claude/rules/database.md §6b）。
func (s *meetingStore) UpdateSession(ctx context.Context, m *model.MeetingSession) error {
	return s.db.WithContext(ctx).Save(m).Error
}

// --- segment ---

// CreateSegment 追加一条转写分段。
func (s *meetingStore) CreateSegment(ctx context.Context, seg *model.MeetingSegment) error {
	return s.db.WithContext(ctx).Create(seg).Error
}

// ListSegmentsBySession 取某会话全部分段，按 seq ASC。
func (s *meetingStore) ListSegmentsBySession(ctx context.Context, sessionID uint64) ([]model.MeetingSegment, error) {
	var list []model.MeetingSegment
	if err := s.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("seq ASC").
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// GetMaxSegmentSeq 取某会话当前最大 seq，无分段返回 0。
func (s *meetingStore) GetMaxSegmentSeq(ctx context.Context, sessionID uint64) (int, error) {
	var maxSeq *int
	if err := s.db.WithContext(ctx).
		Model(&model.MeetingSegment{}).
		Where("session_id = ?", sessionID).
		Select("MAX(seq)").
		Scan(&maxSeq).Error; err != nil {
		return 0, err
	}
	if maxSeq == nil {
		return 0, nil
	}
	return *maxSeq, nil
}

// --- feedback ---

// CreateFeedback 追加一条反馈事件。
func (s *meetingStore) CreateFeedback(ctx context.Context, fb *model.MeetingFeedback) error {
	return s.db.WithContext(ctx).Create(fb).Error
}

// ListFeedbacksBySession 取某会话全部反馈，按 created_at ASC。
func (s *meetingStore) ListFeedbacksBySession(ctx context.Context, sessionID uint64) ([]model.MeetingFeedback, error) {
	var list []model.MeetingFeedback
	if err := s.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("created_at ASC, id ASC").
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// --- preset ---

// CreatePreset 创建预设。
func (s *meetingStore) CreatePreset(ctx context.Context, p *model.MeetingPreset) error {
	return s.db.WithContext(ctx).Create(p).Error
}

// GetPreset 按主键查预设。
func (s *meetingStore) GetPreset(ctx context.Context, id uint64) (*model.MeetingPreset, error) {
	var p model.MeetingPreset
	if err := s.db.WithContext(ctx).First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPresetsForUser 列出当前用户预设 + 系统内置（user_id=0）。
// 内置在前（user_id=0 ASC 把 0 排首组），组内按 created_at ASC。
func (s *meetingStore) ListPresetsForUser(ctx context.Context, userID uint) ([]model.MeetingPreset, error) {
	var list []model.MeetingPreset
	if err := s.db.WithContext(ctx).
		Where("user_id = ? OR user_id = 0", userID).
		Order("is_builtin DESC, created_at ASC, id ASC").
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// DeletePreset 硬删除预设。仅删 (id, user_id) 匹配且非 builtin 的行；不存在/不匹配返回 gorm.ErrRecordNotFound。
func (s *meetingStore) DeletePreset(ctx context.Context, id uint64, userID uint) error {
	res := s.db.WithContext(ctx).
		Where("id = ? AND user_id = ? AND is_builtin = 0", id, userID).
		Delete(&model.MeetingPreset{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
