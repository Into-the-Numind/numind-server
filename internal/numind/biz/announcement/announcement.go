// Package announcement 实现通知中心（公告/问卷）的业务逻辑（notification-center T3）。
//
// 职责分层（业务逻辑硬规则）：本包组装 store 调用、做可见性/状态/校验判定，并
// 拥有面向 HTTP 的 RESPONSE DTO（json tag 严格匹配 spec §3）。controller（T4）只做
// 参数绑定 + 鉴权上下文提取 + core.WriteResponse(c, nil, dto)，不含业务逻辑。
//
// 关键 remap（spec §3.2）：store 的 ResponseRow.Answers 是 []model.SurveyAnswer
// （json key answer_options/answer_rating/answer_text），spec /responses 要求
// answers:[{question_id, options, rating, text}] —— 本包 DTO 显式重映射字段名。
package announcement

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ============================================================================
// RESPONSE DTO（json tag 严格匹配 spec §3，controller 直接 WriteResponse）
// ============================================================================

// QuestionDTO 是问卷题目的对外形状（spec §3.1 / §3.2 detail.questions[]）。
// options/rating_max/rating_style 在非对应题型时为 null（沿用 model 的 *T / datatypes.JSON
// nil 语义 → JSON null）。
type QuestionDTO struct {
	ID           uint64      `json:"id"`
	OrderIndex   int         `json:"order_index"`
	QuestionType string      `json:"question_type"`
	Title        string      `json:"title"`
	Required     bool        `json:"required"`
	Options      interface{} `json:"options"`      // []string 或 null（single/multi 才有）
	RatingMax    *int        `json:"rating_max"`   // rating 才有
	RatingStyle  *string     `json:"rating_style"` // rating 才有
}

// AnnouncementBrief 是用户端列表项（spec §3.1）。
type AnnouncementBrief struct {
	ID                uint64     `json:"id"`
	Type              string     `json:"type"`
	Title             string     `json:"title"`
	Content           string     `json:"content"`
	IsImportant       bool       `json:"is_important"`
	PublishedAt       *time.Time `json:"published_at"`
	ExpiresAt         *time.Time `json:"expires_at"`
	IsRead            bool       `json:"is_read"`
	IsSurveySubmitted bool       `json:"is_survey_submitted"`
}

// AnnouncementListDTO 是用户端列表响应（spec §3.1）。
type AnnouncementListDTO struct {
	List        []AnnouncementBrief `json:"list"`
	Total       int64               `json:"total"`
	UnreadCount int64               `json:"unread_count"`
}

// UnreadCountDTO 是未读数响应（spec §3.1 unread-count / read）。
type UnreadCountDTO struct {
	UnreadCount int64 `json:"unread_count"`
}

// AnnouncementDetailDTO 是用户端详情响应（spec §3.1）。
// 非 survey 时 Questions 为 []（非 null，匹配 spec "非 survey 时 questions 为 []"）。
type AnnouncementDetailDTO struct {
	ID                uint64        `json:"id"`
	Type              string        `json:"type"`
	Title             string        `json:"title"`
	Content           string        `json:"content"`
	IsImportant       bool          `json:"is_important"`
	PublishedAt       *time.Time    `json:"published_at"`
	ExpiresAt         *time.Time    `json:"expires_at"`
	IsRead            bool          `json:"is_read"`
	IsSurveySubmitted bool          `json:"is_survey_submitted"`
	Questions         []QuestionDTO `json:"questions"`
}

