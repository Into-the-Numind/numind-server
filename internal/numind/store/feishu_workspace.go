package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/model"
)

const (
	feishuSucceededCreateProofLimit     = 32
	maxFeishuCLIVaultCiphertextBytes    = 64 << 20
	feishuConnectionBootstrapStaleAfter = 2 * time.Minute
)

// ErrFeishuProofReservationUnavailable means a candidate cannot atomically
// reserve the requested one-shot proof. Callers may safely retry creation only
// after removing the proof exemption from the encrypted request.
var ErrFeishuProofReservationUnavailable = errors.New("feishu operation proof reservation unavailable")

// ErrFeishuConnectionOperationInProgress means the current account generation
// already has one connection bootstrap owner (Agent or Settings). A caller must
// join/observe that flow instead of creating another personal app worker.
var ErrFeishuConnectionOperationInProgress = errors.New("feishu connection operation already in progress")

var errFeishuAuthSessionInvalidProtocolShape = errors.New("invalid feishu auth session protocol shape")

// FeishuDeviceAuthCredentialAttach is the complete, encrypted protocol-v2
// credential written after a worker starts a device authorization flow.
type FeishuDeviceAuthCredentialAttach struct {
	UserID       uint
	Generation   uint64
	SessionID    string
	LeaseOwner   string
	AppID        string
	Ciphertext   []byte
	KeyVersion   string
	ResumeExpiry time.Time
	ScopeHash    string
	Now          time.Time
}

// FeishuDeviceAuthReplacement binds one terminal device authorization attempt
// to its protocol-v2 retry and the exact waiting operation summary.
type FeishuDeviceAuthReplacement struct {
	UserID               uint
	Generation           uint64
	OldSessionID         string
	LeaseOwner           string
	TerminalState        string
	NewSession           *model.FeishuAuthSession
	OperationID          string
	ExpectedWaitingState string
	OldSummary           []byte
	NewSummary           []byte
	Now                  time.Time
}

// FeishuDeviceAuthSuccess contains every fence required to atomically publish
// a completed device-authorization HOME and terminalize its owning session.
type FeishuDeviceAuthSuccess struct {
	UserID                uint
	Generation            uint64
	SessionID             string
	OperationID           string
	LeaseOwner            string
	ExpectedAppID         string
	ExpectedWaitingState  string
	Candidate             model.FeishuCLIVault
	ExpectedVaultRevision uint64
	Evidence              model.FeishuConnectionEvidence
	Now                   time.Time
}

