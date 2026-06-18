package model

import "time"

// 会议副驾 (Meeting Copilot) v1 数据模型（meeting-copilot feature）。
//
// 全新独立模式，代码高度自包含、可整体删除。隔离：仅 features.meeting_copilot.enabled
// 开启时本组表才参与 AutoMigrate（见 helper.go），prod 默认 flag off → 表不出现，零 schema 影响。
//
// 权威 schema 见 migrations/20260617_160000_create_meeting_copilot.sql；列与字段以 SPEC §2 为准。

const (
	// MeetingSessionStatusActive 进行中。
	MeetingSessionStatusActive = "active"
	// MeetingSessionStatusEnded 已结束。
	MeetingSessionStatusEnded = "ended"

	// MeetingSummaryStatusNone 尚未生成纪要。
	MeetingSummaryStatusNone = "none"
	// MeetingSummaryStatusGenerating 纪要生成中。
	MeetingSummaryStatusGenerating = "generating"
	// MeetingSummaryStatusDone 纪要已生成。
	MeetingSummaryStatusDone = "done"
	// MeetingSummaryStatusFailed 纪要生成失败。
	MeetingSummaryStatusFailed = "failed"
	// MeetingSummaryStatusSkipped 用户结束会议时选择不生成纪要。
	MeetingSummaryStatusSkipped = "skipped"

	// MeetingFeedbackTriggerAuto 自动触发（服务端 LLM 判官决定是否给反馈）。
	MeetingFeedbackTriggerAuto = "auto"
	// MeetingFeedbackTriggerManual 手动触发（用户点「现在给我反馈」，总是生成）。
	MeetingFeedbackTriggerManual = "manual"

	// MeetingDiarizationStatusNone 尚未开始说话人分离（DIARIZATION_SPEC §6）。
	MeetingDiarizationStatusNone = "none"
	// MeetingDiarizationStatusOnline 会中在线增量聚类进行中（A/B/C 临时标签）。
	MeetingDiarizationStatusOnline = "online"
	// MeetingDiarizationStatusRefining 会后离线全局重聚类精修中。
	MeetingDiarizationStatusRefining = "refining"
	// MeetingDiarizationStatusDone 离线精修完成（final 标签就绪）。
	MeetingDiarizationStatusDone = "done"
	// MeetingDiarizationStatusFailed 说话人分离失败（软降级，转写不受影响）。
	MeetingDiarizationStatusFailed = "failed"
)

// MeetingSession 会议会话主表（SPEC §2.1）。
type MeetingSession struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// UserID 归属用户。
	UserID uint `gorm:"not null;index:idx_msess_user" json:"user_id"`
	// Title 标题（默认「未命名会议 + 时间」，可后续由首段转写/纪要生成）。
	Title string `gorm:"size:255;not null;default:''" json:"title"`
	// RolePrompt 角色定位 + 反馈规则（自由文本）。
	RolePrompt string `gorm:"type:text;not null" json:"role_prompt"`
	// PresetID 若从预设载入（弱关联，无 FK）。
	PresetID *uint64 `gorm:"column:preset_id" json:"preset_id,omitempty"`
	// Status active / ended。
	Status string `gorm:"size:20;not null;default:'active'" json:"status"`
	// AutoIntervalSeconds 自动反馈最小间隔（秒）。
	AutoIntervalSeconds int `gorm:"not null;default:60" json:"auto_interval_seconds"`
	// RecordingURL 预留（MVP 录音=分段列表，可空）。
	RecordingURL string `gorm:"size:1024" json:"recording_url,omitempty"`
	// DurationSeconds 结束时统计的会议总时长（秒）。
	DurationSeconds int `gorm:"not null;default:0" json:"duration_seconds"`
	// Summary AI 纪要（markdown）。
	Summary string `gorm:"type:mediumtext" json:"summary,omitempty"`
	// RunningSummary 滚动结构化摘要（running memory，FEEDBACK_V2_SPEC §2.1）：会议进行中由后台
	// goroutine 节流折叠出的全局脉络（主题/事实决议/各方立场/未决待办），喂给反馈判官避免幻觉，
	// 也作为最终纪要生成的基底。AutoMigrate 自动补此列（migration 20260618_100000 供手动环境）。
	RunningSummary string `gorm:"type:mediumtext" json:"running_summary,omitempty"`
	// SummaryStatus none / generating / done / failed。
	SummaryStatus string `gorm:"size:20;not null;default:'none'" json:"summary_status"`
	// SpeakerCount 离线精修出的说话人数（DIARIZATION_SPEC §6）；NULL=未精修。
	SpeakerCount *int `gorm:"column:speaker_count" json:"speaker_count,omitempty"`
	// DiarizationStatus 说话人分离状态：none/online/refining/done/failed。无 default:true，规避
	// §database.md 的 default:true bool Create 坑（此处为 string，亦保持显式 default:'none'）。
	DiarizationStatus string `gorm:"size:20;not null;default:'none'" json:"diarization_status"`
	// StartedAt 会议开始时间。
	StartedAt *time.Time `json:"started_at,omitempty"`
	// EndedAt 会议结束时间。
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	CreatedAt time.Time  `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time  `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName 指定表名。
func (MeetingSession) TableName() string { return "meeting_session" }

