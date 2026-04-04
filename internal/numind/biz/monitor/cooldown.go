package monitor

import (
	"sync"
	"time"
)

// CooldownManager 基于内存的冷却时间管理器，使用 sync.Map 实现并发安全
type CooldownManager struct {
	checkCooldowns   sync.Map // userID (uint) → time.Time
	analyzeCooldowns sync.Map // noteID (uint) → time.Time
	checkMinutes     int
	analyzeMinutes   int
}

// NewCooldownManager 创建冷却时间管理器
func NewCooldownManager(checkMinutes, analyzeMinutes int) *CooldownManager {
	return &CooldownManager{
		checkMinutes:   checkMinutes,
		analyzeMinutes: analyzeMinutes,
	}
}

// CanCheck 检查用户是否可以执行检查操作（冷却期已过）
func (cm *CooldownManager) CanCheck(userID uint) bool {
	if v, ok := cm.checkCooldowns.Load(userID); ok {
		return time.Since(v.(time.Time)) > time.Duration(cm.checkMinutes)*time.Minute
	}
	return true
}

// RecordCheck 记录用户执行检查操作的时间
func (cm *CooldownManager) RecordCheck(userID uint) {
	cm.checkCooldowns.Store(userID, time.Now())
}

// CanAnalyze 检查笔记是否可以执行 AI 分析（冷却期已过）
func (cm *CooldownManager) CanAnalyze(noteID uint) bool {
	if v, ok := cm.analyzeCooldowns.Load(noteID); ok {
		return time.Since(v.(time.Time)) > time.Duration(cm.analyzeMinutes)*time.Minute
	}
	return true
}

// RecordAnalyze 记录笔记执行 AI 分析的时间
func (cm *CooldownManager) RecordAnalyze(noteID uint) {
	cm.analyzeCooldowns.Store(noteID, time.Now())
}
