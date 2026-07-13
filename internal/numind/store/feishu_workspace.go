package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/model"
)

// IFeishuWorkspaceStore defines tenant- and generation-safe persistence primitives
// for encrypted lark-cli workspaces, authorization sessions, and operations.
type IFeishuWorkspaceStore interface {
	GetVault(ctx context.Context, userID uint, generation uint64) (*model.FeishuCLIVault, error)
	PutVaultCAS(ctx context.Context, vault *model.FeishuCLIVault, expectedRevision uint64) error
	DeleteVault(ctx context.Context, userID uint, generation uint64) error
	CreateSession(ctx context.Context, session *model.FeishuAuthSession) error
	GetSessionForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuAuthSession, error)
	ClaimSession(ctx context.Context, id, owner string, now, leaseUntil time.Time) (bool, error)
	UpdateSessionState(ctx context.Context, id, owner, state string, completedAt *time.Time) error
	CreateOrGetOperation(ctx context.Context, operation *model.FeishuOperation) (*model.FeishuOperation, error)
	GetOperationForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuOperation, error)
	ClaimOperation(ctx context.Context, id, owner string, now, leaseUntil time.Time) (bool, error)
	TransitionOperation(ctx context.Context, id, owner string, from []string, to string, fields map[string]any) error
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
	db := s.db.WithContext(ctx)
	if expectedRevision == 0 {
		vault.Revision = 1
		if err := db.Create(vault).Error; err != nil {
			var existing model.FeishuCLIVault
			if lookupErr := db.Where("user_id = ?", vault.UserID).Take(&existing).Error; lookupErr == nil {
				return gorm.ErrRecordNotFound
			}
			return fmt.Errorf("create feishu CLI vault: %w", err)
		}
		return nil
	}

	nextRevision := expectedRevision + 1
	result := db.Model(&model.FeishuCLIVault{}).
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
func (s *feishuWorkspaceStore) ClaimSession(ctx context.Context, id, owner string, now, leaseUntil time.Time) (bool, error) {
	activeGeneration := s.db.WithContext(ctx).
		Model(&model.UserThirdPartyAccount{}).
		Select("1").
		Where("user_third_party_account.user_id = feishu_auth_session.user_id").
		Where("user_third_party_account.provider = ?", "lark").
		Where("user_third_party_account.generation = feishu_auth_session.generation")
	result := s.db.WithContext(ctx).
		Model(&model.FeishuAuthSession{}).
		Where("id = ?", id).
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
func (s *feishuWorkspaceStore) UpdateSessionState(ctx context.Context, id, owner, state string, completedAt *time.Time) error {
	activeGeneration := s.db.WithContext(ctx).
		Model(&model.UserThirdPartyAccount{}).
		Select("1").
		Where("user_third_party_account.user_id = feishu_auth_session.user_id").
		Where("user_third_party_account.provider = ?", "lark").
		Where("user_third_party_account.generation = feishu_auth_session.generation")
	result := s.db.WithContext(ctx).
		Model(&model.FeishuAuthSession{}).
		Where("id = ? AND lease_owner = ?", id, owner).
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
func (s *feishuWorkspaceStore) ClaimOperation(ctx context.Context, id, owner string, now, leaseUntil time.Time) (bool, error) {
	activeGeneration := s.db.WithContext(ctx).
		Model(&model.UserThirdPartyAccount{}).
		Select("1").
		Where("user_third_party_account.user_id = feishu_operation.user_id").
		Where("user_third_party_account.provider = ?", "lark").
		Where("user_third_party_account.generation = feishu_operation.generation")
	result := s.db.WithContext(ctx).
		Model(&model.FeishuOperation{}).
		Where("id = ?", id).
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
func (s *feishuWorkspaceStore) TransitionOperation(ctx context.Context, id, owner string, from []string, to string, fields map[string]any) error {
	updates := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		updates[key] = value
	}
	updates["state"] = to

	activeGeneration := s.db.WithContext(ctx).
		Model(&model.UserThirdPartyAccount{}).
		Select("1").
		Where("user_third_party_account.user_id = feishu_operation.user_id").
		Where("user_third_party_account.provider = ?", "lark").
		Where("user_third_party_account.generation = feishu_operation.generation")
	result := s.db.WithContext(ctx).
		Model(&model.FeishuOperation{}).
		Where("id = ? AND lease_owner = ?", id, owner).
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
				"state":       model.FeishuOperationCancelled,
				"finished_at": now,
			}).Error; err != nil {
			return fmt.Errorf("cancel feishu operations: %w", err)
		}
		return nil
	})
}
