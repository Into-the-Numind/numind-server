package store

import (
	"context"
	"fmt"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ContextBudgetStore defines persistence operations for the context budget feature.
// All writes to versioned tables (token profiles and policies) must use transactions
// with SELECT ... FOR UPDATE to enforce the active-version invariant.
// See spec §3.2 / §3.3.
type ContextBudgetStore interface {
	// GetActiveTokenProfile returns the current active token estimation profile
	// matching the given lookup key. Profiles are ordered by version DESC, id DESC.
	GetActiveTokenProfile(ctx context.Context, key TokenProfileLookupKey) (*model.TokenEstimationProfile, error)

	// SaveTokenProfileVersion inserts a new active version of a token estimation
	// profile, deactivating all prior active rows with the same key.
	SaveTokenProfileVersion(ctx context.Context, input SaveTokenProfileInput) (*model.TokenEstimationProfile, error)

	// GetActivePolicy returns the current active budget policy for the given operation.
	GetActivePolicy(ctx context.Context, operation string) (*model.ContextBudgetPolicy, error)

	// SavePolicyVersion inserts a new active version of a budget policy, deactivating
	// all prior active rows for the same operation.
	SavePolicyVersion(ctx context.Context, input SavePolicyInput) (*model.ContextBudgetPolicy, error)

	// FindReadySummary looks up a summary by (owner_user_id, scope_type, scope_id,
	// source_hash) with status='ready'. Returns gorm.ErrRecordNotFound when absent.
	FindReadySummary(ctx context.Context, ownerUserID uint, scopeType, scopeID, sourceHash string) (*model.ContextSummary, error)

	// UpsertSummary inserts or updates a context summary on conflict of the
	// (owner_user_id, scope_type, scope_id, source_hash) unique key.
	UpsertSummary(ctx context.Context, summary *model.ContextSummary) error

	// CreateEvent inserts a new context budget event row.
	CreateEvent(ctx context.Context, event *model.ContextBudgetEvent) error

	// PatchEvent updates non-nil fields of an existing event identified by id.
	PatchEvent(ctx context.Context, id uint64, patch EventPatch) error
}

// TokenProfileLookupKey carries the lookup dimensions for GetActiveTokenProfile.
// Set IsFallback=true to query the provider/global fallback rows; otherwise the
// store performs an exact (provider, model, service_type) lookup.
// When Model is empty, the store queries by (provider, ”, service_type) for a
// family-level row.
type TokenProfileLookupKey struct {
	Provider    string
	Model       string // may be "" for family lookup
	ServiceType string
	IsFallback  bool // if true, is_fallback=1 rows are queried
}

// SaveTokenProfileInput carries all fields needed to create a new token profile version.
type SaveTokenProfileInput struct {
	Provider              string
	Model                 string
	ModelFamily           string
	ServiceType           string
	ProfileJSON           datatypes.JSON
	SafetyMultiplier      float64
	CalibrationMultiplier float64
	IsFallback            bool
	ChangeReason          string
	UpdatedBy             string
}

// SavePolicyInput carries all fields needed to create a new policy version.
type SavePolicyInput struct {
	Operation            string
	ReservedOutputTokens int
	SafeRatio            float64
	FixedOverheadTokens  int
	SoftThresholdRatio   float64
	HardThresholdRatio   float64
	ChargeUser           bool
	Description          string
	ChangeReason         string
	UpdatedBy            string
}

// EventPatch carries optional fields to update on a ContextBudgetEvent.
// Only non-nil pointer fields are written; zero-value non-pointer fields are ignored.
type EventPatch struct {
	EstimatedAfter          *int
	ActualPromptTokens      *int
	ActualCompletionTokens  *int
	ReserveAmount           *int64
	ReconcileDelta          *int64
	CompressionActions      datatypes.JSON
	DroppedFragmentCount    *int
	SummarizedFragmentCount *int
	CriticalFragmentCount   *int
	CalibrationRatio        *float64
	UsageRecordID           *uint64
	ReservationID           *uint64
	Status                  *string
	ErrorCode               *string
	Metadata                datatypes.JSON
}

// contextBudgetStore is the concrete GORM-backed implementation of ContextBudgetStore.
type contextBudgetStore struct {
	db *gorm.DB
}

// NewContextBudgetStore returns a ContextBudgetStore backed by db.
func NewContextBudgetStore(db *gorm.DB) ContextBudgetStore {
	return &contextBudgetStore{db: db}
}

// GetActiveTokenProfile looks up the active token estimation profile for the
// given key. Exact match on (provider, model, service_type) unless IsFallback
// is set, in which case is_fallback=1 rows are queried. Rows are ordered by
// version DESC, id DESC so the newest active version is returned first.
//
// Per spec §3.2: if more than one active row is found (data integrity anomaly),
// a warning is logged and the newest row (by version DESC, id DESC) is returned.
// P2-B: is_fallback is always filtered explicitly to prevent exact lookups from
// accidentally matching fallback rows.
func (s *contextBudgetStore) GetActiveTokenProfile(ctx context.Context, key TokenProfileLookupKey) (*model.TokenEstimationProfile, error) {
	var profiles []model.TokenEstimationProfile
	q := s.db.WithContext(ctx).
		Where("provider = ? AND model = ? AND service_type = ? AND is_active = ?",
			key.Provider, key.Model, key.ServiceType, true)
	// P2-B: always filter is_fallback explicitly so exact lookups cannot match
	// fallback rows and fallback lookups cannot match exact rows.
	q = q.Where("is_fallback = ?", key.IsFallback)
	if err := q.Order("version DESC, id DESC").Limit(2).Find(&profiles).Error; err != nil {
		return nil, fmt.Errorf("GetActiveTokenProfile: %w", err)
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("GetActiveTokenProfile: %w", gorm.ErrRecordNotFound)
	}
	// P2-A: spec §3.2 — warn if more than one active row is found, then use the newest.
	if len(profiles) > 1 {
		log.Warnw("GetActiveTokenProfile: multiple active rows found (data integrity anomaly), using newest",
			"provider", key.Provider, "model", key.Model, "service_type", key.ServiceType,
			"newest_version", profiles[0].Version, "newest_id", profiles[0].ID)
	}
	return &profiles[0], nil
}

// SaveTokenProfileVersion creates a new active version of a token estimation
// profile. Within a transaction it locks all currently-active rows matching the
// same (provider, model, service_type) key, deactivates them, then inserts a
// new row with version = max(prior)+1 and is_active=true.
func (s *contextBudgetStore) SaveTokenProfileVersion(ctx context.Context, input SaveTokenProfileInput) (*model.TokenEstimationProfile, error) {
	var saved *model.TokenEstimationProfile
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock all currently-active rows for this key, filtered by is_fallback to
		// prevent accidentally deactivating fallback=true rows when saving a
		// fallback=false version and vice versa. (F1 fix)
		var existing []model.TokenEstimationProfile
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("provider = ? AND model = ? AND service_type = ? AND is_active = ? AND is_fallback = ?",
				input.Provider, input.Model, input.ServiceType, true, input.IsFallback).
			Find(&existing).Error; err != nil {
			return err
		}

		// Determine next version.
		nextVersion := uint(1)
		for _, p := range existing {
			if p.Version+1 > nextVersion {
				nextVersion = p.Version + 1
			}
		}

		// Deactivate all existing active rows (UpdateColumn bypasses GORM hooks/defaults).
		if len(existing) > 0 {
			ids := make([]uint64, 0, len(existing))
			for _, p := range existing {
				ids = append(ids, p.ID)
			}
			if err := tx.Model(&model.TokenEstimationProfile{}).
				Where("id IN ?", ids).
				UpdateColumn("is_active", false).Error; err != nil {
				return err
			}
		}

		// Insert the new active version.
		newRow := &model.TokenEstimationProfile{
			Provider:              input.Provider,
			Model:                 input.Model,
			ModelFamily:           input.ModelFamily,
			ServiceType:           input.ServiceType,
			ProfileJSON:           input.ProfileJSON,
			SafetyMultiplier:      input.SafetyMultiplier,
			CalibrationMultiplier: input.CalibrationMultiplier,
			IsFallback:            input.IsFallback,
			Version:               nextVersion,
			IsActive:              true,
			ChangeReason:          input.ChangeReason,
			UpdatedBy:             input.UpdatedBy,
		}
		if err := tx.Create(newRow).Error; err != nil {
			return err
		}

		// IsFallback has default:false, so IsFallback=true is stored correctly by
		// GORM (non-zero value). No UpdateColumn fixup needed for IsFallback.
		// IsActive is default:true and we want true — DB default takes effect correctly.
		// No fixup needed for any bool field in SaveTokenProfileVersion.

		saved = newRow
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("SaveTokenProfileVersion: %w", err)
	}
	return saved, nil
}

