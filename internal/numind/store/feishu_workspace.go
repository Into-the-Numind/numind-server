package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/model"
)

const feishuSucceededCreateProofLimit = 32

// ErrFeishuProofReservationUnavailable means a candidate cannot atomically
// reserve the requested one-shot proof. Callers may safely retry creation only
// after removing the proof exemption from the encrypted request.
var ErrFeishuProofReservationUnavailable = errors.New("feishu operation proof reservation unavailable")

// IFeishuWorkspaceStore defines tenant- and generation-safe persistence primitives
// for encrypted lark-cli workspaces, authorization sessions, and operations.
type IFeishuWorkspaceStore interface {
	GetVault(ctx context.Context, userID uint, generation uint64) (*model.FeishuCLIVault, error)
	PutVaultCAS(ctx context.Context, vault *model.FeishuCLIVault, expectedRevision uint64) error
	DeleteVault(ctx context.Context, userID uint, generation uint64) error
	CreateSession(ctx context.Context, session *model.FeishuAuthSession) error
	GetSessionForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuAuthSession, error)
	ClaimSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error)
	UpdateSessionState(ctx context.Context, userID uint, generation uint64, id, owner, state string, now time.Time, completedAt *time.Time) error
	CreateOrGetOperation(ctx context.Context, operation *model.FeishuOperation) (*model.FeishuOperation, error)
	CreateOrGetOperationWithProof(ctx context.Context, operation *model.FeishuOperation, sourceOperationID string) (*model.FeishuOperation, error)
	ListSucceededCreatesForRun(ctx context.Context, userID uint, generation uint64, agentRunID uint64) ([]model.FeishuOperation, error)
	IsOperationProofBound(ctx context.Context, userID uint, generation uint64, agentRunID uint64, sourceOperationID, consumerOperationID string) (bool, error)
	GetOperationForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuOperation, error)
	ClaimOperation(ctx context.Context, userID uint, generation uint64, id, owner string, expectedStates []string, now, leaseUntil time.Time) (bool, error)
	TransitionOperation(ctx context.Context, userID uint, generation uint64, id, owner string, from []string, to string, now time.Time, fields map[string]any) error
	CancelPendingForGeneration(ctx context.Context, userID uint, generation uint64) error
}

type feishuWorkspaceStore struct {
	db *gorm.DB
}

var _ IFeishuWorkspaceStore = (*feishuWorkspaceStore)(nil)

func newFeishuWorkspaceStore(db *gorm.DB) *feishuWorkspaceStore {
	return &feishuWorkspaceStore{db: db}
}

// GetVault returns a vault only when both its tenant and account generation match.
func (s *feishuWorkspaceStore) GetVault(ctx context.Context, userID uint, generation uint64) (*model.FeishuCLIVault, error) {
	var vault model.FeishuCLIVault
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND generation = ?", userID, generation).
		Take(&vault).Error; err != nil {
		return nil, err
	}
	return &vault, nil
}