// MeetingSegment 转写分段（SPEC §2.2，追加型，无软删除）。
type MeetingSegment struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// SessionID 所属会话。
	SessionID uint64 `gorm:"not null;index:idx_mseg_session;index:idx_mseg_session_seq,priority:1" json:"session_id"`
	// Seq 顺序（前端给或后端自增）。
	Seq int `gorm:"not null;index:idx_mseg_session_seq,priority:2" json:"seq"`
	// Text 该段转写文本（可空字符串=静音段）。
	Text string `gorm:"type:text" json:"text"`
	// StartMs 相对会议开始的毫秒偏移（best-effort）。
	StartMs int `gorm:"not null;default:0" json:"start_ms"`
	// DurationSeconds ASR 返回的音频时长（也用于用量参考）。
	DurationSeconds float64 `gorm:"not null;default:0" json:"duration_seconds"`
	// AudioURL 该段音频在 COS 的地址（录音回放用）。
	AudioURL string `gorm:"size:1024" json:"audio_url,omitempty"`
	// OnlineSpeakerID 在线增量聚类临时簇号（DIARIZATION_SPEC §6）；NULL=声纹未就绪/软降级。
	OnlineSpeakerID *int `gorm:"column:online_speaker_id" json:"online_speaker_id,omitempty"`
	// OnlineProvisional 灰区暂定标（provisional，不更新质心）。无 default:true，规避 §database.md 的
	// default:true bool Create 坑（false 须如实持久化）。
	OnlineProvisional bool `gorm:"column:online_provisional;not null;default:0" json:"online_provisional"`
	// FinalSpeakerID 离线全局重聚类稳定簇号；NULL=尚未精修。
	FinalSpeakerID *int `gorm:"column:final_speaker_id" json:"final_speaker_id,omitempty"`
	// SpeakerConfidence 说话人聚类置信度；NULL=未知。
	SpeakerConfidence *float32  `gorm:"column:speaker_confidence" json:"speaker_confidence,omitempty"`
	CreatedAt         time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName 指定表名。
func (MeetingSegment) TableName() string { return "meeting_segment" }

// MeetingFeedback 反馈事件（SPEC §2.3，追加型，无软删除）。
type MeetingFeedback struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// SessionID 所属会话。
	SessionID uint64 `gorm:"not null;index:idx_mfb_session" json:"session_id"`
	// Trigger auto / manual。`trigger` 是 MySQL 保留字，显式 column 名带反引号由 GORM 处理。
	Trigger string `gorm:"column:trigger;size:10;not null" json:"trigger"`
	// AnchorSeq 生成时转写进度锚点。
	AnchorSeq int `gorm:"not null;default:0" json:"anchor_seq"`
	// Content 反馈正文（markdown）。
	Content   string    `gorm:"type:text" json:"content"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName 指定表名。
func (MeetingFeedback) TableName() string { return "meeting_feedback" }

// MeetingPreset 角色预设（SPEC §2.4）。user_id=0 为系统内置模板（is_builtin=1，不可删）。
type MeetingPreset struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// UserID 归属用户，0 = 系统内置模板。
	UserID uint `gorm:"not null;index:idx_mpreset_user" json:"user_id"`
	// Name 预设名称。
	Name string `gorm:"size:100;not null" json:"name"`
	// RolePrompt 角色定位 + 反馈规则。
	RolePrompt string `gorm:"type:text;not null" json:"role_prompt"`
	// AutoIntervalSeconds 自动反馈最小间隔（秒）。
	AutoIntervalSeconds int `gorm:"not null;default:60" json:"auto_interval_seconds"`
	// IsBuiltin 系统内置不可删。
	IsBuiltin bool      `gorm:"column:is_builtin;not null;default:0" json:"is_builtin"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName 指定表名。
func (MeetingPreset) TableName() string { return "meeting_preset" }

// MeetingSpeaker 离线全局重聚类产物：出场序稳定编号 + 色板映射（DIARIZATION_SPEC §6）。
// 出场序编号须幂等（重试不漂移）；与 MeetingSession 弱关联无 FK。
type MeetingSpeaker struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// MeetingID 所属会话（弱关联，无 FK）。
	MeetingID uint64 `gorm:"not null;uniqueIndex:uk_ms_meeting_cluster,priority:1;index:idx_ms_meeting" json:"meeting_id"`
	// ClusterID 离线重聚类簇号。
	ClusterID int `gorm:"not null;uniqueIndex:uk_ms_meeting_cluster,priority:2" json:"cluster_id"`
	// DisplayLabel 展示编号（出场序 1/2/3…）。
	DisplayLabel string `gorm:"size:32;not null" json:"display_label"`
	// ColorIndex 前端取色板下标。
	ColorIndex int       `gorm:"not null" json:"color_index"`
	CreatedAt  time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName 指定表名。
func (MeetingSpeaker) TableName() string { return "meeting_speaker" }

// MeetingSegmentEmbedding 逐段 192-d 声纹 embedding 落库（DIARIZATION_SPEC §6，离线 AHC 重聚类
// 主路径前提 P0-2）。Embedding 为 float32×192 packed 字节（BLOB）；与 segment/session 弱关联无 FK。
type MeetingSegmentEmbedding struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// MeetingID 所属会话（弱关联，无 FK）。
	MeetingID uint64 `gorm:"not null;index:idx_mse_meeting" json:"meeting_id"`
	// SegmentID 所属分段（弱关联，无 FK）。
	SegmentID uint64 `gorm:"not null;uniqueIndex:uk_mse_segment" json:"segment_id"`
	// Embedding float32×192 packed 字节。type:longblob 对齐项目现有 agent_session_memory.embedding 约定。
	Embedding []byte    `gorm:"type:longblob;not null" json:"embedding"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName 指定表名。
func (MeetingSegmentEmbedding) TableName() string { return "meeting_segment_embedding" }