// AdminAnnouncementDTO 是 admin 端公告详情（spec §3.2，含 questions）。
type AdminAnnouncementDTO struct {
	ID          uint64        `json:"id"`
	Type        string        `json:"type"`
	Title       string        `json:"title"`
	Content     string        `json:"content"`
	IsImportant bool          `json:"is_important"`
	Audience    string        `json:"audience"`
	Status      string        `json:"status"`
	PublishedAt *time.Time    `json:"published_at"`
	ExpiresAt   *time.Time    `json:"expires_at"`
	CreatedBy   uint          `json:"created_by"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Questions   []QuestionDTO `json:"questions"`
}

// AdminAnnouncementBrief 是 admin 端列表项（spec §3.2）。
type AdminAnnouncementBrief struct {
	ID            uint64     `json:"id"`
	Type          string     `json:"type"`
	Title         string     `json:"title"`
	Status        string     `json:"status"`
	IsImportant   bool       `json:"is_important"`
	PublishedAt   *time.Time `json:"published_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
	CreatedAt     time.Time  `json:"created_at"`
	ReadCount     int64      `json:"read_count"`
	TargetCount   int64      `json:"target_count"`
	ResponseCount int64      `json:"response_count"`
}

// AdminListDTO 是 admin 端列表响应（spec §3.2）。
type AdminListDTO struct {
	List  []AdminAnnouncementBrief `json:"list"`
	Total int64                    `json:"total"`
}

// StatsDTO 是统计响应（spec §3.2 stats）。
// read_rate/response_rate 为比例（0~1）；read_rate = read/target（target=0 时 0）。
type StatsDTO struct {
	TargetCount   int64   `json:"target_count"`
	ReadCount     int64   `json:"read_count"`
	ReadRate      float64 `json:"read_rate"`
	ResponseCount int64   `json:"response_count"`
	ResponseRate  float64 `json:"response_rate"`
}

// ReaderDTO 是 readers 列表的单行（spec §3.2）。
type ReaderDTO struct {
	UserID   uint       `json:"user_id"`
	Nickname string     `json:"nickname"`
	Phone    string     `json:"phone"`
	ReadAt   *time.Time `json:"read_at"`
}

// ReadersDTO 是 readers 列表响应（spec §3.2）。
type ReadersDTO struct {
	List  []ReaderDTO `json:"list"`
	Total int64       `json:"total"`
}

// OptionCountDTO 单/多选题选项计数（spec §3.2 survey-results）。
type OptionCountDTO struct {
	Option string `json:"option"`
	Count  int64  `json:"count"`
}

// RatingBucketDTO 评分题分布桶（spec §3.2 survey-results）。
type RatingBucketDTO struct {
	Value int   `json:"value"`
	Count int64 `json:"count"`
}

