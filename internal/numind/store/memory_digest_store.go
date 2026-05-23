package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/model"
)

// IMemoryDigestStore 定义 4 张 user_memory_digest_* 表的存取接口
// (Task 3.8 分层时间感知).
//
// D7 (拍板规则): B2B2C 父子账户 memory **完全隔离**
//   - 所有查询签名只接受 userID 单参数 (不接受 parentUserID)
//   - 调用方在 controller / cron 层 enforce 单用户隔离
//   - 父账户**不**聚合子账户 digest (这是有意识的 trade-off — 见 spec §风险 R5)
//
// Idempotency 设计:
//   - Upsert* 用 ON DUPLICATE KEY UPDATE (cron 重跑覆盖, generated_at 刷新)
//   - 4 张表的 UNIQUE KEY 保证 (user_id, period) 维度唯一性
//   - cron 多实例双跑通过外层 Redis SETNX 防止 (见 digest_cron.go), 不依赖 DB unique
type IMemoryDigestStore interface {
	// UpsertDaily 写入或覆盖一行 daily digest (UNIQUE 冲突 user_id+digest_date)
	UpsertDaily(ctx context.Context, d *model.UserMemoryDigestDaily) error
	// UpsertWeekly 写入或覆盖一行 weekly digest (UNIQUE 冲突 user_id+iso_year+iso_week)
	UpsertWeekly(ctx context.Context, d *model.UserMemoryDigestWeekly) error
	// UpsertMonthly 写入或覆盖一行 monthly digest (UNIQUE 冲突 user_id+year+month)
	UpsertMonthly(ctx context.Context, d *model.UserMemoryDigestMonthly) error
	// UpsertQuarterly 写入或覆盖一行 quarterly digest (UNIQUE 冲突 user_id+year+quarter)
	UpsertQuarterly(ctx context.Context, d *model.UserMemoryDigestQuarterly) error

	// GetDaily 按 user_id + date 查单条 daily digest;
	// 不存在返回 gorm.ErrRecordNotFound (调用方判 errors.Is + 跳过注入).
	GetDaily(ctx context.Context, userID uint, date time.Time) (*model.UserMemoryDigestDaily, error)
	// GetDailyRange 按 user_id + [from,to] 日期窗口查 daily digest 列表
	// (升序 by digest_date). from/to 均包含.
	GetDailyRange(ctx context.Context, userID uint, from, to time.Time) ([]*model.UserMemoryDigestDaily, error)
	// GetWeekly 按 user_id + (isoYear, isoWeek) 查单条 weekly digest;
	// 不存在返回 gorm.ErrRecordNotFound.
	GetWeekly(ctx context.Context, userID uint, isoYear, isoWeek int) (*model.UserMemoryDigestWeekly, error)
	// GetWeeklyRange 按 user_id + [fromYW, toYW] (含) 查 weekly digest 列表.
	// fromYW / toYW 是 [iso_year, iso_week] 二元组, 按 (iso_year, iso_week) lexicographic order 比较.
	GetWeeklyRange(ctx context.Context, userID uint, fromYW, toYW [2]int) ([]*model.UserMemoryDigestWeekly, error)
	// GetMonthly 按 user_id + (year, month) 查单条 monthly digest;
	// 不存在返回 gorm.ErrRecordNotFound.
	GetMonthly(ctx context.Context, userID uint, year, month int) (*model.UserMemoryDigestMonthly, error)
	// GetMonthlyRange 按 user_id + [fromYM, toYM] (含) 查 monthly digest 列表.
	// fromYM / toYM 是 [year, month] 二元组.
	GetMonthlyRange(ctx context.Context, userID uint, fromYM, toYM [2]int) ([]*model.UserMemoryDigestMonthly, error)
	// GetQuarterly 按 user_id + (year, quarter) 查单条 quarterly digest;
	// 不存在返回 gorm.ErrRecordNotFound.
	GetQuarterly(ctx context.Context, userID uint, year, quarter int) (*model.UserMemoryDigestQuarterly, error)

	// GetUsersActiveOn 返回在指定日期 (Asia/Shanghai) 有 agent_run 活动的 user_id 列表
	// (从 agent_run.started_at 推断). cron 阶段 1 过滤掉 0 活动用户 — 不浪费 LLM 调用.
	// date 的"日"按 Asia/Shanghai 划分: [date 00:00:00, date+1 00:00:00).
	GetUsersActiveOn(ctx context.Context, date time.Time) ([]uint, error)
	// GetUsersActiveInRange 返回在 [from, to] 时间窗口 (UTC compare on started_at)
	// 有 agent_run 活动的 user_id 列表.
	// 用于 weekly/monthly/quarterly cron 阶段 1 过滤.
	GetUsersActiveInRange(ctx context.Context, from, to time.Time) ([]uint, error)

	// ListAgentRunsByUserDateRange 返回某 user 在 [from, to) (start-inclusive,
	// end-exclusive) 时间窗口内**已完成 (status='terminated')** 的 agent_run 列表
	// (按 started_at ASC).
	// 用于 daily digest 阶段 2: 取昨日所有已完成 agent_run 喂给 LLM 总结.
	// 限制: 单 user 单日 ≤ 200 个 run (超过 truncate, 防 pathological 用户).
	//
	// 仅返回 status='terminated' 的 run (spec §Daily-digest-输入构造):
	// 未完成的 running / cancelled / failed 状态 run 不应进入 daily digest, 否则
	// summary 会基于不完整对话 (例如 mid-stream 中断的 message 列表).
	ListAgentRunsByUserDateRange(ctx context.Context, userID uint, from, to time.Time) ([]*model.AgentRun, error)
}

