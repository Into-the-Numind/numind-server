package model

import (
	"time"

	"gorm.io/datatypes"
)

// UserMemoryDigestDaily 是 Agent Mode V1.5 Layer A 的日级 digest 表
// (Task 3.8 分层时间感知).
//
// 每用户每日一行：cron 每日 04:00 (Asia/Shanghai) 跑, 聚合昨日所有 agent_run /
// messages → aiservice.Chat(profile.AgentDigest) 生成 100-200 字第三人称总结
// + 3-5 个关键主题. UNIQUE (user_id, digest_date) 保证 UPSERT 幂等 (cron 可重跑).
//
// Layer A only: digest 内容描述 user 本人的活动总结 (跨 session aggregate),
// 不引入 Layer B subject 概念 (V2 扩展). D7 父子完全隔离: 每个 user_id 独立
// digest series, 父账户**不**聚合子账户.
type UserMemoryDigestDaily struct {
	ID                  uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID              uint           `gorm:"not null;uniqueIndex:uniq_user_date,priority:1" json:"user_id"`
	DigestDate          time.Time      `gorm:"type:date;not null;uniqueIndex:uniq_user_date,priority:2;index:idx_date" json:"digest_date"`
	SessionCount        int            `gorm:"not null;default:0" json:"session_count"`
	MessageCount        int            `gorm:"not null;default:0" json:"message_count"`
	ExtractedFactsCount int            `gorm:"not null;default:0" json:"extracted_facts_count"`
	Summary             string         `gorm:"type:text" json:"summary"`
	KeyTopics           datatypes.JSON `gorm:"type:json" json:"key_topics"`
	LLMCostCredits      int            `gorm:"not null;default:0" json:"llm_cost_credits"`
	GeneratedAt         time.Time      `gorm:"not null;autoCreateTime" json:"generated_at"`
}

// TableName overrides GORM's default plural naming.
func (UserMemoryDigestDaily) TableName() string { return "user_memory_digest_daily" }

// UserMemoryDigestWeekly 是 Agent Mode V1.5 Layer A 的周级 digest 表
// (Task 3.8 分层时间感知).
//
// 每用户每 ISO 周一行：cron 每周一 04:30 (Asia/Shanghai) 跑, 聚合上周 7 天
// daily digest → aiservice.Chat(profile.AgentDigest) 生成 200-300 字综合归纳.
//
// 注意: 用 ISO 周 (iso_year, iso_week) 而不是自然年 — 跨年 ISO 周边界 case
// 如 2026-01-01 算 2026-W01, 不是 2025-W53 (spec §边界 case).
type UserMemoryDigestWeekly struct {
	ID            uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID        uint           `gorm:"not null;uniqueIndex:uniq_user_week,priority:1" json:"user_id"`
	ISOYear       int            `gorm:"not null;uniqueIndex:uniq_user_week,priority:2;column:iso_year" json:"iso_year"`
	ISOWeek       int            `gorm:"not null;uniqueIndex:uniq_user_week,priority:3;column:iso_week" json:"iso_week"`
	WeekStartDate time.Time      `gorm:"type:date;not null;index:idx_week_range" json:"week_start_date"`
	WeekEndDate   time.Time      `gorm:"type:date;not null" json:"week_end_date"`
	Summary       string         `gorm:"type:text" json:"summary"`
	KeyTopics     datatypes.JSON `gorm:"type:json" json:"key_topics"`
	GeneratedAt   time.Time      `gorm:"not null;autoCreateTime" json:"generated_at"`
}

// TableName overrides GORM's default plural naming.
func (UserMemoryDigestWeekly) TableName() string { return "user_memory_digest_weekly" }

// UserMemoryDigestMonthly 是 Agent Mode V1.5 Layer A 的月级 digest 表
// (Task 3.8 分层时间感知).
//
// 每用户每月一行：cron 每月 1 号 04:30 (Asia/Shanghai) 跑, 聚合上月所有
// weekly digest → aiservice.Chat(profile.AgentDigest) 生成 200-300 字综合归纳.
type UserMemoryDigestMonthly struct {
	ID          uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID      uint           `gorm:"not null;uniqueIndex:uniq_user_month,priority:1" json:"user_id"`
	Year        int            `gorm:"not null;uniqueIndex:uniq_user_month,priority:2" json:"year"`
	Month       int            `gorm:"not null;uniqueIndex:uniq_user_month,priority:3" json:"month"`
	Summary     string         `gorm:"type:text" json:"summary"`
	KeyTopics   datatypes.JSON `gorm:"type:json" json:"key_topics"`
	GeneratedAt time.Time      `gorm:"not null;autoCreateTime" json:"generated_at"`
}

// TableName overrides GORM's default plural naming.
func (UserMemoryDigestMonthly) TableName() string { return "user_memory_digest_monthly" }

// UserMemoryDigestQuarterly 是 Agent Mode V1.5 Layer A 的季度级 digest 表
// (Task 3.8 分层时间感知).
//
// 每用户每季度一行：cron 季度首日 04:30 (Asia/Shanghai) 跑, 聚合上季度所有
// monthly digest → aiservice.Chat(profile.AgentDigest) 生成 200-300 字综合归纳.
// 自然季: Q1=Jan-Mar / Q2=Apr-Jun / Q3=Jul-Sep / Q4=Oct-Dec.
type UserMemoryDigestQuarterly struct {
	ID          uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID      uint           `gorm:"not null;uniqueIndex:uniq_user_quarter,priority:1" json:"user_id"`
	Year        int            `gorm:"not null;uniqueIndex:uniq_user_quarter,priority:2" json:"year"`
	Quarter     int            `gorm:"not null;uniqueIndex:uniq_user_quarter,priority:3" json:"quarter"`
	Summary     string         `gorm:"type:text" json:"summary"`
	KeyTopics   datatypes.JSON `gorm:"type:json" json:"key_topics"`
	GeneratedAt time.Time      `gorm:"not null;autoCreateTime" json:"generated_at"`
}

// TableName overrides GORM's default plural naming.
func (UserMemoryDigestQuarterly) TableName() string { return "user_memory_digest_quarterly" }