// TextAnswerDTO 文本题单条答案（spec §3.2 survey-results）。
type TextAnswerDTO struct {
	UserID      uint      `json:"user_id"`
	Nickname    string    `json:"nickname"`
	Text        string    `json:"text"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// SurveyQuestionResultDTO 单题聚合（spec §3.2 survey-results.questions[]）。
// 按题型只输出对应字段（其余 omitempty 隐藏），与 spec 示例形状一致：
//   - single/multi → option_counts
//   - rating → distribution + average
//   - text → answers
type SurveyQuestionResultDTO struct {
	QuestionID   uint64            `json:"question_id"`
	Title        string            `json:"title"`
	QuestionType string            `json:"question_type"`
	OptionCounts []OptionCountDTO  `json:"option_counts,omitempty"`
	Distribution []RatingBucketDTO `json:"distribution,omitempty"`
	Average      *float64          `json:"average,omitempty"`
	Answers      []TextAnswerDTO   `json:"answers,omitempty"`
}

// SurveyResultsDTO 是 survey-results 响应（spec §3.2）。
type SurveyResultsDTO struct {
	ResponseCount int64                     `json:"response_count"`
	Questions     []SurveyQuestionResultDTO `json:"questions"`
}

// AnswerDTO 是 responses 下钻里单题答案的对外形状（spec §3.2）。
// ★ remap：store 的 model.SurveyAnswer(answer_options/answer_rating/answer_text)
// → 此处 options/rating/text。
type AnswerDTO struct {
	QuestionID uint64      `json:"question_id"`
	Options    interface{} `json:"options"` // []string 或 null
	Rating     *int        `json:"rating"`
	Text       *string     `json:"text"`
}

// ResponseDTO 是 responses 下钻的单条答卷（spec §3.2）。
type ResponseDTO struct {
	UserID      uint        `json:"user_id"`
	Nickname    string      `json:"nickname"`
	SubmittedAt time.Time   `json:"submitted_at"`
	Answers     []AnswerDTO `json:"answers"`
}

// ResponsesDTO 是 responses 下钻响应（spec §3.2）。
type ResponsesDTO struct {
	List  []ResponseDTO `json:"list"`
	Total int64         `json:"total"`
}

// ============================================================================
// INPUT 结构（无 gin binding tag —— controller 自行绑定 request 后映射进这些）
// ============================================================================

// QuestionInput 是 admin 创建/编辑问卷题目的入参。
// Required 用 *bool：nil → 默认 true（与 DB default:1 一致）；非 nil 时尊重显式值
// （配合 store 的 default-bool fixup，required=false 正确落库）。
type QuestionInput struct {
	OrderIndex   int
	QuestionType string
	Title        string
	Options      []string
	RatingMax    *int
	RatingStyle  *string
	Required     *bool
}

// CreateInput 是 admin 创建公告的入参（spec §3.2 POST）。
type CreateInput struct {
	Type        string
	Title       string
	Content     string
	IsImportant bool
	ExpiresAt   *time.Time
	Status      string // 缺省 'draft'
	Questions   []QuestionInput
}

// UpdateInput 是 admin 更新公告的入参（spec §3.2 PUT）。
// 指针字段 nil 表示"不改"；Questions 非 nil 表示要替换题目（仅 draft 允许）。
type UpdateInput struct {
	Title        *string
	Content      *string
	IsImportant  *bool
	ExpiresAt    *time.Time // 见 ExpiresAtSet：需配合显式 set 标志区分"清空"与"不改"
	ExpiresAtSet bool       // true 时按 ExpiresAt 更新（含置 nil=永不过期）；false 时不改
	Questions    []QuestionInput
}

// ============================================================================
// 接口 + 实现
// ============================================================================

// IAnnouncementBiz 通知中心业务逻辑接口。
type IAnnouncementBiz interface {
	// ---------- 用户端 ----------
	ListForUser(ctx context.Context, userID uint, page, pageSize int) (*AnnouncementListDTO, error)
	UnreadCount(ctx context.Context, userID uint) (*UnreadCountDTO, error)
	DetailForUser(ctx context.Context, userID uint, id uint64) (*AnnouncementDetailDTO, error)
	MarkRead(ctx context.Context, userID uint, id uint64) (*UnreadCountDTO, error)
	SubmitSurvey(ctx context.Context, userID uint, id uint64, answers []AnswerInput) error

	// ---------- admin ----------
	Create(ctx context.Context, adminID uint, in CreateInput) (*AdminAnnouncementDTO, error)
	ListForAdmin(ctx context.Context, status, annType string, page, pageSize int) (*AdminListDTO, error)
	GetForAdmin(ctx context.Context, id uint64) (*AdminAnnouncementDTO, error)
	Update(ctx context.Context, id uint64, in UpdateInput) (*AdminAnnouncementDTO, error)
	Publish(ctx context.Context, id uint64) (*AdminAnnouncementDTO, error)
	Archive(ctx context.Context, id uint64) (*AdminAnnouncementDTO, error)
	Delete(ctx context.Context, id uint64) error
	Stats(ctx context.Context, id uint64) (*StatsDTO, error)
	ListReaders(ctx context.Context, id uint64, status string, page, pageSize int) (*ReadersDTO, error)
	SurveyResults(ctx context.Context, id uint64) (*SurveyResultsDTO, error)
	ListResponses(ctx context.Context, id uint64, page, pageSize int) (*ResponsesDTO, error)
}

type announcementBiz struct {
	store store.IAnnouncementStore
}

var _ IAnnouncementBiz = (*announcementBiz)(nil)

// New 用 IStore 构造（biz 聚合调用，沿用 customerbiz.New(b.ds) 模式）。
func New(ds store.IStore) IAnnouncementBiz {
	return &announcementBiz{store: ds.Announcements()}
}

// NewWithStore 直接注入 announcement store（单测用，避免构造全量 IStore）。
func NewWithStore(s store.IAnnouncementStore) IAnnouncementBiz {
	return &announcementBiz{store: s}
}

// pageOffset 把 1-based page + pageSize 归一为 offset/limit（api-design.md §4）。
func pageOffset(page, pageSize int) (offset, limit int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return (page - 1) * pageSize, pageSize
}

// ============================================================================
// 用户端
// ============================================================================

// ListForUser 组装用户端列表：可见公告 → AnnouncementBrief（含 is_read /
// is_survey_submitted）+ total + unread_count。
//
// N+1 说明：is_read / is_survey_submitted 逐项查 store（V1 列表 N 小，每页 ≤100）。
// 可接受；若日后量大可改批量查询（store 增 BatchIsRead/BatchHasSubmitted）。
func (b *announcementBiz) ListForUser(ctx context.Context, userID uint, page, pageSize int) (*AnnouncementListDTO, error) {
	offset, limit := pageOffset(page, pageSize)
	list, total, err := b.store.ListVisible(ctx, userID, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("ListForUser: list visible: %w", err)
	}

	briefs := make([]AnnouncementBrief, 0, len(list))
	for i := range list {
		ann := &list[i]
		isRead, rerr := b.store.IsRead(ctx, ann.ID, userID)
		if rerr != nil {
			return nil, fmt.Errorf("ListForUser: is read (id=%d): %w", ann.ID, rerr)
		}
		var submitted bool
		if ann.Type == model.AnnouncementTypeSurvey {
			submitted, err = b.store.HasSubmitted(ctx, ann.ID, userID)
			if err != nil {
				return nil, fmt.Errorf("ListForUser: has submitted (id=%d): %w", ann.ID, err)
			}
		}
		briefs = append(briefs, AnnouncementBrief{
			ID:                ann.ID,
			Type:              ann.Type,
			Title:             ann.Title,
			Content:           ann.Content,
			IsImportant:       ann.IsImportant,
			PublishedAt:       ann.PublishedAt,
			ExpiresAt:         ann.ExpiresAt,
			IsRead:            isRead,
			IsSurveySubmitted: submitted,
		})
	}

	unread, err := b.store.CountUnread(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("ListForUser: count unread: %w", err)
	}

	return &AnnouncementListDTO{List: briefs, Total: total, UnreadCount: unread}, nil
}

// UnreadCount 返回当前用户的未读公告数（铃铛轮询用）。
func (b *announcementBiz) UnreadCount(ctx context.Context, userID uint) (*UnreadCountDTO, error) {
	n, err := b.store.CountUnread(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("UnreadCount: %w", err)
	}
	return &UnreadCountDTO{UnreadCount: n}, nil
}

// DetailForUser 返回对用户可见的公告详情（含问卷题目）。
// 不可见（草稿/归档/过期/软删）→ ErrAnnouncementNotFound（由 store.GetVisibleByID 返回）。
// GET 不改已读状态（spec §3.1）。
func (b *announcementBiz) DetailForUser(ctx context.Context, userID uint, id uint64) (*AnnouncementDetailDTO, error) {
	ann, err := b.store.GetVisibleByID(ctx, id)
	if err != nil {
		return nil, err // 透传 ErrAnnouncementNotFound（已是 domain error）
	}

	isRead, err := b.store.IsRead(ctx, id, userID)
	if err != nil {
		return nil, fmt.Errorf("DetailForUser: is read: %w", err)
	}

	var submitted bool
	questions := []QuestionDTO{}
	if ann.Type == model.AnnouncementTypeSurvey {
		submitted, err = b.store.HasSubmitted(ctx, id, userID)
		if err != nil {
			return nil, fmt.Errorf("DetailForUser: has submitted: %w", err)
		}
		qs, qerr := b.store.GetQuestions(ctx, id)
		if qerr != nil {
			return nil, fmt.Errorf("DetailForUser: questions: %w", qerr)
		}
		questions, err = mapQuestions(qs)
		if err != nil {
			return nil, fmt.Errorf("DetailForUser: map questions: %w", err)
		}
	}

	return &AnnouncementDetailDTO{
		ID:                ann.ID,
		Type:              ann.Type,
		Title:             ann.Title,
		Content:           ann.Content,
		IsImportant:       ann.IsImportant,
		PublishedAt:       ann.PublishedAt,
		ExpiresAt:         ann.ExpiresAt,
		IsRead:            isRead,
		IsSurveySubmitted: submitted,
		Questions:         questions,
	}, nil
}

// MarkRead 校验公告对用户可见 → 幂等 upsert 已读回执 → 返回最新 unread_count。
func (b *announcementBiz) MarkRead(ctx context.Context, userID uint, id uint64) (*UnreadCountDTO, error) {
	if _, err := b.store.GetVisibleByID(ctx, id); err != nil {
		return nil, err // ErrAnnouncementNotFound 透传
	}
	if err := b.store.MarkRead(ctx, id, userID); err != nil {
		return nil, fmt.Errorf("MarkRead: %w", err)
	}
	n, err := b.store.CountUnread(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("MarkRead: count unread: %w", err)
	}
	return &UnreadCountDTO{UnreadCount: n}, nil
}

// ============================================================================
// admin
// ============================================================================

// Create 校验入参 → 组装 model.Announcement + 题目 → store.Create → 返回 DTO。
// status='published' 时置 published_at=now；created_by=adminID。
func (b *announcementBiz) Create(ctx context.Context, adminID uint, in CreateInput) (*AdminAnnouncementDTO, error) {
	annType := in.Type
	if annType == "" {
		annType = model.AnnouncementTypePlain
	}
	if annType != model.AnnouncementTypePlain && annType != model.AnnouncementTypeSurvey {
		return nil, errno.ErrSurveyValidation.SetMessage("type 必须为 plain 或 survey")
	}
	if in.Title == "" {
		return nil, errno.ErrSurveyValidation.SetMessage("title 不能为空")
	}

	status := in.Status
	if status == "" {
		status = model.AnnouncementStatusDraft
	}
	if status != model.AnnouncementStatusDraft && status != model.AnnouncementStatusPublished {
		return nil, errno.ErrAnnouncementStatus.SetMessage("status 只能创建为 draft 或 published")
	}

	// 题目校验（survey 必须 ≥1 题；plain 不应携带题目）。
	questions, err := buildQuestions(annType, in.Questions)
	if err != nil {
		return nil, err
	}

	ann := &model.Announcement{
		Type:        annType,
		Title:       in.Title,
		Content:     in.Content,
		IsImportant: in.IsImportant,
		Audience:    model.AnnouncementAudienceAll,
		Status:      status,
		ExpiresAt:   in.ExpiresAt,
		CreatedBy:   adminID,
	}
	if status == model.AnnouncementStatusPublished {
		now := time.Now()
		ann.PublishedAt = &now
	}

	if err := b.store.Create(ctx, ann, questions); err != nil {
		return nil, fmt.Errorf("Create: store: %w", err)
	}

	return b.assembleAdminDTO(ctx, ann)
}

// ListForAdmin 组装 admin 列表：公告 + 每行 read_count/response_count + 全局 target_count。
// target_count 只查一次（spec §3.2 口径）。
func (b *announcementBiz) ListForAdmin(ctx context.Context, status, annType string, page, pageSize int) (*AdminListDTO, error) {
	offset, limit := pageOffset(page, pageSize)
	list, total, err := b.store.ListAll(ctx, status, annType, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("ListForAdmin: list: %w", err)
	}

	target, err := b.store.TargetUserCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListForAdmin: target count: %w", err)
	}

	briefs := make([]AdminAnnouncementBrief, 0, len(list))
	for i := range list {
		ann := &list[i]
		readCount, rerr := b.store.ReadCount(ctx, ann.ID)
		if rerr != nil {
			return nil, fmt.Errorf("ListForAdmin: read count (id=%d): %w", ann.ID, rerr)
		}
		var respCount int64
		if ann.Type == model.AnnouncementTypeSurvey {
			respCount, err = b.store.ResponseCount(ctx, ann.ID)
			if err != nil {
				return nil, fmt.Errorf("ListForAdmin: response count (id=%d): %w", ann.ID, err)
			}
		}
		briefs = append(briefs, AdminAnnouncementBrief{
			ID:            ann.ID,
			Type:          ann.Type,
			Title:         ann.Title,
			Status:        ann.Status,
			IsImportant:   ann.IsImportant,
			PublishedAt:   ann.PublishedAt,
			ExpiresAt:     ann.ExpiresAt,
			CreatedAt:     ann.CreatedAt,
			ReadCount:     readCount,
			TargetCount:   target,
			ResponseCount: respCount,
		})
	}

	return &AdminListDTO{List: briefs, Total: total}, nil
}

// GetForAdmin 返回 admin 端公告详情（含题目）。
func (b *announcementBiz) GetForAdmin(ctx context.Context, id uint64) (*AdminAnnouncementDTO, error) {
	ann, err := b.store.GetByID(ctx, id)
	if err != nil {
		return nil, err // ErrAnnouncementNotFound 透传
	}
	return b.assembleAdminDTO(ctx, ann)
}

// Update 更新 title/content/is_important/expires_at（任意状态）；
// questions 仅 status=draft 可改（published survey 题目冻结，spec §5）。
func (b *announcementBiz) Update(ctx context.Context, id uint64, in UpdateInput) (*AdminAnnouncementDTO, error) {
	ann, err := b.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if in.Title != nil {
		if *in.Title == "" {
			return nil, errno.ErrSurveyValidation.SetMessage("title 不能为空")
		}
		ann.Title = *in.Title
	}
	if in.Content != nil {
		ann.Content = *in.Content
	}
	if in.IsImportant != nil {
		ann.IsImportant = *in.IsImportant
	}
	if in.ExpiresAtSet {
		ann.ExpiresAt = in.ExpiresAt
	}

	if err := b.store.Update(ctx, ann); err != nil {
		return nil, fmt.Errorf("Update: store: %w", err)
	}

	// 题目替换仅 draft 允许。
	if in.Questions != nil {
		if ann.Status != model.AnnouncementStatusDraft {
			return nil, errno.ErrAnnouncementStatus.SetMessage("仅草稿状态可修改问卷题目")
		}
		questions, berr := buildQuestions(ann.Type, in.Questions)
		if berr != nil {
			return nil, berr
		}
		if err := b.store.ReplaceQuestions(ctx, id, questions); err != nil {
			return nil, fmt.Errorf("Update: replace questions: %w", err)
		}
	}

	// 重新读取以拿到最新 updated_at + 题目。
	fresh, err := b.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return b.assembleAdminDTO(ctx, fresh)
}

// Publish 仅 draft→published（否则 ErrAnnouncementStatus），置 published_at=now。
func (b *announcementBiz) Publish(ctx context.Context, id uint64) (*AdminAnnouncementDTO, error) {
	ann, err := b.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if ann.Status != model.AnnouncementStatusDraft {
		return nil, errno.ErrAnnouncementStatus.SetMessage("仅草稿状态可发布")
	}
	now := time.Now()
	if err := b.store.UpdateStatus(ctx, id, model.AnnouncementStatusPublished, &now); err != nil {
		return nil, fmt.Errorf("Publish: %w", err)
	}
	fresh, err := b.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return b.assembleAdminDTO(ctx, fresh)
}

// Archive 置 archived（用户端不再展示）。任意状态可归档。
func (b *announcementBiz) Archive(ctx context.Context, id uint64) (*AdminAnnouncementDTO, error) {
	if _, err := b.store.GetByID(ctx, id); err != nil {
		return nil, err
	}
	if err := b.store.UpdateStatus(ctx, id, model.AnnouncementStatusArchived, nil); err != nil {
		return nil, fmt.Errorf("Archive: %w", err)
	}
	fresh, err := b.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return b.assembleAdminDTO(ctx, fresh)
}

// Delete 软删公告（spec §3.2 DELETE）。
func (b *announcementBiz) Delete(ctx context.Context, id uint64) error {
	if _, err := b.store.GetByID(ctx, id); err != nil {
		return err
	}
	if err := b.store.SoftDelete(ctx, id); err != nil {
		return fmt.Errorf("Delete: %w", err)
	}
	return nil
}

// Stats 实时计算 target/read/response 计数 + 比例（spec §3.2 / §5）。
// read_rate = read/target（target=0 → 0，无除零）；response_rate 仅 survey 有意义
// （plain 公告 response_count=0、response_rate=0）。
func (b *announcementBiz) Stats(ctx context.Context, id uint64) (*StatsDTO, error) {
	ann, err := b.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	target, err := b.store.TargetUserCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("Stats: target count: %w", err)
	}
	readCount, err := b.store.ReadCount(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("Stats: read count: %w", err)
	}

	dto := &StatsDTO{
		TargetCount: target,
		ReadCount:   readCount,
		ReadRate:    ratio(readCount, target),
	}

	if ann.Type == model.AnnouncementTypeSurvey {
		respCount, rerr := b.store.ResponseCount(ctx, id)
		if rerr != nil {
			return nil, fmt.Errorf("Stats: response count: %w", rerr)
		}
		dto.ResponseCount = respCount
		dto.ResponseRate = ratio(respCount, target)
	}

	return dto, nil
}

// ListReaders 列出已读/未读用户（spec §3.2）。status 必须 read|unread。
func (b *announcementBiz) ListReaders(ctx context.Context, id uint64, status string, page, pageSize int) (*ReadersDTO, error) {
	if status != "read" && status != "unread" {
		return nil, errno.ErrSurveyValidation.SetMessage("status 必须为 read 或 unread")
	}
	if _, err := b.store.GetByID(ctx, id); err != nil {
		return nil, err
	}
	offset, limit := pageOffset(page, pageSize)
	rows, total, err := b.store.ListReaders(ctx, id, status, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("ListReaders: %w", err)
	}
	list := make([]ReaderDTO, 0, len(rows))
	for _, r := range rows {
		list = append(list, ReaderDTO{
			UserID:   r.UserID,
			Nickname: r.Nickname,
			Phone:    r.Phone,
			ReadAt:   r.ReadAt,
		})
	}
	return &ReadersDTO{List: list, Total: total}, nil
}

// SurveyResults 组装问卷聚合（spec §3.2 survey-results）。复用 store.SurveyAggregate，
// 按题型映射到 spec JSON 形状。
func (b *announcementBiz) SurveyResults(ctx context.Context, id uint64) (*SurveyResultsDTO, error) {
	if _, err := b.store.GetByID(ctx, id); err != nil {
		return nil, err
	}
	respCount, err := b.store.ResponseCount(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("SurveyResults: response count: %w", err)
	}
	aggs, err := b.store.SurveyAggregate(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("SurveyResults: aggregate: %w", err)
	}

	questions := make([]SurveyQuestionResultDTO, 0, len(aggs))
	for _, a := range aggs {
		q := SurveyQuestionResultDTO{
			QuestionID:   a.QuestionID,
			Title:        a.Title,
			QuestionType: a.QuestionType,
		}
		switch a.QuestionType {
		case model.SurveyQuestionTypeSingle, model.SurveyQuestionTypeMulti:
			q.OptionCounts = make([]OptionCountDTO, 0, len(a.OptionCounts))
			for _, oc := range a.OptionCounts {
				q.OptionCounts = append(q.OptionCounts, OptionCountDTO{Option: oc.Option, Count: oc.Count})
			}
		case model.SurveyQuestionTypeRating:
			q.Distribution = make([]RatingBucketDTO, 0, len(a.Distribution))
			for _, d := range a.Distribution {
				q.Distribution = append(q.Distribution, RatingBucketDTO{Value: d.Value, Count: d.Count})
			}
			avg := a.Average
			q.Average = &avg
		case model.SurveyQuestionTypeText:
			q.Answers = make([]TextAnswerDTO, 0, len(a.TextAnswers))
			for _, t := range a.TextAnswers {
				q.Answers = append(q.Answers, TextAnswerDTO{
					UserID:      t.UserID,
					Nickname:    t.Nickname,
					Text:        t.Text,
					SubmittedAt: t.SubmittedAt,
				})
			}
		}
		questions = append(questions, q)
	}

	return &SurveyResultsDTO{ResponseCount: respCount, Questions: questions}, nil
}

// ListResponses 按用户下钻答卷（spec §3.2）。★ remap store.ResponseRow.Answers
// （model.SurveyAnswer：answer_options/answer_rating/answer_text）→ AnswerDTO
// （options/rating/text）。
func (b *announcementBiz) ListResponses(ctx context.Context, id uint64, page, pageSize int) (*ResponsesDTO, error) {
	if _, err := b.store.GetByID(ctx, id); err != nil {
		return nil, err
	}
	offset, limit := pageOffset(page, pageSize)
	rows, total, err := b.store.ListResponses(ctx, id, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("ListResponses: %w", err)
	}

	list := make([]ResponseDTO, 0, len(rows))
	for _, r := range rows {
		answers := make([]AnswerDTO, 0, len(r.Answers))
		for _, a := range r.Answers {
			opts, derr := decodeOptions(a.AnswerOptions)
			if derr != nil {
				return nil, fmt.Errorf("ListResponses: decode answer_options (q=%d): %w", a.QuestionID, derr)
			}
			answers = append(answers, AnswerDTO{
				QuestionID: a.QuestionID,
				Options:    opts, // nil → JSON null
				Rating:     a.AnswerRating,
				Text:       a.AnswerText,
			})
		}
		list = append(list, ResponseDTO{
			UserID:      r.UserID,
			Nickname:    r.Nickname,
			SubmittedAt: r.SubmittedAt,
			Answers:     answers,
		})
	}

	return &ResponsesDTO{List: list, Total: total}, nil
}

// ============================================================================
// helper
// ============================================================================

// ratio 计算 num/den（den<=0 时返回 0，避免除零）。
func ratio(num, den int64) float64 {
	if den <= 0 {
		return 0
	}
	return float64(num) / float64(den)
}

// assembleAdminDTO 组装 admin 公告 DTO（含题目）。
func (b *announcementBiz) assembleAdminDTO(ctx context.Context, ann *model.Announcement) (*AdminAnnouncementDTO, error) {
	questions := []QuestionDTO{}
	if ann.Type == model.AnnouncementTypeSurvey {
		qs, err := b.store.GetQuestions(ctx, ann.ID)
		if err != nil {
			return nil, fmt.Errorf("assembleAdminDTO: questions: %w", err)
		}
		questions, err = mapQuestions(qs)
		if err != nil {
			return nil, fmt.Errorf("assembleAdminDTO: map questions: %w", err)
		}
	}
	return &AdminAnnouncementDTO{
		ID:          ann.ID,
		Type:        ann.Type,
		Title:       ann.Title,
		Content:     ann.Content,
		IsImportant: ann.IsImportant,
		Audience:    ann.Audience,
		Status:      ann.Status,
		PublishedAt: ann.PublishedAt,
		ExpiresAt:   ann.ExpiresAt,
		CreatedBy:   ann.CreatedBy,
		CreatedAt:   ann.CreatedAt,
		UpdatedAt:   ann.UpdatedAt,
		Questions:   questions,
	}, nil
}