// GetActivePolicy returns the current active budget policy for the given operation,
// ordered by version DESC, id DESC.
//
// Per spec §3.3: if more than one active row is found (data integrity anomaly),
// a warning is logged and the newest row is returned.
func (s *contextBudgetStore) GetActivePolicy(ctx context.Context, operation string) (*model.ContextBudgetPolicy, error) {
	var policies []model.ContextBudgetPolicy
	if err := s.db.WithContext(ctx).
		Where("operation = ? AND is_active = ?", operation, true).
		Order("version DESC, id DESC").
		Limit(2).Find(&policies).Error; err != nil {
		return nil, fmt.Errorf("GetActivePolicy: %w", err)
	}
	if len(policies) == 0 {
		return nil, fmt.Errorf("GetActivePolicy: %w", gorm.ErrRecordNotFound)
	}
	// P2-A: warn if more than one active row is found, then use the newest.
	if len(policies) > 1 {
		log.Warnw("GetActivePolicy: multiple active rows found (data integrity anomaly), using newest",
			"operation", operation,
			"newest_version", policies[0].Version, "newest_id", policies[0].ID)
	}
	return &policies[0], nil
}

// SavePolicyVersion creates a new active version of a budget policy. Within a
// transaction it locks all currently-active rows for the operation, deactivates
// them, and inserts a new row with the next version number.
//
// GORM default:true bool gotcha: ChargeUser carries `default:true`. When the
// caller passes ChargeUser=false, GORM v2 treats the bool zero-value as "not set"
// on Create and the DB DEFAULT TRUE silently wins. We capture caller intent
// before Create and follow up with UpdateColumn to persist false.
// See .claude/rules/database.md §6.
func (s *contextBudgetStore) SavePolicyVersion(ctx context.Context, input SavePolicyInput) (*model.ContextBudgetPolicy, error) {
	var saved *model.ContextBudgetPolicy
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock all currently-active rows for this operation.
		var existing []model.ContextBudgetPolicy
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("operation = ? AND is_active = ?", input.Operation, true).
			Find(&existing).Error; err != nil {
			return err
		}

		// Determine next version.
		nextVersion := uint(1)
		for _, p := range existing {
			if p.Version+1 > nextVersion {
				nextVersion = p.Version + 1
			}
		}

		// Deactivate all existing active rows.
		if len(existing) > 0 {
			ids := make([]uint64, 0, len(existing))
			for _, p := range existing {
				ids = append(ids, p.ID)
			}
			if err := tx.Model(&model.ContextBudgetPolicy{}).
				Where("id IN ?", ids).
				UpdateColumn("is_active", false).Error; err != nil {
				return err
			}
		}

		// Capture caller's ChargeUser intent before Create (GORM default:true gotcha).
		wantChargeUser := input.ChargeUser

		// Insert new active version.
		newRow := &model.ContextBudgetPolicy{
			Operation:            input.Operation,
			ReservedOutputTokens: input.ReservedOutputTokens,
			SafeRatio:            input.SafeRatio,
			FixedOverheadTokens:  input.FixedOverheadTokens,
			SoftThresholdRatio:   input.SoftThresholdRatio,
			HardThresholdRatio:   input.HardThresholdRatio,
			ChargeUser:           input.ChargeUser,
			Description:          input.Description,
			Version:              nextVersion,
			IsActive:             true,
			ChangeReason:         input.ChangeReason,
			UpdatedBy:            input.UpdatedBy,
		}
		if err := tx.Create(newRow).Error; err != nil {
			return err
		}

		// Fix ChargeUser=false being silently flipped to true by GORM default:true.
		if !wantChargeUser && newRow.ChargeUser {
			if err := tx.Model(newRow).UpdateColumn("charge_user", false).Error; err != nil {
				return err
			}
			newRow.ChargeUser = false
		}

		// IsActive=true is what we want — DB default:true is correct. No fixup needed.
		saved = newRow
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("SavePolicyVersion: %w", err)
	}
	return saved, nil
}