// PutVaultCAS creates revision 1 when expectedRevision is zero or replaces the
// encrypted snapshot only when the persisted revision matches expectedRevision.
func (s *feishuWorkspaceStore) PutVaultCAS(ctx context.Context, vault *model.FeishuCLIVault, expectedRevision uint64) error {
	nextRevision := expectedRevision + 1
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", vault.UserID, "lark").
			Take(&account).Error; err != nil {
			return err
		}
		if account.Generation != vault.Generation {
			return gorm.ErrRecordNotFound
		}

		if expectedRevision == 0 {
			candidate := *vault
			candidate.Revision = nextRevision
			if err := tx.Create(&candidate).Error; err != nil {
				var existing model.FeishuCLIVault
				if lookupErr := tx.Where("user_id = ?", vault.UserID).Take(&existing).Error; lookupErr == nil {
					return gorm.ErrRecordNotFound
				}
				return fmt.Errorf("create feishu CLI vault: %w", err)
			}
			return nil
		}

		result := tx.Model(&model.FeishuCLIVault{}).
			Where("user_id = ? AND generation = ? AND revision = ?", vault.UserID, vault.Generation, expectedRevision).
			Updates(map[string]any{
				"ciphertext":  vault.Ciphertext,
				"key_version": vault.KeyVersion,
				"checksum":    vault.Checksum,
				"revision":    nextRevision,
			})
		if result.Error != nil {
			return fmt.Errorf("update feishu CLI vault: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	vault.Revision = nextRevision
	return nil
}

// DeleteVault deletes a vault only for the supplied tenant generation.
func (s *feishuWorkspaceStore) DeleteVault(ctx context.Context, userID uint, generation uint64) error {
	result := s.db.WithContext(ctx).
		Where("user_id = ? AND generation = ?", userID, generation).
		Delete(&model.FeishuCLIVault{})
	if result.Error != nil {
		return fmt.Errorf("delete feishu CLI vault: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// CreateSession inserts authorization session metadata without storing ephemeral URLs.
func (s *feishuWorkspaceStore) CreateSession(ctx context.Context, session *model.FeishuAuthSession) error {
	if err := s.db.WithContext(ctx).Create(session).Error; err != nil {
		return fmt.Errorf("create feishu auth session: %w", err)
	}
	return nil
}

// GetSessionForUser returns a session only when ID, tenant, and generation all match.
func (s *feishuWorkspaceStore) GetSessionForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuAuthSession, error) {
	var session model.FeishuAuthSession
	if err := s.db.WithContext(ctx).
		Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
		Take(&session).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

// ClaimSession acquires or renews a lease when it is free, expired, or already
// belongs to owner. Sessions from an inactive account generation cannot be claimed.
func (s *feishuWorkspaceStore) ClaimSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error) {
	activeGeneration := s.db.WithContext(ctx).
		Model(&model.UserThirdPartyAccount{}).
		Select("1").
		Where("user_third_party_account.user_id = feishu_auth_session.user_id").
		Where("user_third_party_account.provider = ?", "lark").
		Where("user_third_party_account.generation = feishu_auth_session.generation")
	result := s.db.WithContext(ctx).
		Model(&model.FeishuAuthSession{}).
		Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
		Where("EXISTS (?)", activeGeneration).
		Where("lease_until IS NULL OR lease_until <= ? OR lease_owner = ?", now, owner).
		Updates(map[string]any{
			"lease_owner": owner,
			"lease_until": leaseUntil,
		})
	if result.Error != nil {
		return false, fmt.Errorf("claim feishu auth session: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

// UpdateSessionState changes a session only for its current lease owner and active generation.
func (s *feishuWorkspaceStore) UpdateSessionState(ctx context.Context, userID uint, generation uint64, id, owner, state string, now time.Time, completedAt *time.Time) error {
	activeGeneration := s.db.WithContext(ctx).
		Model(&model.UserThirdPartyAccount{}).
		Select("1").
		Where("user_third_party_account.user_id = feishu_auth_session.user_id").
		Where("user_third_party_account.provider = ?", "lark").
		Where("user_third_party_account.generation = feishu_auth_session.generation")
	result := s.db.WithContext(ctx).
		Model(&model.FeishuAuthSession{}).
		Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
		Where("lease_owner = ? AND lease_until > ?", owner, now).
		Where("EXISTS (?)", activeGeneration).
		Updates(map[string]any{
			"state":        state,
			"completed_at": completedAt,
		})
	if result.Error != nil {
		return fmt.Errorf("update feishu auth session state: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// CreateOrGetOperation atomically preserves per-user idempotency. Reusing a key
// for the same user returns the original operation; another user may reuse it.
func (s *feishuWorkspaceStore) CreateOrGetOperation(ctx context.Context, operation *model.FeishuOperation) (*model.FeishuOperation, error) {
	var stored *model.FeishuOperation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// All generation-changing flows must use the same account -> operation
		// lock order. This makes operation creation and future generation bumps
		// mutually exclusive on MySQL without introducing an inverse-order deadlock.
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", operation.UserID, "lark").
			Take(&account).Error; err != nil {
			return err
		}
		if account.Generation != operation.Generation {
			return gorm.ErrRecordNotFound
		}

		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(operation)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			stored = operation
			return nil
		}

		var existing model.FeishuOperation
		if err := tx.Where("user_id = ? AND idempotency_key = ?", operation.UserID, operation.IdempotencyKey).
			Take(&existing).Error; err != nil {
			return err
		}
		stored = &existing
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create or get feishu operation: %w", err)
	}
	return stored, nil
}

// CreateOrGetOperationWithProof creates a consumer and reserves its proof in
// one transaction. Lock order is account -> source -> consumer -> consumption.
// The consumption is permanent even if the consumer later waits or fails.
func (s *feishuWorkspaceStore) CreateOrGetOperationWithProof(
	ctx context.Context,
	operation *model.FeishuOperation,
	sourceOperationID string,
) (*model.FeishuOperation, error) {
	if operation == nil || sourceOperationID == "" {
		return nil, ErrFeishuProofReservationUnavailable
	}
	var stored *model.FeishuOperation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", operation.UserID, "lark").
			Take(&account).Error; err != nil {
			return err
		}
		if account.Generation != operation.Generation {
			return ErrFeishuProofReservationUnavailable
		}

		var source model.FeishuOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", sourceOperationID).
			Take(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrFeishuProofReservationUnavailable
			}
			return err
		}
		if source.UserID != operation.UserID || source.Generation != operation.Generation ||
			source.AgentRunID != operation.AgentRunID || source.State != model.FeishuOperationSucceeded ||
			(source.CommandPath != "docs +create" && source.CommandPath != "wiki +node-create") {
			return ErrFeishuProofReservationUnavailable
		}

		var intermediateWrites int64
		if err := tx.Model(&model.FeishuOperation{}).
			Where("user_id = ? AND generation = ? AND agent_run_id = ?", operation.UserID, operation.Generation, operation.AgentRunID).
			Where("command_path = ?", "docs +update").
			Where("created_at >= ?", source.CreatedAt).
			Count(&intermediateWrites).Error; err != nil {
			return err
		}
		if intermediateWrites != 0 {
			return ErrFeishuProofReservationUnavailable
		}

		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(operation)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var existing model.FeishuOperation
			if err := tx.Where("user_id = ? AND idempotency_key = ?", operation.UserID, operation.IdempotencyKey).
				Take(&existing).Error; err != nil {
				return err
			}
			stored = &existing
			return nil
		}

		var existingConsumption model.FeishuOperationProofConsumption
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("source_operation_id = ?", source.ID).
			Take(&existingConsumption).Error
		if err == nil {
			return ErrFeishuProofReservationUnavailable
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		consumption := &model.FeishuOperationProofConsumption{
			SourceOperationID: source.ID, ConsumerOperationID: operation.ID,
			UserID: operation.UserID, Generation: operation.Generation, AgentRunID: operation.AgentRunID,
		}
		if err := tx.Create(consumption).Error; err != nil {
			return fmt.Errorf("create feishu proof consumption: %w", err)
		}
		stored = operation
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrFeishuProofReservationUnavailable) {
			return nil, ErrFeishuProofReservationUnavailable
		}
		return nil, fmt.Errorf("create or get feishu operation with proof: %w", err)
	}
	return stored, nil
}

// ListSucceededCreatesForRun returns a small, deterministic proof-candidate
// set. Callers must still authenticate and inspect each encrypted request and
// result; this query alone never proves that an overwrite is safe.
func (s *feishuWorkspaceStore) ListSucceededCreatesForRun(
	ctx context.Context,
	userID uint,
	generation uint64,
	agentRunID uint64,
) ([]model.FeishuOperation, error) {
	var operations []model.FeishuOperation
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND generation = ? AND agent_run_id = ?", userID, generation, agentRunID).
		Where("state = ?", model.FeishuOperationSucceeded).
		Where("command_path IN ?", []string{"docs +create", "wiki +node-create"}).
		Order("created_at DESC").
		Order("id DESC").
		Limit(feishuSucceededCreateProofLimit).
		Find(&operations).Error; err != nil {
		return nil, fmt.Errorf("list succeeded feishu create operations: %w", err)
	}
	return operations, nil
}

// IsOperationProofBound verifies the immutable audit tuple for a source and
// consumer. It deliberately has no release/delete counterpart.
func (s *feishuWorkspaceStore) IsOperationProofBound(
	ctx context.Context,
	userID uint,
	generation uint64,
	agentRunID uint64,
	sourceOperationID, consumerOperationID string,
) (bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.FeishuOperationProofConsumption{}).
		Where("source_operation_id = ? AND consumer_operation_id = ?", sourceOperationID, consumerOperationID).
		Where("user_id = ? AND generation = ? AND agent_run_id = ?", userID, generation, agentRunID).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("verify feishu proof consumption: %w", err)
	}
	return count == 1, nil
}

// GetOperationForUser returns an operation only when ID, tenant, and generation all match.
func (s *feishuWorkspaceStore) GetOperationForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuOperation, error) {
	var operation model.FeishuOperation
	if err := s.db.WithContext(ctx).
		Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
		Take(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

// ClaimOperation acquires or renews an operation lease only for the account's
// active generation and only when the prior lease is free, expired, or same-owner.
func (s *feishuWorkspaceStore) ClaimOperation(ctx context.Context, userID uint, generation uint64, id, owner string, expectedStates []string, now, leaseUntil time.Time) (bool, error) {
	if len(expectedStates) == 0 {
		return false, fmt.Errorf("claim feishu operation: expected source state is required")
	}
	activeGeneration := s.db.WithContext(ctx).
		Model(&model.UserThirdPartyAccount{}).
		Select("1").
		Where("user_third_party_account.user_id = feishu_operation.user_id").
		Where("user_third_party_account.provider = ?", "lark").
		Where("user_third_party_account.generation = feishu_operation.generation")
	result := s.db.WithContext(ctx).
		Model(&model.FeishuOperation{}).
		Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
		Where("state IN ?", expectedStates).
		Where("EXISTS (?)", activeGeneration).
		Where("lease_until IS NULL OR lease_until <= ? OR lease_owner = ?", now, owner).
		Updates(map[string]any{
			"lease_owner": owner,
			"lease_until": leaseUntil,
		})
	if result.Error != nil {
		return false, fmt.Errorf("claim feishu operation: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

// TransitionOperation performs a lease-owner and source-state conditional update.
func (s *feishuWorkspaceStore) TransitionOperation(ctx context.Context, userID uint, generation uint64, id, owner string, from []string, to string, now time.Time, fields map[string]any) error {
	allowedFields := map[string]struct{}{
		"attempt_count":       {},
		"started_at":          {},
		"finished_at":         {},
		"error_type":          {},
		"error_subtype":       {},
		"error_code":          {},
		"result_ciphertext":   {},
		"result_summary_json": {},
	}
	updates := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		if _, ok := allowedFields[key]; !ok {
			return fmt.Errorf("transition feishu operation field %q is not allowed", key)
		}
		updates[key] = value
	}
	updates["state"] = to
	if to != model.FeishuOperationExecuting {
		updates["lease_owner"] = ""
		updates["lease_until"] = nil
	}

	activeGeneration := s.db.WithContext(ctx).
		Model(&model.UserThirdPartyAccount{}).
		Select("1").
		Where("user_third_party_account.user_id = feishu_operation.user_id").
		Where("user_third_party_account.provider = ?", "lark").
		Where("user_third_party_account.generation = feishu_operation.generation")
	result := s.db.WithContext(ctx).
		Model(&model.FeishuOperation{}).
		Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
		Where("lease_owner = ? AND lease_until > ?", owner, now).
		Where("state IN ?", from).
		Where("EXISTS (?)", activeGeneration).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("transition feishu operation: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// CancelPendingForGeneration supersedes pending auth sessions and cancels
// not-yet-executing operations for one retired account generation.
func (s *feishuWorkspaceStore) CancelPendingForGeneration(ctx context.Context, userID uint, generation uint64) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.FeishuAuthSession{}).
			Where("user_id = ? AND generation = ? AND state = ?", userID, generation, model.FeishuAuthSessionPending).
			Updates(map[string]any{
				"state":        model.FeishuAuthSessionSuperseded,
				"completed_at": now,
			}).Error; err != nil {
			return fmt.Errorf("supersede feishu auth sessions: %w", err)
		}

		pendingStates := []string{
			model.FeishuOperationNotStarted,
			model.FeishuOperationWaitingConnection,
			model.FeishuOperationWaitingAppScope,
			model.FeishuOperationWaitingUserAuth,
			model.FeishuOperationWaitingConfirmation,
		}
		if err := tx.Model(&model.FeishuOperation{}).
			Where("user_id = ? AND generation = ? AND state IN ?", userID, generation, pendingStates).
			Updates(map[string]any{
				"state": model.FeishuOperationCancelled, "finished_at": now,
				"lease_owner": "", "lease_until": nil,
			}).Error; err != nil {
			return fmt.Errorf("cancel feishu operations: %w", err)
		}
		return nil
	})
}