// memoryDigestStore 是 IMemoryDigestStore 的具体实现.
type memoryDigestStore struct {
	db *gorm.DB
}

var _ IMemoryDigestStore = (*memoryDigestStore)(nil)

// NewMemoryDigestStore 构造 IMemoryDigestStore 实例.
func NewMemoryDigestStore(db *gorm.DB) IMemoryDigestStore {
	return &memoryDigestStore{db: db}
}

// ─── Upsert helpers ──────────────────────────────────────────────────────────

// digestDailyUpdateCols 列出 ON DUPLICATE KEY UPDATE 时刷新的字段
// (排除 created/PK; 包含 generated_at 让 cron 重跑刷新时间戳).
var digestDailyUpdateCols = []string{
	"session_count", "message_count", "extracted_facts_count",
	"summary", "key_topics", "llm_cost_credits", "generated_at",
}

func (s *memoryDigestStore) UpsertDaily(ctx context.Context, d *model.UserMemoryDigestDaily) error {
	if d == nil {
		return fmt.Errorf("memoryDigestStore.UpsertDaily: nil digest")
	}
	if d.UserID == 0 {
		return fmt.Errorf("memoryDigestStore.UpsertDaily: UserID required")
	}
	if err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "digest_date"}},
			DoUpdates: clause.AssignmentColumns(digestDailyUpdateCols),
		}).
		Create(d).Error; err != nil {
		return fmt.Errorf("memoryDigestStore.UpsertDaily(userID=%d, date=%s): %w",
			d.UserID, d.DigestDate.Format("2006-01-02"), err)
	}
	return nil
}

var digestWeeklyUpdateCols = []string{
	"week_start_date", "week_end_date", "summary", "key_topics", "generated_at",
}

func (s *memoryDigestStore) UpsertWeekly(ctx context.Context, d *model.UserMemoryDigestWeekly) error {
	if d == nil {
		return fmt.Errorf("memoryDigestStore.UpsertWeekly: nil digest")
	}
	if d.UserID == 0 {
		return fmt.Errorf("memoryDigestStore.UpsertWeekly: UserID required")
	}
	if err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "iso_year"}, {Name: "iso_week"}},
			DoUpdates: clause.AssignmentColumns(digestWeeklyUpdateCols),
		}).
		Create(d).Error; err != nil {
		return fmt.Errorf("memoryDigestStore.UpsertWeekly(userID=%d, yw=%d-W%d): %w",
			d.UserID, d.ISOYear, d.ISOWeek, err)
	}
	return nil
}

var digestMonthlyUpdateCols = []string{
	"summary", "key_topics", "generated_at",
}

func (s *memoryDigestStore) UpsertMonthly(ctx context.Context, d *model.UserMemoryDigestMonthly) error {
	if d == nil {
		return fmt.Errorf("memoryDigestStore.UpsertMonthly: nil digest")
	}
	if d.UserID == 0 {
		return fmt.Errorf("memoryDigestStore.UpsertMonthly: UserID required")
	}
	if err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "year"}, {Name: "month"}},
			DoUpdates: clause.AssignmentColumns(digestMonthlyUpdateCols),
		}).
		Create(d).Error; err != nil {
		return fmt.Errorf("memoryDigestStore.UpsertMonthly(userID=%d, ym=%d-%02d): %w",
			d.UserID, d.Year, d.Month, err)
	}
	return nil
}

