package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	CreateOrGetPendingSession(ctx context.Context, session *model.FeishuAuthSession) (*model.FeishuAuthSession, bool, error)
	GetSessionForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuAuthSession, error)
	FindActiveSessionForUser(ctx context.Context, userID uint, generation uint64) (*model.FeishuAuthSession, error)
	SupersedeSessionForUser(ctx context.Context, userID uint, generation uint64, id string, now time.Time) error
	RefreshOperationSession(ctx context.Context, userID uint, generation uint64, oldSessionID, operationID, waitingState, connectionState string, replacement *model.FeishuAuthSession, replacementSummary []byte, now time.Time) (*model.FeishuAuthSession, error)
	RestoreOperationSessionRefresh(ctx context.Context, userID uint, generation uint64, oldSessionID, replacementSessionID, operationID, waitingState string, oldSummary []byte, now time.Time) error
	ClaimSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error)
	RenewSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error)
	UpdateSessionState(ctx context.Context, userID uint, generation uint64, id, owner, state string, now time.Time, completedAt *time.Time) error
	FinalizeSessionCompleted(ctx context.Context, userID uint, generation uint64, id, owner, accountState string, connected bool, now time.Time, evidence model.FeishuConnectionEvidence) error
	UpdateAccountConnectionState(ctx context.Context, userID uint, generation uint64, state string, connected bool, now time.Time) error
	RecordCapabilityOutcome(ctx context.Context, userID uint, generation uint64, outcome model.FeishuCapabilityOutcome) error
	CreateOrGetOperation(ctx context.Context, operation *model.FeishuOperation) (*model.FeishuOperation, error)
	CreateOrGetOperationWithProof(ctx context.Context, operation *model.FeishuOperation, sourceOperationID string) (*model.FeishuOperation, error)
	TryClaimExecutionGate(ctx context.Context, userID uint, generation uint64, owner, operationID string, now, leaseUntil time.Time) (bool, error)
	RenewExecutionGate(ctx context.Context, userID uint, generation uint64, owner, operationID string, now, leaseUntil time.Time) (bool, error)
	ReleaseExecutionGate(ctx context.Context, userID uint, generation uint64, owner string, now time.Time) (bool, error)
	RetiredExecutionGateDrained(ctx context.Context, userID uint, retiredGeneration uint64, now time.Time) (bool, error)
	ClaimRetiredTeardown(ctx context.Context, userID uint, retiredGeneration, disconnectingGeneration uint64, owner string, now, leaseUntil time.Time) (bool, error)
	RenewRetiredTeardown(ctx context.Context, userID uint, retiredGeneration, disconnectingGeneration uint64, owner string, now, leaseUntil time.Time) (bool, error)
	ReleaseRetiredTeardown(ctx context.Context, userID uint, retiredGeneration uint64, owner string, now time.Time) error
	CompleteRetiredTeardown(ctx context.Context, userID uint, retiredGeneration, disconnectingGeneration uint64, owner string, now time.Time) (bool, error)
	ListSucceededCreatesForRun(ctx context.Context, userID uint, generation uint64, agentRunID uint64) ([]model.FeishuOperation, error)
	IsOperationProofUsable(ctx context.Context, userID uint, generation uint64, agentRunID uint64, sourceOperationID, consumerOperationID string) (bool, error)
	GetOperationForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuOperation, error)
	// ListTerminalOperationsForGeneration returns only the supplied terminal
	// states for one tenant and one retired generation. Lifecycle teardown uses
	// it to finish the exact linked Agent waits after RetireGeneration has
	// atomically fenced their operations; it is not a broad Agent-run scan.
	ListTerminalOperationsForGeneration(ctx context.Context, userID uint, generation uint64, states []string) ([]model.FeishuOperation, error)
	ClaimOperation(ctx context.Context, userID uint, generation uint64, id, owner string, expectedStates []string, now, leaseUntil time.Time) (bool, error)
	TransitionOperation(ctx context.Context, userID uint, generation uint64, id, owner string, from []string, to string, now time.Time, fields map[string]any) error
	TransitionOperationWithCapabilityOutcome(ctx context.Context, userID uint, generation uint64, id, owner string, from []string, to string, now time.Time, fields map[string]any, outcome model.FeishuCapabilityOutcome) error
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
		if !feishuAccountGenerationActive(&account, vault.Generation) {
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

// CreateOrGetPendingSession serializes one authorization intent through the
// tenant's account row. The intent key is tenant generation, optional operation,
// phase, and canonical requested scopes. URLs are deliberately not part of the
// persisted session and therefore cannot influence reuse.
func (s *feishuWorkspaceStore) CreateOrGetPendingSession(
	ctx context.Context,
	session *model.FeishuAuthSession,
) (*model.FeishuAuthSession, bool, error) {
	if session == nil {
		return nil, false, errors.New("create or get feishu auth session: nil session")
	}
	var stored *model.FeishuAuthSession
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", session.UserID, "lark").
			Take(&account).Error; err != nil {
			return err
		}
		if !feishuAccountGenerationActive(&account, session.Generation) {
			return gorm.ErrRecordNotFound
		}

		existing, err := findMatchingAuthSession(tx, session, model.FeishuAuthSessionPending, "created_at ASC, id ASC")
		if err == nil {
			stored = existing
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// Operation recovery completion is durable: after the worker dispatches a
		// resume, another service instance must observe the completed intent rather
		// than opening the same authorization flow again. Manual intents are
		// intentionally excluded so a later explicit reconnect can start fresh.
		if session.OperationID != nil {
			existing, err = findMatchingAuthSession(tx, session, model.FeishuAuthSessionCompleted, "completed_at DESC, id ASC")
			if err == nil {
				stored = existing
				return nil
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}

		candidate := *session
		if err := tx.Create(&candidate).Error; err != nil {
			return err
		}
		stored = &candidate
		created = true
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("create or get feishu auth session: %w", err)
	}
	return stored, created, nil
}

func findMatchingAuthSession(
	tx *gorm.DB,
	intent *model.FeishuAuthSession,
	state string,
	order string,
) (*model.FeishuAuthSession, error) {
	query := tx.Where(
		"user_id = ? AND generation = ? AND phase = ? AND state = ?",
		intent.UserID,
		intent.Generation,
		intent.Phase,
		state,
	)
	if intent.OperationID == nil {
		query = query.Where("operation_id IS NULL")
	} else {
		query = query.Where("operation_id = ?", *intent.OperationID)
	}
	var candidates []model.FeishuAuthSession
	if err := query.Order(order).Find(&candidates).Error; err != nil {
		return nil, err
	}
	for i := range candidates {
		if equalRequestedScopes(candidates[i].RequestedScopesJSON, intent.RequestedScopesJSON) {
			return &candidates[i], nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func equalRequestedScopes(left, right []byte) bool {
	var leftScopes []string
	if err := json.Unmarshal(left, &leftScopes); err != nil {
		return false
	}
	var rightScopes []string
	if err := json.Unmarshal(right, &rightScopes); err != nil {
		return false
	}
	if len(leftScopes) != len(rightScopes) {
		return false
	}
	for i := range leftScopes {
		if leftScopes[i] != rightScopes[i] {
			return false
		}
	}
	return true
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

// FindActiveSessionForUser returns the most recently touched pending session
// for a current account generation. It intentionally returns metadata only;
// verification URLs and device codes are never persisted on this model.
func (s *feishuWorkspaceStore) FindActiveSessionForUser(
	ctx context.Context,
	userID uint,
	generation uint64,
) (*model.FeishuAuthSession, error) {
	var session model.FeishuAuthSession
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND generation = ? AND state = ?", userID, generation, model.FeishuAuthSessionPending).
		Order("updated_at DESC, id DESC").Take(&session).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

// SupersedeSessionForUser retires one still-pending session regardless of its
// live worker lease. A caller uses this only after validating the current
// account generation; releasing the lease prevents the old worker from later
// finalizing a stale authorization result.
func (s *feishuWorkspaceStore) SupersedeSessionForUser(
	ctx context.Context,
	userID uint,
	generation uint64,
	id string,
	now time.Time,
) error {
	result := s.db.WithContext(ctx).Model(&model.FeishuAuthSession{}).
		Where("id = ? AND user_id = ? AND generation = ? AND state = ?", id, userID, generation, model.FeishuAuthSessionPending).
		Updates(map[string]any{
			"state": model.FeishuAuthSessionSuperseded, "completed_at": now.UTC(),
			"lease_owner": "", "lease_until": nil,
		})
	if result.Error != nil {
		return fmt.Errorf("supersede feishu auth session: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ClaimSession acquires a pending session with a fresh token. A token may not
// reclaim its own expired lease; workers must use RenewSession before expiry.
func (s *feishuWorkspaceStore) ClaimSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error) {
	if owner == "" || !leaseUntil.After(now) {
		return false, nil
	}
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := lockFeishuAuthAccount(tx, userID, generation)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		result := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
			Where("state = ?", model.FeishuAuthSessionPending).
			Where(
				"((COALESCE(lease_owner, '') = '') AND lease_until IS NULL) OR (lease_until <= ? AND COALESCE(lease_owner, '') <> ?)",
				now,
				owner,
			).
			Updates(map[string]any{"lease_owner": owner, "lease_until": leaseUntil})
		if result.Error != nil {
			return result.Error
		}
		claimed = result.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("claim feishu auth session: %w", err)
	}
	return claimed, nil
}

// RenewSession extends an unexpired pending lease only for its exact token and
// active account generation.
func (s *feishuWorkspaceStore) RenewSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error) {
	if owner == "" || !leaseUntil.After(now) {
		return false, nil
	}
	renewed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := lockFeishuAuthAccount(tx, userID, generation)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		result := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
			Where("state = ?", model.FeishuAuthSessionPending).
			Where("lease_owner = ? AND lease_until > ?", owner, now).
			Update("lease_until", leaseUntil)
		if result.Error != nil {
			return result.Error
		}
		renewed = result.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("renew feishu auth session: %w", err)
	}
	return renewed, nil
}

// UpdateSessionState terminally changes a pending session only for its current
// unexpired lease token and active generation.
func (s *feishuWorkspaceStore) UpdateSessionState(ctx context.Context, userID uint, generation uint64, id, owner, state string, now time.Time, completedAt *time.Time) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := lockFeishuAuthAccount(tx, userID, generation)
		if err != nil {
			return err
		}
		if !active {
			return gorm.ErrRecordNotFound
		}
		result := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
			Where("state = ?", model.FeishuAuthSessionPending).
			Where("lease_owner = ? AND lease_until > ?", owner, now).
			Updates(map[string]any{
				"state":        state,
				"completed_at": completedAt,
				"lease_owner":  "",
				"lease_until":  nil,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return gorm.ErrRecordNotFound
		}
		return fmt.Errorf("update feishu auth session state: %w", err)
	}
	return nil
}

func lockFeishuAuthAccount(tx *gorm.DB, userID uint, generation uint64) (bool, error) {
	var account model.UserThirdPartyAccount
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND provider = ?", userID, "lark").
		Take(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return feishuAccountGenerationActive(&account, generation), nil
}

func feishuAccountGenerationActive(account *model.UserThirdPartyAccount, generation uint64) bool {
	return account != nil && account.Generation == generation && account.ConnectionState != model.FeishuConnectionDisconnecting
}

// FinalizeSessionCompleted atomically updates the generation-fenced account and
// moves its pending authorization session to completed. The lock order is
// always account then session.
func (s *feishuWorkspaceStore) FinalizeSessionCompleted(
	ctx context.Context,
	userID uint,
	generation uint64,
	id string,
	owner string,
	accountState string,
	connected bool,
	now time.Time,
	evidence model.FeishuConnectionEvidence,
) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", userID, "lark").
			Take(&account).Error; err != nil {
			return err
		}
		if !feishuAccountGenerationActive(&account, generation) {
			return gorm.ErrRecordNotFound
		}

		var session model.FeishuAuthSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
			Where("state = ?", model.FeishuAuthSessionPending).
			Where("lease_owner = ? AND lease_until > ?", owner, now).
			Take(&session).Error; err != nil {
			return err
		}

		var connectedAt *time.Time
		if connected {
			value := now.UTC()
			connectedAt = &value
		}
		accountUpdates := map[string]any{
			"connection_state": accountState,
			"connected":        connected,
			"connected_at":     connectedAt,
		}
		if validFeishuAppIDEvidence(evidence.AppID) {
			accountUpdates["app_id"] = evidence.AppID
		}
		if validFeishuCLIVersionEvidence(evidence.CLIVersion) {
			accountUpdates["lark_cli_version"] = evidence.CLIVersion
		}
		accountResult := tx.Model(&model.UserThirdPartyAccount{}).
			Where("user_id = ? AND provider = ? AND generation = ?", userID, "lark", generation).
			Updates(accountUpdates)
		if accountResult.Error != nil {
			return fmt.Errorf("finalize feishu account connection: %w", accountResult.Error)
		}
		if accountResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		sessionResult := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND state = ? AND lease_owner = ?", session.ID, model.FeishuAuthSessionPending, owner).
			Updates(map[string]any{
				"state":        model.FeishuAuthSessionCompleted,
				"completed_at": now,
				"lease_owner":  "",
				"lease_until":  nil,
			})
		if sessionResult.Error != nil {
			return fmt.Errorf("finalize feishu auth session: %w", sessionResult.Error)
		}
		if sessionResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// RecordCapabilityOutcome persists only catalog-derived, classified outcome
// metadata for the active generation. It never receives scopes, identifiers,
// raw CLI data, or credentials. The account row lock prevents concurrent Docs,
// Base, and Wiki commands from dropping each other's last-known state.
func (s *feishuWorkspaceStore) RecordCapabilityOutcome(
	ctx context.Context,
	userID uint,
	generation uint64,
	outcome model.FeishuCapabilityOutcome,
) error {
	if !validFeishuCapabilityOutcome(outcome) {
		return gorm.ErrRecordNotFound
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ? AND generation = ? AND connection_state <> ?", userID, "lark", generation, model.FeishuConnectionDisconnecting).
			Take(&account).Error; err != nil {
			return err
		}

		return recordFeishuCapabilityOutcome(tx, &account, outcome)
	})
}

func recordFeishuCapabilityOutcome(tx *gorm.DB, account *model.UserThirdPartyAccount, outcome model.FeishuCapabilityOutcome) error {
	if tx == nil || account == nil || !validFeishuCapabilityOutcome(outcome) {
		return gorm.ErrRecordNotFound
	}
	states := decodeFeishuCapabilityStates(account.CapabilityStateJSON)
	states[outcome.Domain] = feishuStoredCapabilityState{State: outcome.State}
	if outcome.SucceededAt != nil {
		at := outcome.SucceededAt.UTC()
		states[outcome.Domain] = feishuStoredCapabilityState{State: outcome.State, LastSuccessAt: &at}
	}
	encoded, err := json.Marshal(states)
	if err != nil {
		return fmt.Errorf("encode feishu capability outcome: %w", err)
	}
	updates := map[string]any{"capability_state_json": encoded}
	if outcome.SucceededAt != nil {
		at := outcome.SucceededAt.UTC()
		updates["last_success_at"] = &at
	}
	if outcome.CLIVersion != "" {
		updates["lark_cli_version"] = outcome.CLIVersion
	}
	result := tx.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ? AND generation = ? AND connection_state <> ?", account.UserID, "lark", account.Generation, model.FeishuConnectionDisconnecting).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("record feishu capability outcome: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

type feishuStoredCapabilityState struct {
	State         string     `json:"state"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
}

func decodeFeishuCapabilityStates(raw []byte) map[string]feishuStoredCapabilityState {
	decoded := make(map[string]feishuStoredCapabilityState)
	if len(raw) > 0 && json.Unmarshal(raw, &decoded) != nil {
		decoded = make(map[string]feishuStoredCapabilityState)
	}
	clean := make(map[string]feishuStoredCapabilityState, 3)
	for _, domain := range []string{"docs", "base", "wiki"} {
		state, found := decoded[domain]
		if !found || !validFeishuCapabilityState(state.State) {
			continue
		}
		if state.LastSuccessAt != nil {
			at := state.LastSuccessAt.UTC()
			state.LastSuccessAt = &at
		}
		clean[domain] = state
	}
	return clean
}

func validFeishuCapabilityDomain(domain string) bool {
	return domain == "docs" || domain == "base" || domain == "wiki"
}

func validFeishuCapabilityState(state string) bool {
	switch state {
	case model.FeishuCapabilityAvailable, model.FeishuCapabilityNeedsAppScope, model.FeishuCapabilityNeedsUserScope,
		model.FeishuCapabilityRevoked, model.FeishuCapabilityResourceDenied:
		return true
	default:
		return false
	}
}

func validFeishuCapabilityOutcome(outcome model.FeishuCapabilityOutcome) bool {
	return validFeishuCapabilityDomain(outcome.Domain) && validFeishuCapabilityState(outcome.State) &&
		(outcome.SucceededAt == nil || outcome.State == model.FeishuCapabilityAvailable) &&
		(outcome.CLIVersion == "" || validFeishuCLIVersionEvidence(outcome.CLIVersion))
}

func validFeishuAppIDEvidence(appID string) bool {
	if appID == "" {
		return false
	}
	if len(appID) > 64 || strings.TrimSpace(appID) != appID {
		return false
	}
	for _, char := range appID {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validFeishuCLIVersionEvidence(version string) bool {
	return version == "1.0.68"
}

// UpdateAccountConnectionState changes only the active tenant generation. It
// keeps the compatibility Connected fields synchronized with the new state
// machine without touching app metadata or credentials.
func (s *feishuWorkspaceStore) UpdateAccountConnectionState(
	ctx context.Context,
	userID uint,
	generation uint64,
	state string,
	connected bool,
	now time.Time,
) error {
	var connectedAt *time.Time
	if connected {
		value := now.UTC()
		connectedAt = &value
	}
	result := s.db.WithContext(ctx).
		Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ? AND generation = ? AND connection_state <> ?", userID, "lark", generation, model.FeishuConnectionDisconnecting).
		Updates(map[string]any{
			"connection_state": state,
			"connected":        connected,
			"connected_at":     connectedAt,
		})
	if result.Error != nil {
		return fmt.Errorf("update feishu account connection state: %w", result.Error)
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
		if !feishuAccountGenerationActive(&account, operation.Generation) {
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
		if !feishuAccountGenerationActive(&account, operation.Generation) {
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
		if !isSucceededCreateProofSource(&source, operation.UserID, operation.Generation, operation.AgentRunID) {
			return ErrFeishuProofReservationUnavailable
		}

		// A retry of the same idempotency key must reuse the already-bound
		// consumer even though that consumer is itself a later docs update.
		var existing model.FeishuOperation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND idempotency_key = ?", operation.UserID, operation.IdempotencyKey).
			Take(&existing).Error
		if err == nil {
			var consumption model.FeishuOperationProofConsumption
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("source_operation_id = ? AND consumer_operation_id = ?", source.ID, existing.ID).
				Take(&consumption).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrFeishuProofReservationUnavailable
				}
				return err
			}
			stored = &existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		intermediateWrites, err := countProofBlockingDocumentUpdates(tx, &source, operation.ID)
		if err != nil {
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
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
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

// TryClaimExecutionGate serializes business CLI invocations account-wide. Its
// global lock order is account -> execution gate; an active gate from any
// generation blocks all other owners until release or expiry.
func (s *feishuWorkspaceStore) TryClaimExecutionGate(
	ctx context.Context,
	userID uint,
	generation uint64,
	owner, operationID string,
	now, leaseUntil time.Time,
) (bool, error) {
	now = feishuExecutionGateClaimNow(now)
	leaseUntil = feishuExecutionGateLeaseUntil(leaseUntil)
	if userID == 0 || generation == 0 || owner == "" || operationID == "" || !leaseUntil.After(now) {
		return false, nil
	}
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", userID, "lark").
			Take(&account).Error; err != nil {
			return err
		}
		if !feishuAccountGenerationActive(&account, generation) {
			return nil
		}

		var gate model.FeishuOperationExecutionGate
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userID).
			Take(&gate).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			gate = model.FeishuOperationExecutionGate{
				UserID: userID, Generation: generation, LeaseOwner: owner,
				OperationID: operationID, LeaseUntil: &leaseUntil, UpdatedAt: now,
			}
			if err := tx.Create(&gate).Error; err != nil {
				return err
			}
			claimed = true
			return nil
		}
		if err != nil {
			return err
		}

		active := gate.LeaseOwner != "" && gate.LeaseUntil != nil && gate.LeaseUntil.After(now)
		sameOwner := gate.Generation == generation && gate.LeaseOwner == owner && gate.OperationID == operationID
		if active && !sameOwner {
			return nil
		}
		result := tx.Model(&model.FeishuOperationExecutionGate{}).
			Where("user_id = ?", userID).
			Updates(map[string]any{
				"generation": generation, "lease_owner": owner, "operation_id": operationID,
				"lease_until": leaseUntil, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		claimed = result.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("claim feishu execution gate: %w", err)
	}
	return claimed, nil
}

// RenewExecutionGate extends only an active lease held by the exact current
// generation, owner, and operation. An expired lease is never resurrected.
func (s *feishuWorkspaceStore) RenewExecutionGate(
	ctx context.Context,
	userID uint,
	generation uint64,
	owner, operationID string,
	now, leaseUntil time.Time,
) (bool, error) {
	now = feishuExecutionGateRenewNow(now)
	leaseUntil = feishuExecutionGateLeaseUntil(leaseUntil)
	if userID == 0 || generation == 0 || owner == "" || operationID == "" || !leaseUntil.After(now) {
		return false, nil
	}
	renewed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", userID, "lark").
			Take(&account).Error; err != nil {
			return err
		}
		if !feishuAccountGenerationActive(&account, generation) {
			return nil
		}

		var gate model.FeishuOperationExecutionGate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userID).
			Take(&gate).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if gate.Generation != generation || gate.LeaseOwner != owner || gate.OperationID != operationID ||
			gate.LeaseUntil == nil || !gate.LeaseUntil.After(now) {
			return nil
		}

		result := tx.Model(&model.FeishuOperationExecutionGate{}).
			Where("user_id = ? AND generation = ? AND lease_owner = ? AND operation_id = ?", userID, generation, owner, operationID).
			Where("lease_until > ?", now).
			Updates(map[string]any{"lease_until": leaseUntil, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		// The locked row already proved the exact active tuple. MySQL may report
		// zero affected rows when a same-timestamp renewal writes identical values.
		renewed = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("renew feishu execution gate: %w", err)
	}
	return renewed, nil
}

func feishuExecutionGateClaimNow(value time.Time) time.Time {
	return value.UTC().Truncate(time.Millisecond)
}

func feishuExecutionGateRenewNow(value time.Time) time.Time {
	normalized := value.UTC()
	millisecond := normalized.Truncate(time.Millisecond)
	if normalized.After(millisecond) {
		return millisecond.Add(time.Millisecond)
	}
	return millisecond
}

func feishuExecutionGateLeaseUntil(value time.Time) time.Time {
	return value.UTC().Truncate(time.Millisecond)
}

// ReleaseExecutionGate clears only the caller's current-generation lease. A
// stale owner cannot release a gate reclaimed after expiry.
func (s *feishuWorkspaceStore) ReleaseExecutionGate(
	ctx context.Context,
	userID uint,
	generation uint64,
	owner string,
	now time.Time,
) (bool, error) {
	now = feishuExecutionGateRenewNow(now)
	if userID == 0 || generation == 0 || owner == "" {
		return false, nil
	}
	result := s.db.WithContext(ctx).
		Model(&model.FeishuOperationExecutionGate{}).
		Where("user_id = ? AND generation = ? AND lease_owner = ?", userID, generation, owner).
		Updates(map[string]any{
			"lease_owner": "", "operation_id": "", "lease_until": nil, "updated_at": now,
		})
	if result.Error != nil {
		return false, fmt.Errorf("release feishu execution gate: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

// RetiredExecutionGateDrained reports whether no active account-wide business
// CLI lease remains after the supplied generation has been retired. It locks
// account then gate, matching the claim/renew order, but does not modify either
// row. An active lease from any generation blocks completion: while an account
// is disconnecting no new gate may be claimed, so release or expiry is the
// only safe cross-instance drain boundary before local vault deletion.
func (s *feishuWorkspaceStore) RetiredExecutionGateDrained(
	ctx context.Context,
	userID uint,
	retiredGeneration uint64,
	now time.Time,
) (bool, error) {
	if userID == 0 || retiredGeneration == 0 {
		return false, nil
	}
	now = feishuExecutionGateClaimNow(now)
	drained := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", userID, "lark").
			Take(&account).Error; err != nil {
			return err
		}
		if account.Generation != retiredGeneration+1 || account.ConnectionState != model.FeishuConnectionDisconnecting {
			return gorm.ErrRecordNotFound
		}

		var gate model.FeishuOperationExecutionGate
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userID).
			Take(&gate).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			drained = true
			return nil
		}
		if err != nil {
			return err
		}
		drained = gate.LeaseOwner == "" || gate.LeaseUntil == nil || !gate.LeaseUntil.After(now)
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("observe retired feishu execution gate: %w", err)
	}
	return drained, nil
}

// ClaimRetiredTeardown reuses the account-wide execution-gate row as the
// durable single-owner lease for destructive retired-HOME cleanup. It is called
// only after RetiredExecutionGateDrained, so it cannot overlap a business CLI.
func (s *feishuWorkspaceStore) ClaimRetiredTeardown(ctx context.Context, userID uint, retiredGeneration, disconnectingGeneration uint64, owner string, now, leaseUntil time.Time) (bool, error) {
	if userID == 0 || retiredGeneration == 0 || disconnectingGeneration != retiredGeneration+1 || owner == "" || !leaseUntil.After(now) {
		return false, nil
	}
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND provider = ?", userID, "lark").Take(&account).Error; err != nil {
			return err
		}
		if account.Generation != disconnectingGeneration || account.ConnectionState != model.FeishuConnectionDisconnecting {
			return nil
		}
		var gate model.FeishuOperationExecutionGate
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).Take(&gate).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&model.FeishuOperationExecutionGate{UserID: userID, Generation: retiredGeneration, LeaseOwner: owner, OperationID: "retired-teardown", LeaseUntil: &leaseUntil, UpdatedAt: now}).Error; err != nil {
				return err
			}
			claimed = true
			return nil
		}
		if err != nil {
			return err
		}
		if gate.LeaseOwner != "" && gate.LeaseUntil != nil && gate.LeaseUntil.After(now) {
			return nil
		}
		if err := tx.Model(&model.FeishuOperationExecutionGate{}).Where("user_id = ?", userID).Updates(map[string]any{"generation": retiredGeneration, "lease_owner": owner, "operation_id": "retired-teardown", "lease_until": leaseUntil, "updated_at": now}).Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed, err
}

// RenewRetiredTeardown extends an already-held retired-workspace cleanup
// lease. It never revives an expired lease: ownership, generation, operation
// marker, and the old unexpired deadline must all still match in one
// account-then-gate transaction. A false result is an ownership loss, not a
// retry invitation for a stale cleanup worker.
func (s *feishuWorkspaceStore) RenewRetiredTeardown(
	ctx context.Context,
	userID uint,
	retiredGeneration, disconnectingGeneration uint64,
	owner string,
	now, leaseUntil time.Time,
) (bool, error) {
	if userID == 0 || retiredGeneration == 0 || disconnectingGeneration != retiredGeneration+1 || owner == "" || !leaseUntil.After(now) {
		return false, nil
	}
	renewed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", userID, "lark").Take(&account).Error; err != nil {
			return err
		}
		if account.Generation != disconnectingGeneration || account.ConnectionState != model.FeishuConnectionDisconnecting {
			return nil
		}
		result := tx.Model(&model.FeishuOperationExecutionGate{}).
			Where("user_id = ? AND generation = ? AND lease_owner = ? AND operation_id = ? AND lease_until > ?", userID, retiredGeneration, owner, "retired-teardown", now).
			Updates(map[string]any{"lease_until": leaseUntil, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		renewed = result.RowsAffected == 1
		return nil
	})
	return renewed, err
}

// ReleaseRetiredTeardown releases only the exact durable teardown owner.
func (s *feishuWorkspaceStore) ReleaseRetiredTeardown(ctx context.Context, userID uint, retiredGeneration uint64, owner string, now time.Time) error {
	if userID == 0 || retiredGeneration == 0 || owner == "" {
		return gorm.ErrRecordNotFound
	}
	result := s.db.WithContext(ctx).Model(&model.FeishuOperationExecutionGate{}).Where("user_id = ? AND generation = ? AND lease_owner = ? AND operation_id = ?", userID, retiredGeneration, owner, "retired-teardown").Updates(map[string]any{"lease_owner": "", "operation_id": "", "lease_until": nil, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	return nil
}

// CompleteRetiredTeardown is the only destructive completion boundary for a
// retired workspace. It deletes the retired vault and clears the disconnecting
// account only while the exact live teardown lease is still owned by caller.
// Keeping those writes in one transaction prevents a worker that lost its
// lease from deleting credentials or finalizing a newer owner's teardown.
func (s *feishuWorkspaceStore) CompleteRetiredTeardown(
	ctx context.Context,
	userID uint,
	retiredGeneration, disconnectingGeneration uint64,
	owner string,
	now time.Time,
) (bool, error) {
	if userID == 0 || retiredGeneration == 0 || disconnectingGeneration != retiredGeneration+1 || owner == "" {
		return false, nil
	}
	completed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", userID, "lark").Take(&account).Error; err != nil {
			return err
		}
		if account.Generation != disconnectingGeneration || account.ConnectionState != model.FeishuConnectionDisconnecting {
			return nil
		}

		var gate model.FeishuOperationExecutionGate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).Take(&gate).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if gate.Generation != retiredGeneration || gate.LeaseOwner != owner || gate.OperationID != "retired-teardown" || gate.LeaseUntil == nil || !gate.LeaseUntil.After(now) {
			return nil
		}

		if result := tx.Where("user_id = ? AND generation = ?", userID, retiredGeneration).Delete(&model.FeishuCLIVault{}); result.Error != nil {
			return fmt.Errorf("delete retired feishu CLI vault: %w", result.Error)
		}
		accountResult := tx.Model(&model.UserThirdPartyAccount{}).
			Where("user_id = ? AND provider = ? AND generation = ? AND connection_state = ?", userID, "lark", disconnectingGeneration, model.FeishuConnectionDisconnecting).
			Updates(map[string]any{
				"app_id":                "",
				"app_secret_enc":        nil,
				"access_token_enc":      nil,
				"refresh_token_enc":     nil,
				"token_expires_at":      nil,
				"scopes":                "",
				"connected":             false,
				"connected_at":          nil,
				"connection_state":      model.FeishuConnectionNone,
				"lark_cli_version":      "",
				"granted_scopes_json":   nil,
				"capability_state_json": nil,
				"last_success_at":       nil,
				"last_error_code":       nil,
			})
		if accountResult.Error != nil {
			return fmt.Errorf("complete feishu retired teardown: %w", accountResult.Error)
		}
		if accountResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if result := tx.Model(&model.FeishuOperationExecutionGate{}).
			Where("user_id = ? AND generation = ? AND lease_owner = ? AND operation_id = ?", userID, retiredGeneration, owner, "retired-teardown").
			Updates(map[string]any{"lease_owner": "", "operation_id": "", "lease_until": nil, "updated_at": now}); result.Error != nil {
			return fmt.Errorf("release completed feishu retired teardown: %w", result.Error)
		}
		completed = true
		return nil
	})
	return completed, err
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

// isOperationProofBound verifies the immutable audit tuple for a source and
// consumer. It deliberately has no release/delete counterpart.
func (s *feishuWorkspaceStore) isOperationProofBound(
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

func isSucceededCreateProofSource(
	source *model.FeishuOperation,
	userID uint,
	generation uint64,
	agentRunID uint64,
) bool {
	return source != nil && source.UserID == userID && source.Generation == generation &&
		source.AgentRunID == agentRunID && source.State == model.FeishuOperationSucceeded &&
		source.FinishedAt != nil &&
		(source.CommandPath == "docs +create" || source.CommandPath == "wiki +node-create")
}

func countProofBlockingDocumentUpdates(
	db *gorm.DB,
	source *model.FeishuOperation,
	consumerOperationID string,
) (int64, error) {
	if db == nil || source == nil {
		return 0, ErrFeishuProofReservationUnavailable
	}
	query := db.Model(&model.FeishuOperation{}).
		Where("user_id = ? AND generation = ? AND agent_run_id = ?", source.UserID, source.Generation, source.AgentRunID).
		Where("command_path = ?", "docs +update")
	if consumerOperationID != "" {
		query = query.Where("id <> ?", consumerOperationID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// IsOperationProofUsable revalidates the durable proof binding and rejects it
// when the same run contains any other document update. This conservative rule
// avoids ordering operations by clocks from different service instances. Callers
// must hold the account execution gate while checking and acting on this result.
func (s *feishuWorkspaceStore) IsOperationProofUsable(
	ctx context.Context,
	userID uint,
	generation uint64,
	agentRunID uint64,
	sourceOperationID, consumerOperationID string,
) (bool, error) {
	bound, err := s.isOperationProofBound(
		ctx, userID, generation, agentRunID, sourceOperationID, consumerOperationID,
	)
	if err != nil || !bound {
		return false, err
	}

	var source model.FeishuOperation
	if err := s.db.WithContext(ctx).
		Where("id = ?", sourceOperationID).
		Take(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("load feishu proof source: %w", err)
	}
	if !isSucceededCreateProofSource(&source, userID, generation, agentRunID) {
		return false, nil
	}

	intermediateWrites, err := countProofBlockingDocumentUpdates(s.db.WithContext(ctx), &source, consumerOperationID)
	if err != nil {
		return false, fmt.Errorf("revalidate feishu proof usage: %w", err)
	}
	return intermediateWrites == 0, nil
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

type feishuOperationRecoveryBinding struct {
	Status    string `json:"status"`
	SessionID string `json:"session_id"`
	Phase     string `json:"phase"`
}

// RefreshOperationSession atomically supersedes the source authorization
// session, creates its replacement, and moves the waiting operation's durable
// binding. The source is normally pending; the same transaction also admits a
// legacy superseded source only when the operation still points to it exactly.
// No partially refreshed operation can be committed if any tenant, state, or
// session fence fails.
func (s *feishuWorkspaceStore) RefreshOperationSession(
	ctx context.Context,
	userID uint,
	generation uint64,
	oldSessionID, operationID, waitingState, connectionState string,
	replacement *model.FeishuAuthSession,
	replacementSummary []byte,
	now time.Time,
) (*model.FeishuAuthSession, error) {
	if userID == 0 || generation == 0 || strings.TrimSpace(oldSessionID) == "" || strings.TrimSpace(operationID) == "" ||
		strings.TrimSpace(waitingState) == "" || !refreshableFeishuConnectionState(connectionState) || replacement == nil || replacement.ID == oldSessionID ||
		replacement.UserID != userID || replacement.Generation != generation || replacement.OperationID == nil ||
		*replacement.OperationID != operationID || replacement.State != model.FeishuAuthSessionPending || !json.Valid(replacementSummary) {
		return nil, gorm.ErrRecordNotFound
	}
	var nextBinding feishuOperationRecoveryBinding
	if err := json.Unmarshal(replacementSummary, &nextBinding); err != nil || nextBinding.Status != waitingState ||
		nextBinding.SessionID != replacement.ID || nextBinding.Phase != replacement.Phase {
		return nil, gorm.ErrRecordNotFound
	}

	var refreshed *model.FeishuAuthSession
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := lockFeishuAuthAccount(tx, userID, generation)
		if err != nil {
			return fmt.Errorf("lock feishu account for session refresh: %w", err)
		}
		if !active {
			return gorm.ErrRecordNotFound
		}
		var oldSession model.FeishuAuthSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ?", oldSessionID, userID, generation).
			Where("state IN ?", []string{model.FeishuAuthSessionPending, model.FeishuAuthSessionSuperseded}).
			Take(&oldSession).Error; err != nil {
			return err
		}
		if oldSession.OperationID == nil || *oldSession.OperationID != operationID || oldSession.Phase != replacement.Phase ||
			!equalRequestedScopes(oldSession.RequestedScopesJSON, replacement.RequestedScopesJSON) {
			return gorm.ErrRecordNotFound
		}

		var operation model.FeishuOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ? AND state = ?", operationID, userID, generation, waitingState).
			Take(&operation).Error; err != nil {
			return err
		}
		var current feishuOperationRecoveryBinding
		if err := json.Unmarshal(operation.ResultSummaryJSON, &current); err != nil || current.Status != waitingState ||
			current.SessionID != oldSessionID || current.Phase != replacement.Phase {
			return gorm.ErrRecordNotFound
		}
		if oldSession.State == model.FeishuAuthSessionSuperseded {
			// The pre-atomic implementation could have created a second pending
			// session without moving the operation summary to it. Before repairing
			// that exact legacy source, fence every matching orphan: a live lease
			// wins, while an unleased/expired one is retired in this transaction.
			var orphans []model.FeishuAuthSession
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("user_id = ? AND generation = ? AND operation_id = ? AND phase = ? AND state = ?", userID, generation, operationID, replacement.Phase, model.FeishuAuthSessionPending).
				Where("id <> ?", oldSessionID).
				Find(&orphans).Error; err != nil {
				return err
			}
			for index := range orphans {
				orphan := &orphans[index]
				if !equalRequestedScopes(orphan.RequestedScopesJSON, replacement.RequestedScopesJSON) {
					continue
				}
				if orphan.LeaseUntil != nil && orphan.LeaseUntil.After(now) {
					return gorm.ErrRecordNotFound
				}
				result := tx.Model(&model.FeishuAuthSession{}).
					Where("id = ? AND user_id = ? AND generation = ? AND state = ?", orphan.ID, userID, generation, model.FeishuAuthSessionPending).
					Updates(map[string]any{
						"state": model.FeishuAuthSessionSuperseded, "completed_at": now.UTC(),
						"lease_owner": "", "lease_until": nil,
					})
				if result.Error != nil {
					return fmt.Errorf("retire legacy orphan feishu auth session: %w", result.Error)
				}
				if result.RowsAffected != 1 {
					return gorm.ErrRecordNotFound
				}
			}
		}
		candidate := *replacement
		if err := tx.Create(&candidate).Error; err != nil {
			return fmt.Errorf("create replacement feishu auth session: %w", err)
		}
		oldResult := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ?", oldSessionID, userID, generation).
			Where("state IN ?", []string{model.FeishuAuthSessionPending, model.FeishuAuthSessionSuperseded}).
			Updates(map[string]any{
				"state": model.FeishuAuthSessionSuperseded, "completed_at": now.UTC(),
				"lease_owner": "", "lease_until": nil,
			})
		if oldResult.Error != nil {
			return fmt.Errorf("supersede refreshed feishu auth session: %w", oldResult.Error)
		}
		if oldResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		accountResult := tx.Model(&model.UserThirdPartyAccount{}).
			Where("user_id = ? AND provider = ? AND generation = ? AND connection_state <> ?", userID, "lark", generation, model.FeishuConnectionDisconnecting).
			Updates(map[string]any{"connection_state": connectionState, "connected": false})
		if accountResult.Error != nil {
			return fmt.Errorf("update refreshed feishu account state: %w", accountResult.Error)
		}
		if accountResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		result := tx.Model(&model.FeishuOperation{}).
			Where("id = ? AND user_id = ? AND generation = ? AND state = ?", operationID, userID, generation, waitingState).
			Update("result_summary_json", append([]byte(nil), replacementSummary...))
		if result.Error != nil {
			return fmt.Errorf("refresh feishu operation recovery session: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		refreshed = &candidate
		return nil
	})
	if err != nil {
		return nil, err
	}
	return refreshed, nil
}

// RestoreOperationSessionRefresh compensates a replacement that committed but
// could not start a usable local authorization worker. Until a new URL reaches
// the browser, that browser can only retry the original session ID; restoring
// the exact old summary keeps that retry path durable and tenant-fenced.
func (s *feishuWorkspaceStore) RestoreOperationSessionRefresh(
	ctx context.Context,
	userID uint,
	generation uint64,
	oldSessionID, replacementSessionID, operationID, waitingState string,
	oldSummary []byte,
	now time.Time,
) error {
	if userID == 0 || generation == 0 || strings.TrimSpace(oldSessionID) == "" ||
		strings.TrimSpace(replacementSessionID) == "" || replacementSessionID == oldSessionID ||
		strings.TrimSpace(operationID) == "" || strings.TrimSpace(waitingState) == "" || !json.Valid(oldSummary) {
		return gorm.ErrRecordNotFound
	}
	var oldBinding feishuOperationRecoveryBinding
	if err := json.Unmarshal(oldSummary, &oldBinding); err != nil || oldBinding.Status != waitingState || oldBinding.SessionID != oldSessionID {
		return gorm.ErrRecordNotFound
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := lockFeishuAuthAccount(tx, userID, generation)
		if err != nil {
			return fmt.Errorf("lock feishu account for session refresh restore: %w", err)
		}
		if !active {
			return gorm.ErrRecordNotFound
		}

		var oldSession model.FeishuAuthSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ? AND state = ?", oldSessionID, userID, generation, model.FeishuAuthSessionSuperseded).
			Take(&oldSession).Error; err != nil {
			return err
		}
		if oldSession.OperationID == nil || *oldSession.OperationID != operationID || oldSession.Phase != oldBinding.Phase {
			return gorm.ErrRecordNotFound
		}

		var replacement model.FeishuAuthSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ?", replacementSessionID, userID, generation).
			Where("state IN ?", []string{
				model.FeishuAuthSessionPending,
				model.FeishuAuthSessionFailed,
				model.FeishuAuthSessionSuperseded,
			}).
			Take(&replacement).Error; err != nil {
			return err
		}
		if replacement.OperationID == nil || *replacement.OperationID != operationID || replacement.Phase != oldSession.Phase ||
			!equalRequestedScopes(replacement.RequestedScopesJSON, oldSession.RequestedScopesJSON) {
			return gorm.ErrRecordNotFound
		}
		if replacement.State == model.FeishuAuthSessionPending && replacement.LeaseUntil != nil && replacement.LeaseUntil.After(now) {
			// A historical browser card must never supersede a replacement that
			// still has a live worker in this or another service instance.
			return gorm.ErrRecordNotFound
		}

		var operation model.FeishuOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ? AND state = ?", operationID, userID, generation, waitingState).
			Take(&operation).Error; err != nil {
			return err
		}
		var current feishuOperationRecoveryBinding
		if err := json.Unmarshal(operation.ResultSummaryJSON, &current); err != nil || current.Status != waitingState ||
			current.SessionID != replacementSessionID || current.Phase != oldSession.Phase {
			return gorm.ErrRecordNotFound
		}

		replacementResult := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ?", replacementSessionID, userID, generation).
			Where("state IN ?", []string{
				model.FeishuAuthSessionPending,
				model.FeishuAuthSessionFailed,
				model.FeishuAuthSessionSuperseded,
			}).
			Updates(map[string]any{
				"state": model.FeishuAuthSessionSuperseded, "completed_at": now.UTC(),
				"lease_owner": "", "lease_until": nil,
			})
		if replacementResult.Error != nil {
			return fmt.Errorf("retire failed replacement feishu auth session: %w", replacementResult.Error)
		}
		if replacementResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		oldResult := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ? AND state = ?", oldSessionID, userID, generation, model.FeishuAuthSessionSuperseded).
			Updates(map[string]any{
				"state": model.FeishuAuthSessionPending, "completed_at": nil,
				"lease_owner": "", "lease_until": nil,
			})
		if oldResult.Error != nil {
			return fmt.Errorf("restore original feishu auth session: %w", oldResult.Error)
		}
		if oldResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		result := tx.Model(&model.FeishuOperation{}).
			Where("id = ? AND user_id = ? AND generation = ? AND state = ?", operationID, userID, generation, waitingState).
			Update("result_summary_json", append([]byte(nil), oldSummary...))
		if result.Error != nil {
			return fmt.Errorf("restore feishu operation recovery session: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return gorm.ErrRecordNotFound
		}
		return fmt.Errorf("restore feishu operation session refresh: %w", err)
	}
	return nil
}

func refreshableFeishuConnectionState(state string) bool {
	return state == model.FeishuConnectionCreatingApp || state == model.FeishuConnectionWaitingAppApproval ||
		state == model.FeishuConnectionWaitingUserAuth
}

// ListTerminalOperationsForGeneration returns the lifecycle-selected terminal
// operations for exactly one user and generation. The caller supplies only
// fixed server state constants; no browser input reaches this query.
func (s *feishuWorkspaceStore) ListTerminalOperationsForGeneration(
	ctx context.Context,
	userID uint,
	generation uint64,
	states []string,
) ([]model.FeishuOperation, error) {
	if userID == 0 || generation == 0 || len(states) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var operations []model.FeishuOperation
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND generation = ? AND state IN ?", userID, generation, states).
		Order("created_at ASC, id ASC").
		Find(&operations).Error; err != nil {
		return nil, fmt.Errorf("list terminal feishu operations: %w", err)
	}
	return operations, nil
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
		Where("user_third_party_account.generation = feishu_operation.generation").
		Where("user_third_party_account.connection_state <> ?", model.FeishuConnectionDisconnecting)
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
	return transitionFeishuOperation(s.db.WithContext(ctx), userID, generation, id, owner, from, to, now, fields)
}

// TransitionOperationWithCapabilityOutcome commits the operation transition and
// its status-cache evidence together. The account row is locked before the
// operation row, matching create/retire lock order and preventing a success
// result from becoming durable while its Status metadata is lost.
func (s *feishuWorkspaceStore) TransitionOperationWithCapabilityOutcome(
	ctx context.Context,
	userID uint,
	generation uint64,
	id, owner string,
	from []string,
	to string,
	now time.Time,
	fields map[string]any,
	outcome model.FeishuCapabilityOutcome,
) error {
	if !validFeishuCapabilityOutcome(outcome) {
		return gorm.ErrRecordNotFound
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ? AND generation = ? AND connection_state <> ?", userID, "lark", generation, model.FeishuConnectionDisconnecting).
			Take(&account).Error; err != nil {
			return err
		}
		if err := transitionFeishuOperation(tx, userID, generation, id, owner, from, to, now, fields); err != nil {
			return err
		}
		return recordFeishuCapabilityOutcome(tx, &account, outcome)
	})
}

func transitionFeishuOperation(db *gorm.DB, userID uint, generation uint64, id, owner string, from []string, to string, now time.Time, fields map[string]any) error {
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

	activeGeneration := db.
		Model(&model.UserThirdPartyAccount{}).
		Select("1").
		Where("user_third_party_account.user_id = feishu_operation.user_id").
		Where("user_third_party_account.provider = ?", "lark").
		Where("user_third_party_account.generation = feishu_operation.generation").
		Where("user_third_party_account.connection_state <> ?", model.FeishuConnectionDisconnecting)
	result := db.
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