// FeishuDeviceAuthCleanupPage reports one bounded primary-key sweep page.
type FeishuDeviceAuthCleanupPage struct {
	NextSessionID string
	Scanned       int
	Cleared       int
	Done          bool
}

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
	AttachDeviceAuthCredential(ctx context.Context, input FeishuDeviceAuthCredentialAttach) error
	ReleaseDeviceAuthLease(ctx context.Context, userID uint, generation uint64, sessionID, leaseOwner string, now time.Time) (bool, error)
	TerminalizeDeviceAuthSession(ctx context.Context, userID uint, generation uint64, sessionID, leaseOwner, terminalState string, now time.Time) error
	ReplaceDeviceAuthSession(ctx context.Context, input FeishuDeviceAuthReplacement) (*model.FeishuAuthSession, error)
	SweepDeviceAuthCredentials(ctx context.Context, before time.Time, afterSessionID string, scanLimit int) (FeishuDeviceAuthCleanupPage, error)
	RefreshOperationSession(ctx context.Context, userID uint, generation uint64, oldSessionID, operationID, waitingState, connectionState string, replacement *model.FeishuAuthSession, replacementSummary []byte, now time.Time) (*model.FeishuAuthSession, error)
	RestoreOperationSessionRefresh(ctx context.Context, userID uint, generation uint64, oldSessionID, replacementSessionID, operationID, waitingState string, oldSummary []byte, now time.Time) error
	ClaimSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error)
	RenewSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error)
	UpdateSessionState(ctx context.Context, userID uint, generation uint64, id, owner, state string, now time.Time, completedAt *time.Time) error
	FinalizeSessionCompleted(ctx context.Context, userID uint, generation uint64, id, owner, accountState string, connected bool, now time.Time, evidence model.FeishuConnectionEvidence) error
	FinalizeDeviceAuthSuccess(ctx context.Context, input FeishuDeviceAuthSuccess) error
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
	ListSucceededBaseCreatesForRun(ctx context.Context, userID uint, generation uint64, agentRunID uint64) ([]model.FeishuOperation, error)
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
	protocolVersion := normalizedFeishuAuthProtocolVersion(session.ProtocolVersion)
	if !validFeishuAuthSessionCreationShape(session, protocolVersion) {
		return nil, false, errFeishuAuthSessionInvalidProtocolShape
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
		var activeConnections []model.FeishuOperation
		activeErr := tx.Where(
			"user_id = ? AND generation = ? AND command_path = ? AND state IN ?",
			session.UserID, session.Generation, "workspace connect", feishuConnectionOperationActiveStates(),
		).Order("created_at ASC, id ASC").Find(&activeConnections).Error
		if activeErr != nil {
			return activeErr
		}
		for index := range activeConnections {
			activeConnection := &activeConnections[index]
			if session.OperationID != nil && *session.OperationID == activeConnection.ID {
				continue
			}
			blocks, fenceErr := feishuConnectionOperationBlocks(tx, activeConnection, time.Now().UTC())
			if fenceErr != nil {
				return fenceErr
			}
			if blocks {
				return ErrFeishuConnectionOperationInProgress
			}
		}
		if session.OperationID != nil {
			var connectionOperation model.FeishuOperation
			connectionErr := tx.Where("id = ? AND user_id = ? AND generation = ? AND command_path = ?",
				*session.OperationID, session.UserID, session.Generation, "workspace connect").Take(&connectionOperation).Error
			if connectionErr == nil && !containsFeishuOperationState(feishuConnectionOperationActiveStates(), connectionOperation.State) {
				return ErrFeishuConnectionOperationInProgress
			}
			if connectionErr != nil && !errors.Is(connectionErr, gorm.ErrRecordNotFound) {
				return connectionErr
			}
		}
		// Exact-session reuse was handled above. Any other pending recovery is
		// the current generation's bootstrap owner, regardless of whether it
		// came from Settings, lark_connect, or a real business operation.
		var pendingOwners []model.FeishuAuthSession
		pendingErr := tx.Where(
			"user_id = ? AND generation = ? AND state = ?",
			session.UserID, session.Generation, model.FeishuAuthSessionPending,
		).Order("created_at ASC, id ASC").Find(&pendingOwners).Error
		if pendingErr != nil {
			return pendingErr
		}
		for index := range pendingOwners {
			pendingOwner := &pendingOwners[index]
			if feishuAuthProtocolUpgradeCanCoexist(pendingOwner, session, time.Now().UTC()) {
				// A lease-free v1 row is inert rolling-upgrade state. Keep it
				// pending for the existing durable refresh/race contract, while
				// allowing v2 to become the newest active protocol owner.
				continue
			}
			return ErrFeishuConnectionOperationInProgress
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
		candidate.ProtocolVersion = protocolVersion
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
		"user_id = ? AND generation = ? AND phase = ? AND state = ? AND protocol_version = ?",
		intent.UserID,
		intent.Generation,
		intent.Phase,
		state,
		normalizedFeishuAuthProtocolVersion(intent.ProtocolVersion),
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
			if !validPersistedFeishuAuthSessionShape(&candidates[i]) {
				return nil, errFeishuAuthSessionInvalidProtocolShape
			}
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

func feishuAuthProtocolUpgradeCanCoexist(current, candidate *model.FeishuAuthSession, now time.Time) bool {
	return current != nil && candidate != nil && current.OperationID != nil && candidate.OperationID != nil &&
		*current.OperationID == *candidate.OperationID && current.Phase == candidate.Phase &&
		normalizedFeishuAuthProtocolVersion(current.ProtocolVersion) == 1 &&
		normalizedFeishuAuthProtocolVersion(candidate.ProtocolVersion) == 2 &&
		equalRequestedScopes(current.RequestedScopesJSON, candidate.RequestedScopesJSON) &&
		(current.LeaseUntil == nil || !current.LeaseUntil.After(now))
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

// SupersedeSessionForUser retires one still-pending legacy session after locking
// its current account generation. Protocol v2 is fenced while any live worker
// owns it and must instead use an exact-owner terminal or replacement primitive.
func (s *feishuWorkspaceStore) SupersedeSessionForUser(
	ctx context.Context,
	userID uint,
	generation uint64,
	id string,
	now time.Time,
) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := lockFeishuAuthAccount(tx, userID, generation)
		if err != nil {
			return err
		}
		if !active {
			return gorm.ErrRecordNotFound
		}
		result := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ? AND state = ?", id, userID, generation, model.FeishuAuthSessionPending).
			Where("protocol_version = 1 OR COALESCE(lease_owner, '') = '' OR lease_until IS NULL OR lease_until <= ?", now).
			Updates(map[string]any{
				"state": model.FeishuAuthSessionSuperseded, "completed_at": now.UTC(),
				"resume_credential_ciphertext": nil, "resume_key_version": nil, "resume_expires_at": nil,
				"lease_owner": "", "lease_until": nil,
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
		return fmt.Errorf("supersede feishu auth session: %w", err)
	}
	return nil
}

// AttachDeviceAuthCredential atomically moves an owned protocol-v2 session to
// its durable waiting shape and releases the start lease.
func (s *feishuWorkspaceStore) AttachDeviceAuthCredential(ctx context.Context, input FeishuDeviceAuthCredentialAttach) error {
	if input.UserID == 0 || input.Generation == 0 || strings.TrimSpace(input.SessionID) == "" ||
		strings.TrimSpace(input.LeaseOwner) == "" || strings.TrimSpace(input.AppID) == "" ||
		len(input.Ciphertext) == 0 || !validFeishuResumeKeyVersion(input.KeyVersion) ||
		!input.ResumeExpiry.After(input.Now) || !validFeishuScopeHash(input.ScopeHash) {
		return gorm.ErrRecordNotFound
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", input.UserID, "lark").
			Take(&account).Error; err != nil {
			return err
		}
		if !feishuAccountGenerationActive(&account, input.Generation) || account.AppID != input.AppID {
			return gorm.ErrRecordNotFound
		}

		var session model.FeishuAuthSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ?", input.SessionID, input.UserID, input.Generation).
			Where("state = ? AND protocol_version = ?", model.FeishuAuthSessionPending, 2).
			Where("lease_owner = ? AND lease_until > ?", input.LeaseOwner, input.Now).
			Take(&session).Error; err != nil {
			return err
		}
		if session.Phase != model.FeishuAuthPhaseUserAuth || session.ScopeHash != input.ScopeHash || !validFeishuDeviceAuthPreStartShape(&session) {
			return gorm.ErrRecordNotFound
		}
		if !session.ExpiresAt.After(input.Now) || input.ResumeExpiry.After(session.ExpiresAt) {
			return gorm.ErrRecordNotFound
		}

		accountResult := tx.Model(&model.UserThirdPartyAccount{}).
			Where("user_id = ? AND provider = ? AND generation = ? AND app_id = ? AND connection_state <> ?", input.UserID, "lark", input.Generation, input.AppID, model.FeishuConnectionDisconnecting).
			Updates(map[string]any{"connection_state": model.FeishuConnectionWaitingUserAuth, "connected": false, "connected_at": nil, "updated_at": input.Now.UTC()})
		if accountResult.Error != nil {
			return fmt.Errorf("update feishu device auth account: %w", accountResult.Error)
		}
		if accountResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		credentialResult := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ?", input.SessionID, input.UserID, input.Generation).
			Where("state = ? AND protocol_version = ?", model.FeishuAuthSessionPending, 2).
			Where("lease_owner = ? AND lease_until > ?", input.LeaseOwner, input.Now).
			Updates(map[string]any{
				"resume_credential_ciphertext": append([]byte(nil), input.Ciphertext...),
				"resume_key_version":           input.KeyVersion,
				"resume_expires_at":            input.ResumeExpiry.UTC(),
				"scope_hash":                   input.ScopeHash,
			})
		if credentialResult.Error != nil {
			return fmt.Errorf("persist feishu device auth credential: %w", credentialResult.Error)
		}
		if credentialResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		leaseResult := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ?", input.SessionID, input.UserID, input.Generation).
			Where("state = ? AND protocol_version = ?", model.FeishuAuthSessionPending, 2).
			Where("lease_owner = ? AND lease_until > ?", input.LeaseOwner, input.Now).
			Updates(map[string]any{"lease_owner": "", "lease_until": nil})
		if leaseResult.Error != nil {
			return fmt.Errorf("release feishu device auth start lease: %w", leaseResult.Error)
		}
		if leaseResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return gorm.ErrRecordNotFound
		}
		return fmt.Errorf("attach feishu device auth credential: %w", err)
	}
	return nil
}

// ReleaseDeviceAuthLease releases only an exact live protocol-v2 owner and
// deliberately retains the encrypted resume credential.
func (s *feishuWorkspaceStore) ReleaseDeviceAuthLease(
	ctx context.Context,
	userID uint,
	generation uint64,
	sessionID string,
	leaseOwner string,
	now time.Time,
) (bool, error) {
	if userID == 0 || generation == 0 || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(leaseOwner) == "" {
		return false, nil
	}
	released := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := lockFeishuAuthAccount(tx, userID, generation)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		var session model.FeishuAuthSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ?", sessionID, userID, generation).
			Where("state = ? AND protocol_version = ?", model.FeishuAuthSessionPending, 2).
			Where("lease_owner = ? AND lease_until > ?", leaseOwner, now).
			Take(&session).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if session.Phase != model.FeishuAuthPhaseUserAuth || !validFeishuDeviceAuthShape(&session) {
			return nil
		}
		result := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ?", sessionID, userID, generation).
			Where("state = ? AND protocol_version = ?", model.FeishuAuthSessionPending, 2).
			Where("lease_owner = ? AND lease_until > ?", leaseOwner, now).
			Updates(map[string]any{"lease_owner": "", "lease_until": nil})
		if result.Error != nil {
			return result.Error
		}
		released = result.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("release feishu device auth lease: %w", err)
	}
	return released, nil
}

// TerminalizeDeviceAuthSession terminally updates an exact owned device-auth
// session while clearing every persisted resume secret in the same transaction.
func (s *feishuWorkspaceStore) TerminalizeDeviceAuthSession(
	ctx context.Context,
	userID uint,
	generation uint64,
	sessionID string,
	leaseOwner string,
	terminalState string,
	now time.Time,
) error {
	if userID == 0 || generation == 0 || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(leaseOwner) == "" ||
		!validFeishuDeviceAuthFailureTerminalState(terminalState) {
		return gorm.ErrRecordNotFound
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := lockFeishuAuthAccount(tx, userID, generation)
		if err != nil {
			return err
		}
		if !active {
			return gorm.ErrRecordNotFound
		}
		var session model.FeishuAuthSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ?", sessionID, userID, generation).
			Where("state = ? AND protocol_version = ?", model.FeishuAuthSessionPending, 2).
			Where("lease_owner = ? AND lease_until > ?", leaseOwner, now).
			Take(&session).Error; err != nil {
			return err
		}
		if session.Phase != model.FeishuAuthPhaseUserAuth || !validFeishuDeviceAuthShape(&session) {
			return gorm.ErrRecordNotFound
		}
		result := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ?", sessionID, userID, generation).
			Where("state = ? AND protocol_version = ?", model.FeishuAuthSessionPending, 2).
			Where("lease_owner = ? AND lease_until > ?", leaseOwner, now).
			Updates(map[string]any{
				"state": terminalState, "completed_at": now.UTC(),
				"resume_credential_ciphertext": nil, "resume_key_version": nil, "resume_expires_at": nil,
				"lease_owner": "", "lease_until": nil,
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
		return fmt.Errorf("terminalize feishu device auth session: %w", err)
	}
	return nil
}

// ReplaceDeviceAuthSession atomically terminalizes the exact source attempt,
// creates a credential-free protocol-v2 retry, and rebinds its operation.
func (s *feishuWorkspaceStore) ReplaceDeviceAuthSession(
	ctx context.Context,
	input FeishuDeviceAuthReplacement,
) (*model.FeishuAuthSession, error) {
	if !validFeishuDeviceAuthReplacementInput(input) {
		return nil, gorm.ErrRecordNotFound
	}
	oldBinding, newBinding, ok := validFeishuDeviceAuthReplacementBindings(input)
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}

	var stored *model.FeishuAuthSession
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := lockFeishuAuthAccount(tx, input.UserID, input.Generation)
		if err != nil {
			return fmt.Errorf("lock feishu account for device auth replacement: %w", err)
		}
		if !active {
			return gorm.ErrRecordNotFound
		}

		var old model.FeishuAuthSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ?", input.OldSessionID, input.UserID, input.Generation).
			Where("state IN ?", []string{model.FeishuAuthSessionPending, model.FeishuAuthSessionRejected, model.FeishuAuthSessionExpired, model.FeishuAuthSessionSuperseded}).
			Take(&old).Error; err != nil {
			return err
		}
		if old.OperationID == nil || *old.OperationID != input.OperationID || old.Phase != model.FeishuAuthPhaseUserAuth ||
			!equalRequestedScopes(old.RequestedScopesJSON, input.NewSession.RequestedScopesJSON) ||
			(old.ProtocolVersion == 2 && old.ScopeHash != input.NewSession.ScopeHash) {
			return gorm.ErrRecordNotFound
		}
		if !replaceableFeishuDeviceAuthSource(&old, input) {
			return gorm.ErrRecordNotFound
		}

		var operation model.FeishuOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ? AND state = ?", input.OperationID, input.UserID, input.Generation, input.ExpectedWaitingState).
			Take(&operation).Error; err != nil {
			return err
		}
		var currentBinding feishuOperationRecoveryBinding
		if err := json.Unmarshal(operation.ResultSummaryJSON, &currentBinding); err != nil || currentBinding != oldBinding ||
			!equivalentJSON(operation.ResultSummaryJSON, input.OldSummary) {
			return gorm.ErrRecordNotFound
		}

		if old.State == model.FeishuAuthSessionPending {
			update := tx.Model(&model.FeishuAuthSession{}).
				Where("id = ? AND user_id = ? AND generation = ?", old.ID, input.UserID, input.Generation).
				Where("state = ? AND protocol_version = ?", model.FeishuAuthSessionPending, old.ProtocolVersion)
			if expiredFeishuDeviceAuthPendingSource(&old, input) {
				update = update.Where("expires_at <= ?", input.Now)
				if old.LeaseOwner == "" {
					update = update.Where("lease_owner = ? AND lease_until IS NULL", "")
				} else {
					update = update.Where("lease_owner = ? AND lease_until = ? AND lease_until <= ?", old.LeaseOwner, old.LeaseUntil.UTC(), input.Now)
				}
			} else {
				update = update.Where("lease_owner = ? AND lease_until > ?", input.LeaseOwner, input.Now)
			}
			result := update.Updates(feishuTerminalAuthSessionUpdates(input.TerminalState, input.Now))
			if result.Error != nil {
				return fmt.Errorf("terminalize replaced feishu device auth session: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
		} else {
			result := tx.Model(&model.FeishuAuthSession{}).
				Where("id = ? AND user_id = ? AND generation = ? AND state = ?", old.ID, input.UserID, input.Generation, old.State).
				Updates(map[string]any{
					"resume_credential_ciphertext": nil, "resume_key_version": nil, "resume_expires_at": nil,
					"lease_owner": "", "lease_until": nil,
				})
			if result.Error != nil {
				return fmt.Errorf("clean replaced feishu auth session: %w", result.Error)
			}
		}

		candidate := *input.NewSession
		if err := tx.Create(&candidate).Error; err != nil {
			return fmt.Errorf("create replacement feishu device auth session: %w", err)
		}
		accountResult := tx.Model(&model.UserThirdPartyAccount{}).
			Where("user_id = ? AND provider = ? AND generation = ? AND connection_state <> ?", input.UserID, "lark", input.Generation, model.FeishuConnectionDisconnecting).
			Updates(map[string]any{"connection_state": model.FeishuConnectionWaitingUserAuth, "connected": false, "connected_at": nil, "updated_at": input.Now.UTC()})
		if accountResult.Error != nil {
			return fmt.Errorf("update replacement feishu device auth account: %w", accountResult.Error)
		}
		if accountResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		operationResult := tx.Model(&model.FeishuOperation{}).
			Where("id = ? AND user_id = ? AND generation = ? AND state = ?", input.OperationID, input.UserID, input.Generation, input.ExpectedWaitingState).
			Update("result_summary_json", append([]byte(nil), input.NewSummary...))
		if operationResult.Error != nil {
			return fmt.Errorf("rebind replacement feishu device auth operation: %w", operationResult.Error)
		}
		if operationResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if newBinding.SessionID != candidate.ID {
			return gorm.ErrRecordNotFound
		}
		stored = &candidate
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, fmt.Errorf("replace feishu device auth session: %w", err)
	}
	return stored, nil
}

// SweepDeviceAuthCredentials scans at most 100 primary-key rows and clears
// only terminal or expired credentials from that bounded page.
func (s *feishuWorkspaceStore) SweepDeviceAuthCredentials(
	ctx context.Context,
	before time.Time,
	afterSessionID string,
	scanLimit int,
) (FeishuDeviceAuthCleanupPage, error) {
	if scanLimit <= 0 || scanLimit > 100 {
		scanLimit = 100
	}
	page := FeishuDeviceAuthCleanupPage{Done: true}
	var sessions []model.FeishuAuthSession
	if err := s.db.WithContext(ctx).
		Select("id", "state", "protocol_version", "resume_credential_ciphertext", "resume_key_version", "resume_expires_at").
		Where("id > ?", afterSessionID).
		Order("id ASC").
		Limit(scanLimit).
		Find(&sessions).Error; err != nil {
		return page, fmt.Errorf("scan feishu device auth credential page: %w", err)
	}
	page.Scanned = len(sessions)
	page.Done = len(sessions) < scanLimit
	if len(sessions) == 0 {
		return page, nil
	}
	page.NextSessionID = sessions[len(sessions)-1].ID
	ids := make([]string, 0, len(sessions))
	for index := range sessions {
		session := &sessions[index]
		if session.ProtocolVersion != 2 || !feishuSessionHasResumeCredential(session) {
			continue
		}
		if validFeishuAuthTerminalState(session.State) || (session.ResumeExpiresAt != nil && !session.ResumeExpiresAt.After(before)) {
			ids = append(ids, session.ID)
		}
	}
	if len(ids) == 0 {
		return page, nil
	}
	result := s.db.WithContext(ctx).Model(&model.FeishuAuthSession{}).
		Where("id IN ? AND protocol_version = ?", ids, 2).
		Where(
			"state IN ? OR (resume_expires_at IS NOT NULL AND resume_expires_at <= ? AND (COALESCE(lease_owner, '') = '' OR lease_until IS NULL OR lease_until <= ?))",
			feishuAuthTerminalStates(), before, before,
		).
		Where("resume_credential_ciphertext IS NOT NULL OR resume_key_version IS NOT NULL OR resume_expires_at IS NOT NULL").
		Updates(map[string]any{
			"resume_credential_ciphertext": nil, "resume_key_version": nil, "resume_expires_at": nil,
			"lease_owner": "", "lease_until": nil,
		})
	if result.Error != nil {
		return page, fmt.Errorf("clear feishu device auth credential page: %w", result.Error)
	}
	page.Cleared = int(result.RowsAffected)
	return page, nil
}

func validFeishuResumeKeyVersion(value string) bool {
	trimmed := strings.TrimSpace(value)
	return value == trimmed && value != "" && len(value) <= 32
}

func normalizedFeishuAuthProtocolVersion(version uint8) uint8 {
	if version == 0 {
		return 1
	}
	return version
}

func validFeishuAuthSessionCreationShape(session *model.FeishuAuthSession, protocolVersion uint8) bool {
	if session == nil {
		return false
	}
	switch protocolVersion {
	case 1:
		return session.ScopeHash == "" && !feishuSessionHasResumeCredential(session)
	case 2:
		return session.Phase == model.FeishuAuthPhaseUserAuth && session.State == model.FeishuAuthSessionPending &&
			session.CompletedAt == nil && session.LeaseOwner == "" && session.LeaseUntil == nil &&
			validFeishuDeviceAuthPreStartShape(session)
	default:
		return false
	}
}

func validPersistedFeishuAuthSessionShape(session *model.FeishuAuthSession) bool {
	if session == nil {
		return false
	}
	switch normalizedFeishuAuthProtocolVersion(session.ProtocolVersion) {
	case 1:
		return session.ScopeHash == "" && !feishuSessionHasResumeCredential(session)
	case 2:
		if session.Phase != model.FeishuAuthPhaseUserAuth {
			return false
		}
		if validFeishuAuthTerminalState(session.State) {
			return session.LeaseOwner == "" && session.LeaseUntil == nil && validFeishuDeviceAuthPreStartShape(session)
		}
		return validFeishuDeviceAuthShape(session)
	default:
		return false
	}
}

func validFeishuScopeHash(value string) bool {
	return len(value) == 64 && strings.TrimSpace(value) == value
}

func validFeishuDeviceAuthPreStartShape(session *model.FeishuAuthSession) bool {
	return session != nil && session.ProtocolVersion == 2 && validFeishuScopeHash(session.ScopeHash) &&
		!feishuSessionHasResumeCredential(session)
}

func validFeishuDeviceAuthWaitingShape(session *model.FeishuAuthSession) bool {
	return session != nil && session.ProtocolVersion == 2 && validFeishuScopeHash(session.ScopeHash) &&
		len(session.ResumeCredentialCiphertext) > 0 && validFeishuResumeKeyVersion(session.ResumeKeyVersion) && session.ResumeExpiresAt != nil
}

func validFeishuDeviceAuthShape(session *model.FeishuAuthSession) bool {
	return validFeishuDeviceAuthPreStartShape(session) || validFeishuDeviceAuthWaitingShape(session)
}

func feishuSessionHasResumeCredential(session *model.FeishuAuthSession) bool {
	return session != nil && (len(session.ResumeCredentialCiphertext) > 0 || session.ResumeKeyVersion != "" || session.ResumeExpiresAt != nil)
}

func feishuAuthTerminalStates() []string {
	return []string{
		model.FeishuAuthSessionCompleted,
		model.FeishuAuthSessionExpired,
		model.FeishuAuthSessionRejected,
		model.FeishuAuthSessionFailed,
		model.FeishuAuthSessionSuperseded,
	}
}

func validFeishuAuthTerminalState(state string) bool {
	for _, candidate := range feishuAuthTerminalStates() {
		if state == candidate {
			return true
		}
	}
	return false
}

func validFeishuDeviceAuthFailureTerminalState(state string) bool {
	switch state {
	case model.FeishuAuthSessionExpired,
		model.FeishuAuthSessionRejected,
		model.FeishuAuthSessionFailed,
		model.FeishuAuthSessionSuperseded:
		return true
	default:
		return false
	}
}

func validFeishuDeviceAuthReplacementInput(input FeishuDeviceAuthReplacement) bool {
	newSession := input.NewSession
	return input.UserID != 0 && input.Generation != 0 && strings.TrimSpace(input.OldSessionID) != "" &&
		strings.TrimSpace(input.OperationID) != "" && input.ExpectedWaitingState == model.FeishuOperationWaitingUserAuth &&
		newSession != nil && newSession.ID != input.OldSessionID && newSession.UserID == input.UserID &&
		newSession.Generation == input.Generation && newSession.OperationID != nil && *newSession.OperationID == input.OperationID &&
		newSession.Phase == model.FeishuAuthPhaseUserAuth && newSession.State == model.FeishuAuthSessionPending &&
		newSession.LeaseOwner == "" && newSession.LeaseUntil == nil && newSession.CompletedAt == nil &&
		validFeishuDeviceAuthPreStartShape(newSession) && json.Valid(input.OldSummary) && json.Valid(input.NewSummary)
}

func validFeishuDeviceAuthReplacementBindings(input FeishuDeviceAuthReplacement) (feishuOperationRecoveryBinding, feishuOperationRecoveryBinding, bool) {
	var oldBinding feishuOperationRecoveryBinding
	var newBinding feishuOperationRecoveryBinding
	if json.Unmarshal(input.OldSummary, &oldBinding) != nil || json.Unmarshal(input.NewSummary, &newBinding) != nil {
		return oldBinding, newBinding, false
	}
	valid := oldBinding.Status == input.ExpectedWaitingState && oldBinding.SessionID == input.OldSessionID && oldBinding.Phase == model.FeishuAuthPhaseUserAuth &&
		newBinding.Status == input.ExpectedWaitingState && newBinding.SessionID == input.NewSession.ID && newBinding.Phase == model.FeishuAuthPhaseUserAuth
	return oldBinding, newBinding, valid
}

func replaceableFeishuDeviceAuthSource(old *model.FeishuAuthSession, input FeishuDeviceAuthReplacement) bool {
	if old == nil {
		return false
	}
	switch old.State {
	case model.FeishuAuthSessionPending:
		if expiredFeishuDeviceAuthPendingSource(old, input) {
			return true
		}
		owned := old.LeaseOwner == input.LeaseOwner && old.LeaseOwner != "" && old.LeaseUntil != nil && old.LeaseUntil.After(input.Now)
		if !owned {
			return false
		}
		if old.ProtocolVersion == 2 {
			return (input.TerminalState == model.FeishuAuthSessionRejected ||
				input.TerminalState == model.FeishuAuthSessionExpired ||
				input.TerminalState == model.FeishuAuthSessionSuperseded) && validFeishuDeviceAuthShape(old)
		}
		return old.ProtocolVersion == 1 && input.TerminalState == model.FeishuAuthSessionSuperseded && !feishuSessionHasResumeCredential(old)
	case model.FeishuAuthSessionRejected, model.FeishuAuthSessionExpired:
		return old.ProtocolVersion == 2 && input.TerminalState == old.State && validFeishuDeviceAuthPreStartShape(old) && old.LeaseOwner == "" && old.LeaseUntil == nil
	case model.FeishuAuthSessionSuperseded:
		return old.ProtocolVersion == 1 && input.TerminalState == model.FeishuAuthSessionSuperseded && !feishuSessionHasResumeCredential(old) && old.LeaseOwner == "" && old.LeaseUntil == nil
	default:
		return false
	}
}

func expiredFeishuDeviceAuthPendingSource(old *model.FeishuAuthSession, input FeishuDeviceAuthReplacement) bool {
	if old == nil || old.State != model.FeishuAuthSessionPending || old.ProtocolVersion != 2 ||
		old.Phase != model.FeishuAuthPhaseUserAuth || old.CompletedAt != nil || old.ExpiresAt.After(input.Now) ||
		input.TerminalState != model.FeishuAuthSessionExpired || !validFeishuDeviceAuthShape(old) {
		return false
	}
	leaseFree := old.LeaseOwner == "" && old.LeaseUntil == nil
	expiredCompleteLease := old.LeaseOwner != "" && old.LeaseUntil != nil && !old.LeaseUntil.After(input.Now)
	return leaseFree || expiredCompleteLease
}

func feishuTerminalAuthSessionUpdates(state string, now time.Time) map[string]any {
	return map[string]any{
		"state": state, "completed_at": now.UTC(),
		"resume_credential_ciphertext": nil, "resume_key_version": nil, "resume_expires_at": nil,
		"lease_owner": "", "lease_until": nil,
	}
}

func equivalentJSON(left, right []byte) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
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
				"state":                        state,
				"completed_at":                 completedAt,
				"resume_credential_ciphertext": nil,
				"resume_key_version":           nil,
				"resume_expires_at":            nil,
				"lease_owner":                  "",
				"lease_until":                  nil,
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
				"state":                        model.FeishuAuthSessionCompleted,
				"completed_at":                 now,
				"resume_credential_ciphertext": nil,
				"resume_key_version":           nil,
				"resume_expires_at":            nil,
				"lease_owner":                  "",
				"lease_until":                  nil,
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

func validFeishuDeviceAuthSuccessInput(input FeishuDeviceAuthSuccess) bool {
	if input.UserID == 0 || input.Generation == 0 || strings.TrimSpace(input.SessionID) == "" ||
		strings.TrimSpace(input.LeaseOwner) == "" || input.Now.IsZero() ||
		!validFeishuAppIDEvidence(input.ExpectedAppID) || input.Evidence.AppID != input.ExpectedAppID ||
		!validFeishuCLIVersionEvidence(input.Evidence.CLIVersion) {
		return false
	}
	if input.OperationID == "" {
		if input.ExpectedWaitingState != "" {
			return false
		}
	} else if strings.TrimSpace(input.OperationID) != input.OperationID ||
		input.ExpectedWaitingState != model.FeishuOperationWaitingUserAuth {
		return false
	}
	if input.ExpectedVaultRevision == ^uint64(0) || input.Candidate.UserID != input.UserID ||
		input.Candidate.Generation != input.Generation || input.Candidate.Revision != input.ExpectedVaultRevision+1 ||
		len(input.Candidate.Ciphertext) == 0 || len(input.Candidate.Ciphertext) > maxFeishuCLIVaultCiphertextBytes ||
		!validFeishuResumeKeyVersion(input.Candidate.KeyVersion) {
		return false
	}
	wantChecksum := fmt.Sprintf("%x", sha256.Sum256(input.Candidate.Ciphertext))
	return len(input.Candidate.Checksum) == len(wantChecksum) &&
		subtle.ConstantTimeCompare([]byte(input.Candidate.Checksum), []byte(wantChecksum)) == 1
}

// FinalizeDeviceAuthSuccess publishes the candidate vault and completes its
// authorization state atomically. Its fixed lock order is account, session,
// optional operation, then vault; every conditional miss rolls back all writes.
func (s *feishuWorkspaceStore) FinalizeDeviceAuthSuccess(ctx context.Context, input FeishuDeviceAuthSuccess) error {
	if !validFeishuDeviceAuthSuccessInput(input) {
		return gorm.ErrRecordNotFound
	}
	now := input.Now.UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account model.UserThirdPartyAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND provider = ?", input.UserID, "lark").
			Take(&account).Error; err != nil {
			return err
		}
		if !feishuAccountGenerationActive(&account, input.Generation) || account.AppID != input.ExpectedAppID {
			return gorm.ErrRecordNotFound
		}

		var session model.FeishuAuthSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND generation = ?", input.SessionID, input.UserID, input.Generation).
			Where("state = ? AND protocol_version = ? AND phase = ?", model.FeishuAuthSessionPending, 2, model.FeishuAuthPhaseUserAuth).
			Where("lease_owner = ? AND lease_until > ?", input.LeaseOwner, now).
			Take(&session).Error; err != nil {
			return err
		}
		if !validFeishuDeviceAuthWaitingShape(&session) || !session.ExpiresAt.After(now) ||
			session.ResumeExpiresAt == nil || !session.ResumeExpiresAt.After(now) {
			return gorm.ErrRecordNotFound
		}
		if input.OperationID == "" {
			if session.OperationID != nil {
				return gorm.ErrRecordNotFound
			}
		} else if session.OperationID == nil || *session.OperationID != input.OperationID {
			return gorm.ErrRecordNotFound
		}

		if input.OperationID != "" {
			var operation model.FeishuOperation
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND user_id = ? AND generation = ? AND state = ?", input.OperationID, input.UserID, input.Generation, input.ExpectedWaitingState).
				Take(&operation).Error; err != nil {
				return err
			}
			var binding feishuOperationRecoveryBinding
			if json.Unmarshal(operation.ResultSummaryJSON, &binding) != nil || binding.Status != input.ExpectedWaitingState ||
				binding.SessionID != input.SessionID || binding.Phase != model.FeishuAuthPhaseUserAuth {
				return gorm.ErrRecordNotFound
			}
		}

		var currentVault model.FeishuCLIVault
		vaultErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", input.UserID).
			Take(&currentVault).Error
		if input.ExpectedVaultRevision == 0 {
			if vaultErr == nil {
				return gorm.ErrRecordNotFound
			}
			if !errors.Is(vaultErr, gorm.ErrRecordNotFound) {
				return vaultErr
			}
			candidate := input.Candidate
			candidate.CreatedAt = time.Time{}
			candidate.UpdatedAt = time.Time{}
			if err := tx.Create(&candidate).Error; err != nil {
				return fmt.Errorf("create finalized feishu CLI vault: %w", err)
			}
		} else {
			if vaultErr != nil {
				return vaultErr
			}
			if currentVault.Generation != input.Generation || currentVault.Revision != input.ExpectedVaultRevision {
				return gorm.ErrRecordNotFound
			}
			vaultResult := tx.Model(&model.FeishuCLIVault{}).
				Where("user_id = ? AND generation = ? AND revision = ?", input.UserID, input.Generation, input.ExpectedVaultRevision).
				Updates(map[string]any{
					"ciphertext": input.Candidate.Ciphertext, "key_version": input.Candidate.KeyVersion,
					"checksum": input.Candidate.Checksum, "revision": input.Candidate.Revision,
				})
			if vaultResult.Error != nil {
				return fmt.Errorf("publish finalized feishu CLI vault: %w", vaultResult.Error)
			}
			if vaultResult.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
		}

		accountResult := tx.Model(&model.UserThirdPartyAccount{}).
			Where("user_id = ? AND provider = ? AND generation = ? AND app_id = ?", input.UserID, "lark", input.Generation, input.ExpectedAppID).
			Where("connection_state <> ?", model.FeishuConnectionDisconnecting).
			Updates(map[string]any{
				"connected": true, "connected_at": now, "connection_state": model.FeishuConnectionConnected,
				"app_id": input.Evidence.AppID, "lark_cli_version": input.Evidence.CLIVersion,
			})
		if accountResult.Error != nil {
			return fmt.Errorf("finalize device authorization account: %w", accountResult.Error)
		}
		if accountResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		sessionResult := tx.Model(&model.FeishuAuthSession{}).
			Where("id = ? AND user_id = ? AND generation = ?", input.SessionID, input.UserID, input.Generation).
			Where("state = ? AND protocol_version = ? AND phase = ?", model.FeishuAuthSessionPending, 2, model.FeishuAuthPhaseUserAuth).
			Where("lease_owner = ? AND lease_until > ?", input.LeaseOwner, now).
			Updates(map[string]any{
				"state": model.FeishuAuthSessionCompleted, "completed_at": now,
				"resume_credential_ciphertext": nil, "resume_key_version": nil, "resume_expires_at": nil,
				"lease_owner": "", "lease_until": nil,
			})
		if sessionResult.Error != nil {
			return fmt.Errorf("finalize device authorization session: %w", sessionResult.Error)
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
// Base, Wiki, and Drive commands from dropping each other's last-known state.
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
	clean := make(map[string]feishuStoredCapabilityState, 4)
	for _, domain := range []string{"docs", "base", "wiki", "drive"} {
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
	return domain == "docs" || domain == "base" || domain == "wiki" || domain == "drive"
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

// CreateOrGetConnectionOperation serializes the connection bootstrap through
// the account row. Exact retries remain idempotent; a different Agent call or
// an in-progress Settings flow is rejected before any operation/session worker
// can be created.
func (s *feishuWorkspaceStore) CreateOrGetConnectionOperation(ctx context.Context, operation *model.FeishuOperation) (*model.FeishuOperation, error) {
	if operation == nil || operation.CommandPath != "workspace connect" {
		return nil, ErrFeishuConnectionOperationInProgress
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
			return gorm.ErrRecordNotFound
		}

		var exact model.FeishuOperation
		exactErr := tx.Where("user_id = ? AND idempotency_key = ?", operation.UserID, operation.IdempotencyKey).
			Take(&exact).Error
		if exactErr == nil {
			stored = &exact
			return nil
		}
		if !errors.Is(exactErr, gorm.ErrRecordNotFound) {
			return exactErr
		}

		var activeConnections []model.FeishuOperation
		activeErr := tx.Where(
			"user_id = ? AND generation = ? AND command_path = ? AND state IN ?",
			operation.UserID, operation.Generation, "workspace connect", feishuConnectionOperationActiveStates(),
		).Order("created_at ASC, id ASC").Find(&activeConnections).Error
		if activeErr != nil {
			return activeErr
		}
		for index := range activeConnections {
			blocks, fenceErr := feishuConnectionOperationBlocks(tx, &activeConnections[index], time.Now().UTC())
			if fenceErr != nil {
				return fenceErr
			}
			if blocks {
				return ErrFeishuConnectionOperationInProgress
			}
		}
		var pendingSession model.FeishuAuthSession
		pendingErr := tx.Where(
			"user_id = ? AND generation = ? AND state = ?",
			operation.UserID, operation.Generation, model.FeishuAuthSessionPending,
		).Order("created_at ASC, id ASC").Take(&pendingSession).Error
		if pendingErr == nil {
			return ErrFeishuConnectionOperationInProgress
		}
		if !errors.Is(pendingErr, gorm.ErrRecordNotFound) {
			return pendingErr
		}
		if err := tx.Create(operation).Error; err != nil {
			return err
		}
		stored = operation
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrFeishuConnectionOperationInProgress) {
			return nil, ErrFeishuConnectionOperationInProgress
		}
		return nil, fmt.Errorf("create or get feishu connection operation: %w", err)
	}
	return stored, nil
}

func feishuConnectionOperationActiveStates() []string {
	return []string{
		model.FeishuOperationNotStarted,
		model.FeishuOperationExecuting,
		model.FeishuOperationWaitingConnection,
		model.FeishuOperationWaitingAppScope,
		model.FeishuOperationWaitingUserAuth,
		model.FeishuOperationWaitingConfirmation,
	}
}

func containsFeishuOperationState(states []string, candidate string) bool {
	for _, state := range states {
		if state == candidate {
			return true
		}
	}
	return false
}

// feishuConnectionOperationBlocks protects the normal operation→session gap,
// but atomically retires a crash orphan after a bounded window. A pending
// session always wins, and a concurrent claim/update wins through the final
// conditional UPDATE.
func feishuConnectionOperationBlocks(tx *gorm.DB, operation *model.FeishuOperation, now time.Time) (bool, error) {
	if tx == nil || operation == nil {
		return true, gorm.ErrInvalidData
	}
	stale := false
	switch operation.State {
	case model.FeishuOperationNotStarted:
		stale = !operation.CreatedAt.IsZero() && !operation.CreatedAt.After(now.Add(-feishuConnectionBootstrapStaleAfter))
	case model.FeishuOperationExecuting:
		stale = operation.LeaseUntil == nil || !operation.LeaseUntil.After(now)
	case model.FeishuOperationWaitingConnection,
		model.FeishuOperationWaitingAppScope,
		model.FeishuOperationWaitingUserAuth,
		model.FeishuOperationWaitingConfirmation:
		anchor := operation.UpdatedAt
		if anchor.IsZero() {
			anchor = operation.CreatedAt
		}
		stale = !anchor.IsZero() && !anchor.After(now.Add(-feishuConnectionBootstrapStaleAfter))
	default:
		return true, nil
	}
	if !stale {
		return true, nil
	}
	var boundSessions []model.FeishuAuthSession
	if err := tx.Where("user_id = ? AND generation = ? AND operation_id = ? AND state IN ?",
		operation.UserID, operation.Generation, operation.ID,
		[]string{model.FeishuAuthSessionPending, model.FeishuAuthSessionCompleted}).
		Order("updated_at DESC, id DESC").Find(&boundSessions).Error; err != nil {
		return true, err
	}
	for index := range boundSessions {
		session := &boundSessions[index]
		if session.State == model.FeishuAuthSessionPending {
			return true, nil
		}
		anchor := session.UpdatedAt
		if session.CompletedAt != nil {
			anchor = session.CompletedAt.UTC()
		}
		if !anchor.IsZero() && anchor.After(now.Add(-feishuConnectionBootstrapStaleAfter)) {
			return true, nil
		}
	}
	query := tx.Model(&model.FeishuOperation{}).
		Where("id = ? AND user_id = ? AND generation = ? AND state = ?",
			operation.ID, operation.UserID, operation.Generation, operation.State)
	if operation.State == model.FeishuOperationNotStarted {
		query = query.Where("created_at <= ?", now.Add(-feishuConnectionBootstrapStaleAfter))
	} else if operation.State == model.FeishuOperationExecuting {
		query = query.Where("lease_until IS NULL OR lease_until <= ?", now)
	} else {
		query = query.Where("updated_at <= ?", now.Add(-feishuConnectionBootstrapStaleAfter))
	}
	result := query.Updates(map[string]any{
		"state": model.FeishuOperationCancelled, "finished_at": now,
		"lease_owner": "", "lease_until": nil,
	})
	if result.Error != nil {
		return true, result.Error
	}
	return result.RowsAffected != 1, nil
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

// ListSucceededCreatesForRun returns a small, deterministic create-candidate
// set. Callers must still authenticate and inspect each encrypted request and
// result; this query alone never proves equivalence or overwrite safety.
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

// ListSucceededBaseCreatesForRun is separate from overwrite-proof candidates so
// a run with many Base operations cannot evict document-create proofs (or vice
// versa) from either bounded result set.
func (s *feishuWorkspaceStore) ListSucceededBaseCreatesForRun(
	ctx context.Context,
	userID uint,
	generation uint64,
	agentRunID uint64,
) ([]model.FeishuOperation, error) {
	var operations []model.FeishuOperation
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND generation = ? AND agent_run_id = ?", userID, generation, agentRunID).
		Where("state = ? AND command_path = ?", model.FeishuOperationSucceeded, "base +base-create").
		Order("created_at DESC").
		Order("id DESC").
		Limit(feishuSucceededCreateProofLimit).
		Find(&operations).Error; err != nil {
		return nil, fmt.Errorf("list succeeded feishu base create operations: %w", err)
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
// binding. The source is normally pending; the same transaction also admits an
// exactly bound failed source and a legacy superseded source.
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
			Where("state IN ?", []string{
				model.FeishuAuthSessionPending,
				model.FeishuAuthSessionRejected,
				model.FeishuAuthSessionExpired,
				model.FeishuAuthSessionFailed,
				model.FeishuAuthSessionSuperseded,
			}).
			Take(&oldSession).Error; err != nil {
			return err
		}
		if oldSession.OperationID == nil || *oldSession.OperationID != operationID || oldSession.Phase != replacement.Phase ||
			!equalRequestedScopes(oldSession.RequestedScopesJSON, replacement.RequestedScopesJSON) {
			return gorm.ErrRecordNotFound
		}
		if oldSession.Phase == model.FeishuAuthPhaseUserAuth && !validFeishuAuthSessionCreationShape(replacement, 2) {
			return gorm.ErrRecordNotFound
		}
		if oldSession.ProtocolVersion == 2 && replacement.ScopeHash != oldSession.ScopeHash {
			return gorm.ErrRecordNotFound
		}
		if (oldSession.State == model.FeishuAuthSessionRejected || oldSession.State == model.FeishuAuthSessionExpired) && oldSession.ProtocolVersion != 2 {
			return gorm.ErrRecordNotFound
		}
		if oldSession.ProtocolVersion == 2 && oldSession.LeaseOwner != "" && oldSession.LeaseUntil != nil && oldSession.LeaseUntil.After(now) {
			// This legacy API carries no lease owner. It must never replace a live
			// protocol-v2 worker; ReplaceDeviceAuthSession is the owned primitive.
			return gorm.ErrRecordNotFound
		}
		if oldSession.State == model.FeishuAuthSessionSuperseded && oldSession.ProtocolVersion != 1 {
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
		if oldSession.State != model.FeishuAuthSessionPending {
			// A failed current session or pre-atomic legacy source can have a
			// second pending session without the operation summary naming it.
			// Fence every matching orphan: a live lease wins, while an
			// unleased/expired one is retired in this transaction.
			var orphans []model.FeishuAuthSession
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("user_id = ? AND generation = ? AND operation_id = ? AND phase = ? AND state = ?", userID, generation, operationID, replacement.Phase, model.FeishuAuthSessionPending).
				Where("id <> ?", oldSessionID).
				Find(&orphans).Error; err != nil {
				return err
			}
			for index := range orphans {
				orphan := &orphans[index]
				if orphan.LeaseUntil != nil && orphan.LeaseUntil.After(now) {
					return gorm.ErrRecordNotFound
				}
				if !equalRequestedScopes(orphan.RequestedScopesJSON, replacement.RequestedScopesJSON) {
					continue
				}
				result := tx.Model(&model.FeishuAuthSession{}).
					Where("id = ? AND user_id = ? AND generation = ? AND state = ?", orphan.ID, userID, generation, model.FeishuAuthSessionPending).
					Updates(map[string]any{
						"state": model.FeishuAuthSessionSuperseded, "completed_at": now.UTC(),
						"resume_credential_ciphertext": nil, "resume_key_version": nil, "resume_expires_at": nil,
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
			Where("state IN ?", []string{
				model.FeishuAuthSessionPending,
				model.FeishuAuthSessionRejected,
				model.FeishuAuthSessionExpired,
				model.FeishuAuthSessionFailed,
				model.FeishuAuthSessionSuperseded,
			}).
			Updates(map[string]any{
				"state": model.FeishuAuthSessionSuperseded, "completed_at": now.UTC(),
				"resume_credential_ciphertext": nil, "resume_key_version": nil, "resume_expires_at": nil,
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
// User-auth replacements are intentionally irreversible and are never restored.
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
	if oldBinding.Phase == model.FeishuAuthPhaseUserAuth {
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
				"resume_credential_ciphertext": nil, "resume_key_version": nil, "resume_expires_at": nil,
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
				"state":                        model.FeishuAuthSessionSuperseded,
				"completed_at":                 now,
				"resume_credential_ciphertext": nil,
				"resume_key_version":           nil,
				"resume_expires_at":            nil,
				"lease_owner":                  "",
				"lease_until":                  nil,
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