func (s *memoryDigestStore) UpsertQuarterly(ctx context.Context, d *model.UserMemoryDigestQuarterly) error {
	if d == nil {
		return fmt.Errorf("memoryDigestStore.UpsertQuarterly: nil digest")
	}
	if d.UserID == 0 {
		return fmt.Errorf("memoryDigestStore.UpsertQuarterly: UserID required")
	}
	if err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "year"}, {Name: "quarter"}},
			DoUpdates: clause.AssignmentColumns(digestMonthlyUpdateCols), // 同 monthly: summary/key_topics/generated_at
		}).
		Create(d).Error; err != nil {
		return fmt.Errorf("memoryDigestStore.UpsertQuarterly(userID=%d, yq=%dQ%d): %w",
			d.UserID, d.Year, d.Quarter, err)
	}
	return nil
}

// ─── Get / Range helpers ─────────────────────────────────────────────────────

func (s *memoryDigestStore) GetDaily(ctx context.Context, userID uint, date time.Time) (*model.UserMemoryDigestDaily, error) {
	// Snap to midnight in the date's location so the equality compare against
	// MySQL DATE / SQLite DATETIME works across both backends.
	day := snapToMidnight(date)
	dayEnd := day.Add(24 * time.Hour)
	var d model.UserMemoryDigestDaily
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND digest_date >= ? AND digest_date < ?", userID, day, dayEnd).
		First(&d).Error; err != nil {
		return nil, fmt.Errorf("memoryDigestStore.GetDaily(userID=%d, date=%s): %w", userID, day.Format("2006-01-02"), err)
	}
	return &d, nil
}

func (s *memoryDigestStore) GetDailyRange(ctx context.Context, userID uint, from, to time.Time) ([]*model.UserMemoryDigestDaily, error) {
	fromDay := snapToMidnight(from)
	toDay := snapToMidnight(to).Add(24 * time.Hour) // exclusive upper bound
	var rows []*model.UserMemoryDigestDaily
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND digest_date >= ? AND digest_date < ?", userID, fromDay, toDay).
		Order("digest_date ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("memoryDigestStore.GetDailyRange(userID=%d, %s..%s): %w",
			userID, fromDay.Format("2006-01-02"), toDay.Format("2006-01-02"), err)
	}
	return rows, nil
}

// snapToMidnight returns the start-of-day of t in t's location.
func snapToMidnight(t time.Time) time.Time {
	loc := t.Location()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

func (s *memoryDigestStore) GetWeekly(ctx context.Context, userID uint, isoYear, isoWeek int) (*model.UserMemoryDigestWeekly, error) {
	var d model.UserMemoryDigestWeekly
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND iso_year = ? AND iso_week = ?", userID, isoYear, isoWeek).
		First(&d).Error; err != nil {
		return nil, fmt.Errorf("memoryDigestStore.GetWeekly(userID=%d, %d-W%d): %w",
			userID, isoYear, isoWeek, err)
	}
	return &d, nil
}

// GetWeeklyRange returns weekly rows in [fromYW, toYW] inclusive, ordered
// (iso_year ASC, iso_week ASC). Lexicographic order on (year, week) is
// implemented as: (iso_year > from.year) OR (iso_year == from.year AND iso_week >= from.week)
// combined with upper bound.
func (s *memoryDigestStore) GetWeeklyRange(ctx context.Context, userID uint, fromYW, toYW [2]int) ([]*model.UserMemoryDigestWeekly, error) {
	var rows []*model.UserMemoryDigestWeekly
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Where("(iso_year > ?) OR (iso_year = ? AND iso_week >= ?)", fromYW[0], fromYW[0], fromYW[1]).
		Where("(iso_year < ?) OR (iso_year = ? AND iso_week <= ?)", toYW[0], toYW[0], toYW[1]).
		Order("iso_year ASC, iso_week ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("memoryDigestStore.GetWeeklyRange(userID=%d, %d-W%d..%d-W%d): %w",
			userID, fromYW[0], fromYW[1], toYW[0], toYW[1], err)
	}
	return rows, nil
}