// FindReadySummary looks up a context summary by the full tenant-scoped key
// (owner_user_id, scope_type, scope_id, source_hash) with status='ready'.
// Returns gorm.ErrRecordNotFound (wrapped) when no matching row exists.
func (s *contextBudgetStore) FindReadySummary(ctx context.Context, ownerUserID uint, scopeType, scopeID, sourceHash string) (*model.ContextSummary, error) {
	var summary model.ContextSummary
	err := s.db.WithContext(ctx).
		Where("owner_user_id = ? AND scope_type = ? AND scope_id = ? AND source_hash = ? AND status = ?",
			ownerUserID, scopeType, scopeID, sourceHash, "ready").
		First(&summary).Error
	if err != nil {
		return nil, fmt.Errorf("FindReadySummary: %w", err)
	}
	return &summary, nil
}

// UpsertSummary inserts or updates a context summary on conflict of the unique
// key (owner_user_id, scope_type, scope_id, source_hash).
func (s *contextBudgetStore) UpsertSummary(ctx context.Context, summary *model.ContextSummary) error {
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "owner_user_id"},
			{Name: "scope_type"},
			{Name: "scope_id"},
			{Name: "source_hash"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"summary_text",
			"summary_token_estimate",
			"original_token_estimate",
			"model",
			"provider",
			"status",
			"error_message",
			"created_by_operation",
			"expires_at",
			"updated_at",
			"source_fragment_ids",
		}),
	}).Create(summary).Error
	if err != nil {
		return fmt.Errorf("UpsertSummary: %w", err)
	}
	return nil
}

