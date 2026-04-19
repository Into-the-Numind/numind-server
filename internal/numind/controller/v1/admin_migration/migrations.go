// Package admin_migration hosts one-shot migration HTTP handlers.
// It currently exposes the Grandfathering billing-mode initialization
// (spec §4.4.3) — a manually-triggered admin action that flips in-period
// users from billing_mode=credits to legacy_tier. Idempotency is enforced
// via a status-check endpoint and a DB-level guard (WHERE billing_mode='credits').
package admin_migration

import (
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// MigrationController owns the billing-mode-init endpoints. It holds only the
// store — no biz layer — because this is an infrastructure-level migration,
// not a business workflow. The action is audited via action_log.
type MigrationController struct {
	ds store.IStore
}

// NewMigrationController constructs the controller.
func NewMigrationController(ds store.IStore) *MigrationController {
	return &MigrationController{ds: ds}
}

// ----------------------------------------------------------------------------
// Response DTOs (spec §4.4.3)
// ----------------------------------------------------------------------------

// MigrationStatsPerTier records how many users fall into each tier before
// migration, used by the status endpoint to drive the admin UI's PENDING
// state ("待迁移用户 N 人，分布：standard X / premium Y / trial Z").
type MigrationStatsPerTier struct {
	Trial          int64 `json:"trial"`
	Standard       int64 `json:"standard"`
	Premium        int64 `json:"premium"`
	Free           int64 `json:"free"`
	TotalInPeriod  int64 `json:"total_in_period"` // sum of trial+standard+premium in period
}

// MigrationStatusResp is the response body of the status endpoint. Only one of
// {PreMigrationStats, ExecutedAt+ExecutedBy+MigratedCount} is meaningful
// depending on AlreadyExecuted.
type MigrationStatusResp struct {
	AlreadyExecuted   bool                   `json:"already_executed"`
	ExecutedAt        *time.Time             `json:"executed_at,omitempty"`
	ExecutedBy        *string                `json:"executed_by,omitempty"`
	MigratedCount     int64                  `json:"migrated_count"`
	PreMigrationStats *MigrationStatsPerTier `json:"pre_migration_stats,omitempty"`
}

// MigrationRunResp is the response body of the POST (execute) endpoint.
type MigrationRunResp struct {
	MigratedCount int64     `json:"migrated_count"`
	ExecutedAt    time.Time `json:"executed_at"`
	ExecutedBy    string    `json:"executed_by"`
}

// ----------------------------------------------------------------------------
// Status endpoint (spec §4.4.3)
// ----------------------------------------------------------------------------

// GetInitStatus GET /v1/admin/migrations/billing-mode-init/status
//
// State machine:
//   - AlreadyExecuted=true  when COUNT(user WHERE billing_mode='legacy_tier') > 0.
//   - Otherwise returns PreMigrationStats for the admin UI's PENDING view.
//
// ExecutedAt + ExecutedBy are best-effort — they come from the latest
// action_log entry with the migration action key. Absent rows → nil.
func (m *MigrationController) GetInitStatus(ctx *gin.Context) {
	// Count legacy_tier users
	var legacyCount int64
	if err := m.ds.DB().WithContext(ctx).
		Model(&model.User{}).
		Where("billing_mode = ?", model.BillingModeLegacyTier).
		Count(&legacyCount).Error; err != nil {
		log.C(ctx).Errorw("migration status: count legacy_tier failed", "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	resp := MigrationStatusResp{
		AlreadyExecuted: legacyCount > 0,
		MigratedCount:   legacyCount,
	}

	if resp.AlreadyExecuted {
		// Pull the latest action_log entry for the migration action (best-effort).
		var row model.ActionLogM
		err := m.ds.DB().WithContext(ctx).
			Where("action = ?", actionBillingModeInit).
			Order("created_at DESC").
			Limit(1).
			Take(&row).Error
		if err == nil {
			ts := row.CreatedAt
			resp.ExecutedAt = &ts
			// Look up admin username. target_id is the admin user ID.
			if row.TargetID != nil {
				if u, uerr := m.ds.Users().GetUserByID(ctx, *row.TargetID); uerr == nil {
					name := u.Username
					resp.ExecutedBy = &name
				}
			}
		}
		core.WriteResponse(ctx, nil, resp)
		return
	}

	// PENDING — compute pre_migration_stats
	stats, err := m.computePreMigrationStats(ctx)
	if err != nil {
		log.C(ctx).Errorw("migration status: compute stats failed", "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}
	resp.PreMigrationStats = stats
	core.WriteResponse(ctx, nil, resp)
}

// computePreMigrationStats runs the eligible-user distribution query used by
// the PENDING state card. Mirrors the "Step 1" audit query in the migration
// SQL file — tier_expires > NOW() defines "in period".
func (m *MigrationController) computePreMigrationStats(ctx *gin.Context) (*MigrationStatsPerTier, error) {
	stats := &MigrationStatsPerTier{}
	now := time.Now()

	type row struct {
		UserTier string
		N        int64
	}
	var rows []row
	err := m.ds.DB().WithContext(ctx).
		Model(&model.User{}).
		Select("user_tier, COUNT(*) AS n").
		Where("tier_expires IS NOT NULL AND tier_expires > ? AND billing_mode = ?", now, model.BillingModeCredits).
		Group("user_tier").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		switch r.UserTier {
		case model.UserTierTrial:
			stats.Trial = r.N
			stats.TotalInPeriod += r.N
		case model.UserTierStandard:
			stats.Standard = r.N
			stats.TotalInPeriod += r.N
		case model.UserTierPremium:
			stats.Premium = r.N
			stats.TotalInPeriod += r.N
		case model.UserTierFree:
			stats.Free = r.N
		}
	}
	return stats, nil
}

// ----------------------------------------------------------------------------
// Execute endpoint (spec §4.4.3)
// ----------------------------------------------------------------------------

// InitBillingMode POST /v1/admin/migrations/billing-mode-init
//
// One-shot migration (Grandfathering Option E / spec §2.7): flip in-period
// users (trial/standard/premium with tier_expires in the future) from
// billing_mode=credits to legacy_tier. The `billing_mode='credits'` guard in
// the UPDATE ensures idempotency — re-running after any subset of users was
// already flipped is a no-op on those users.
//
// Audit: inserts an action_log row with action='billing_mode.init' so the
// status endpoint can display "执行时间 / 执行人".
func (m *MigrationController) InitBillingMode(ctx *gin.Context) {
	admin := middleware.GetCurrentUser(ctx)
	if admin == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	now := time.Now()

	// Execute the migration UPDATE + action_log INSERT atomically. Using the
	// biz-level DB directly because this is infra-level, no domain logic.
	var migratedCount int64
	if err := m.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.User{}).
			Where("user_tier IN ? AND tier_expires IS NOT NULL AND tier_expires > ? AND billing_mode = ?",
				[]string{model.UserTierStandard, model.UserTierPremium, model.UserTierTrial},
				now, model.BillingModeCredits).
			Update("billing_mode", model.BillingModeLegacyTier)
		if res.Error != nil {
			return res.Error
		}
		migratedCount = res.RowsAffected

		// Audit row
		targetID := admin.ID
		if err := tx.Create(&model.ActionLogM{
			UserID:   admin.ID,
			Action:   actionBillingModeInit,
			Target:   "billing_mode_migration",
			TargetID: &targetID,
			Detail:   "Grandfathering Option E — migrated " + formatInt64(migratedCount) + " user(s)",
		}).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		log.C(ctx).Errorw("billing-mode-init migration failed", "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer.SetMessage("migration failed: %s", err.Error()), nil)
		return
	}

	log.C(ctx).Infow("billing-mode-init migration executed",
		"admin_id", admin.ID, "admin_username", admin.Username,
		"migrated_count", migratedCount)

	core.WriteResponse(ctx, nil, MigrationRunResp{
		MigratedCount: migratedCount,
		ExecutedAt:    now,
		ExecutedBy:    admin.Username,
	})
}

// ----------------------------------------------------------------------------
// constants + helpers
// ----------------------------------------------------------------------------

// actionBillingModeInit is the action_log.action value written by the migration
// and read by the status endpoint.
const actionBillingModeInit = "billing_mode.init"

// formatInt64 is a tiny helper to avoid pulling "strconv" for one call.
func formatInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	if negative {
		return "-" + string(out)
	}
	return string(out)
}