func (s *memoryDigestStore) GetMonthly(ctx context.Context, userID uint, year, month int) (*model.UserMemoryDigestMonthly, error) {
	var d model.UserMemoryDigestMonthly
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND year = ? AND month = ?", userID, year, month).
		First(&d).Error; err != nil {
		return nil, fmt.Errorf("memoryDigestStore.GetMonthly(userID=%d, %d-%02d): %w",
			userID, year, month, err)
	}
	return &d, nil
}

func (s *memoryDigestStore) GetMonthlyRange(ctx context.Context, userID uint, fromYM, toYM [2]int) ([]*model.UserMemoryDigestMonthly, error) {
	var rows []*model.UserMemoryDigestMonthly
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Where("(year > ?) OR (year = ? AND month >= ?)", fromYM[0], fromYM[0], fromYM[1]).
		Where("(year < ?) OR (year = ? AND month <= ?)", toYM[0], toYM[0], toYM[1]).
		Order("year ASC, month ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("memoryDigestStore.GetMonthlyRange(userID=%d, %d-%02d..%d-%02d): %w",
			userID, fromYM[0], fromYM[1], toYM[0], toYM[1], err)
	}
	return rows, nil
}

func (s *memoryDigestStore) GetQuarterly(ctx context.Context, userID uint, year, quarter int) (*model.UserMemoryDigestQuarterly, error) {
	var d model.UserMemoryDigestQuarterly
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND year = ? AND quarter = ?", userID, year, quarter).
		First(&d).Error; err != nil {
		return nil, fmt.Errorf("memoryDigestStore.GetQuarterly(userID=%d, %dQ%d): %w",
			userID, year, quarter, err)
	}
	return &d, nil
}

// ─── Active-user lookup (cron stage 1) ───────────────────────────────────────

// GetUsersActiveOn queries agent_run for distinct user_ids that have a started_at
// inside the (date 00:00:00, date+1d 00:00:00) window in the date's timezone.
// Returns an empty (non-nil) slice when no active users.
func (s *memoryDigestStore) GetUsersActiveOn(ctx context.Context, date time.Time) ([]uint, error) {
	// Snap date to local midnight in its timezone, then [start, end) 1-day window.
	loc := date.Location()
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	end := start.Add(24 * time.Hour)
	return s.GetUsersActiveInRange(ctx, start, end)
}

// GetUsersActiveInRange queries agent_run for distinct user_ids that have a
// started_at inside [from, to). Returns an empty (non-nil) slice when no active.
//
// Important: filters user_id > 0 to exclude anonymous / system-issued runs
// (cron should not generate digests for unauthenticated sessions).
func (s *memoryDigestStore) GetUsersActiveInRange(ctx context.Context, from, to time.Time) ([]uint, error) {
	var ids []uint
	if err := s.db.WithContext(ctx).
		Model(&model.AgentRun{}).
		Where("started_at >= ? AND started_at < ? AND user_id > 0", from, to).
		Distinct("user_id").
		Pluck("user_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("memoryDigestStore.GetUsersActiveInRange(%s..%s): %w",
			from.Format(time.RFC3339), to.Format(time.RFC3339), err)
	}
	if ids == nil {
		ids = []uint{}
	}
	return ids, nil
}

// digestMaxRunsPerUserPerDay is the safety cap for daily digest input — protects
// against pathological users with thousands of micro-runs blowing up the LLM
// prompt token budget. 200 runs × ~200 chars sessions_brief ≈ 40K chars ≈ 60K
// tokens, well under qwen-plus 128K context.
const digestMaxRunsPerUserPerDay = 200

func (s *memoryDigestStore) ListAgentRunsByUserDateRange(ctx context.Context, userID uint, from, to time.Time) ([]*model.AgentRun, error) {
	var runs []*model.AgentRun
	if err := s.db.WithContext(ctx).
		Model(&model.AgentRun{}).
		// status='terminated' filter: spec §Daily-digest-输入构造 requires only
		// completed runs feed the daily summary. Aligns with the canonical
		// terminal status string used throughout biz/agent/runner.go.
		Where("user_id = ? AND status = ? AND started_at >= ? AND started_at < ?",
			userID, "terminated", from, to).
		Order("started_at ASC").
		Limit(digestMaxRunsPerUserPerDay).
		Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("memoryDigestStore.ListAgentRunsByUserDateRange(userID=%d, %s..%s): %w",
			userID, from.Format(time.RFC3339), to.Format(time.RFC3339), err)
	}
	return runs, nil
}