// CreateEvent inserts a new context budget event record.
func (s *contextBudgetStore) CreateEvent(ctx context.Context, event *model.ContextBudgetEvent) error {
	if err := s.db.WithContext(ctx).Create(event).Error; err != nil {
		return fmt.Errorf("CreateEvent: %w", err)
	}
	return nil
}

// PatchEvent updates only non-nil fields on the event identified by id.
// Uses Updates(map) to skip zero-value fields and avoid overwriting unrelated columns.
// Note: GORM `.Updates(map)` returns nil error even when 0 rows are affected
// (the id does not exist). Callers that must distinguish "patched" from "id not
// found" should check beforehand or use db.RowsAffected. This store layer does
// not enforce existence.
func (s *contextBudgetStore) PatchEvent(ctx context.Context, id uint64, patch EventPatch) error {
	updates := make(map[string]interface{})

	if patch.EstimatedAfter != nil {
		updates["estimated_after"] = *patch.EstimatedAfter
	}
	if patch.ActualPromptTokens != nil {
		updates["actual_prompt_tokens"] = *patch.ActualPromptTokens
	}
	if patch.ActualCompletionTokens != nil {
		updates["actual_completion_tokens"] = *patch.ActualCompletionTokens
	}
	if patch.ReserveAmount != nil {
		updates["reserve_amount"] = *patch.ReserveAmount
	}
	if patch.ReconcileDelta != nil {
		updates["reconcile_delta"] = *patch.ReconcileDelta
	}
	if patch.CompressionActions != nil {
		updates["compression_actions"] = patch.CompressionActions
	}
	if patch.DroppedFragmentCount != nil {
		updates["dropped_fragment_count"] = *patch.DroppedFragmentCount
	}
	if patch.SummarizedFragmentCount != nil {
		updates["summarized_fragment_count"] = *patch.SummarizedFragmentCount
	}
	if patch.CriticalFragmentCount != nil {
		updates["critical_fragment_count"] = *patch.CriticalFragmentCount
	}
	if patch.CalibrationRatio != nil {
		updates["calibration_ratio"] = *patch.CalibrationRatio
	}
	if patch.UsageRecordID != nil {
		updates["usage_record_id"] = *patch.UsageRecordID
	}
	if patch.ReservationID != nil {
		updates["reservation_id"] = *patch.ReservationID
	}
	if patch.Status != nil {
		updates["status"] = *patch.Status
	}
	if patch.ErrorCode != nil {
		updates["error_code"] = *patch.ErrorCode
	}
	if patch.Metadata != nil {
		updates["metadata"] = patch.Metadata
	}

	if len(updates) == 0 {
		return nil
	}

	if err := s.db.WithContext(ctx).
		Model(&model.ContextBudgetEvent{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("PatchEvent: %w", err)
	}
	return nil
}
