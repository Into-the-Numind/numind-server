package feishu

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	pkgcrypto "numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/model"
)

const (
	operationMaxToolCallIDBytes   = 128
	operationMaxIdempotencyBytes  = 191
	operationMaxOperationIDBytes  = 64
	operationDefaultLeaseDuration = 90 * time.Second
	operationFinalizeTimeout      = 5 * time.Second
	operationSessionLineageLimit  = 32
	// Each execution-gate lease window is renewed by the heartbeat and again
	// immediately before every runner invocation. Expiry is the crash fallback.
	operationExecutionGateLeaseDuration     = 120 * time.Second
	operationExecutionGateWaitTimeout       = 125 * time.Second
	operationExecutionGatePollInterval      = 100 * time.Millisecond
	operationExecutionGateHeartbeatInterval = 30 * time.Second
	operationExecutionGateOverhead          = 30 * time.Second
	connectionOnlyCommandPath               = "workspace connect"
	connectionOnlyDomain                    = "workspace"
	connectionOnlyAction                    = "connect"
)

var (
	// ErrOperationRequestRejected collapses malformed, denied, and receipt-invalid
	// model input without echoing any supplied value.
	ErrOperationRequestRejected = errors.New("feishu operation request rejected")
	// ErrOperationIdempotencyConflict means a user reused a key for different
	// immutable operation metadata or request content.
	ErrOperationIdempotencyConflict = errors.New("feishu operation idempotency conflict")
	// ErrOperationIntegrity means authenticated persisted operation data could
	// not be safely opened or matched to its row metadata.
	ErrOperationIntegrity = errors.New("feishu operation integrity check failed")
	// ErrOperationUnavailable is a safe dependency/persistence failure. Raw CLI
	// errors and stderr are never wrapped into this sentinel.
	ErrOperationUnavailable = errors.New("feishu operation unavailable")
	// ErrOperationConnectionInProgress prevents multiple Agent/Settings entries
	// from creating parallel personal-app authorization workers.
	ErrOperationConnectionInProgress = errors.New("feishu connection already in progress")
)

// operationRequestValidation carries only a catalog-produced, credential-free
// correction hint. Error deliberately remains the generic rejection sentinel;
// callers must opt in through SafeOperationRequestValidation instead of
// accidentally logging or returning the hint from an ordinary error path.
type operationRequestValidation struct {
	hint string
}

func (e *operationRequestValidation) Error() string { return ErrOperationRequestRejected.Error() }
func (e *operationRequestValidation) Unwrap() error { return ErrOperationRequestRejected }

// SafeOperationRequestValidation returns a bounded catalog validation hint.
// Catalog invalid-argument messages contain only reviewed labels and flag
// names, never argv values, tokens, URLs, paths, provider output, or secrets.
func SafeOperationRequestValidation(err error) (string, bool) {
	var validation *operationRequestValidation
	if !errors.As(err, &validation) || validation == nil || validation.hint == "" || len(validation.hint) > 256 {
		return "", false
	}
	return validation.hint, true
}

func newOperationRequestValidation(argv []string, err error) error {
	hint, ok := SafeCommandValidationHint(argv, err)
	if !ok {
		return ErrOperationRequestRejected
	}
	return &operationRequestValidation{hint: hint}
}

// ReceiptVerifier is retained only as a source-compatible dependency type for
// older composition roots. OperationService no longer calls it: model-carried
// skill receipts are not an execution authorization primitive.
type ReceiptVerifier interface {
	VerifyRequired(receipts []string, runID uint64, domain string) error
}

// RecoveryStarter starts or inspects one deterministic authorization recovery.
// A non-nil action means recovery is still waiting; nil means it completed.
type RecoveryStarter interface {
	StartRecovery(ctx context.Context, req RecoveryRequest) (*OperationAction, error)
	Activate(ctx context.Context, sessionID string) error
	Abort(sessionID string)
}

// ConfirmationRequester publishes a high-risk confirmation action. A successful
// action must include a durable, non-empty SessionID and a future ExpiresAt so it
// can survive process restart; OperationService validates the server-normalized
// action before it owns the lease-fenced waiting transition.
type ConfirmationRequester interface {
	RequestConfirmation(ctx context.Context, operationID string, summary ConfirmationSummary) (*OperationAction, error)
}

// OperationAccountStore is the account subset required by operation fencing.
type OperationAccountStore interface {
	Get(ctx context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error)
	EnsurePlaceholder(ctx context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error)
}

// OperationStore is the encrypted operation persistence subset.
type OperationStore interface {
	CreateOrGetOperation(ctx context.Context, operation *model.FeishuOperation) (*model.FeishuOperation, error)
	CreateOrGetOperationWithProof(ctx context.Context, operation *model.FeishuOperation, sourceOperationID string) (*model.FeishuOperation, error)
	TryClaimExecutionGate(ctx context.Context, userID uint, generation uint64, owner, operationID string, now, leaseUntil time.Time) (bool, error)
	RenewExecutionGate(ctx context.Context, userID uint, generation uint64, owner, operationID string, now, leaseUntil time.Time) (bool, error)
	ReleaseExecutionGate(ctx context.Context, userID uint, generation uint64, owner string, now time.Time) (bool, error)
	RetiredExecutionGateDrained(ctx context.Context, userID uint, retiredGeneration uint64, now time.Time) (bool, error)
	IsOperationProofUsable(ctx context.Context, userID uint, generation uint64, agentRunID uint64, sourceOperationID, consumerOperationID string) (bool, error)
	ListSucceededCreatesForRun(ctx context.Context, userID uint, generation uint64, agentRunID uint64) ([]model.FeishuOperation, error)
	ListSucceededBaseCreatesForRun(ctx context.Context, userID uint, generation uint64, agentRunID uint64) ([]model.FeishuOperation, error)
	GetOperationForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuOperation, error)
	ClaimOperation(ctx context.Context, userID uint, generation uint64, id, owner string, expectedStates []string, now, leaseUntil time.Time) (bool, error)
	TransitionOperation(ctx context.Context, userID uint, generation uint64, id, owner string, from []string, to string, now time.Time, fields map[string]any) error
	TransitionOperationWithCapabilityOutcome(ctx context.Context, userID uint, generation uint64, id, owner string, from []string, to string, now time.Time, fields map[string]any, outcome model.FeishuCapabilityOutcome) error
}

// OperationHomeVault materializes and generation-fences one temporary CLI HOME.
type OperationHomeVault interface {
	WithHome(ctx context.Context, userID uint, generation uint64, callback func(home string) (changed bool, err error)) error
}

// OperationRunner executes only already-normalized argv inside a vault HOME.
type OperationRunner interface {
	Run(ctx context.Context, home string, argv []string, stdinJSON []byte) (*CLIResult, error)
}

// RecoveryRequest contains only server-owned operation metadata and exact
// Catalog scopes. It never contains a verification URL.
type RecoveryRequest struct {
	UserID      uint
	Generation  uint64
	OperationID string
	AgentRunID  uint64
	ToolCallID  string
	Kind        RecoveryKind
	Scopes      []string
	// ConsoleURL is transient evidence from the current, classifier-approved
	// app-scope error. It must never be copied into operation/session persistence.
	ConsoleURL string
}

// ConfirmationSummary is the non-sensitive, server-owned high-risk metadata
// shown to a confirmation adapter. Raw argv and document content are excluded.
type ConfirmationSummary struct {
	CommandPath    string
	Domain         string
	Action         string
	Risk           RiskLevel
	RequiresCLIYes bool
}

// OperationAction is a transient external action. URL may be returned live but
// is deliberately never stored in FeishuOperation.ResultSummaryJSON.
type OperationAction struct {
	Provider    string    `json:"provider"`
	OperationID string    `json:"operation_id"`
	SessionID   string    `json:"session_id,omitempty"`
	Phase       string    `json:"phase"`
	URL         string    `json:"url,omitempty"`
	Scopes      []string  `json:"scopes,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

// ExecuteRequest contains server-context IDs and raw allowlisted business argv.
// Risk, scopes, domain, normalized argv, identity, and authorization are derived.
type ExecuteRequest struct {
	UserID         uint
	AgentRunID     uint64
	ToolCallID     string
	IdempotencyKey string
	Argv           []string
	StdinJSON      json.RawMessage
	// SkillReceipts is retained only so old in-process callers compile during the
	// rolling transition. Execute deliberately ignores it; it is not security input.
	SkillReceipts []string
}

// ConnectOperationRequest starts the server-owned connection lifecycle without
// inventing a Docs/Base/Wiki/Drive business command. Every field is derived
// from the trusted Agent execution context.
type ConnectOperationRequest struct {
	UserID         uint
	AgentRunID     uint64
	ToolCallID     string
	IdempotencyKey string
}

// OperationResult is a defensive, non-sensitive view of a persisted operation.
type OperationResult struct {
	OperationID string                  `json:"operation_id"`
	State       string                  `json:"state"`
	Data        json.RawMessage         `json:"data,omitempty"`
	Failure     *OperationFailure       `json:"failure,omitempty"`
	Action      *OperationAction        `json:"action,omitempty"`
	NoticeCode  AuthorizationNoticeCode `json:"notice_code,omitempty"`
	AgentRunID  uint64                  `json:"-"`
	ToolCallID  string                  `json:"-"`
	// SupersededSessionIDs is an internal handoff fence. It is never returned
	// to browsers and contains identifiers only, never authorization material.
	SupersededSessionIDs []string `json:"-"`
}

// OperationCipherPurpose separates request and result ciphertext domains.
type OperationCipherPurpose string

const (
	OperationCipherPurposeRequest OperationCipherPurpose = "request"
	OperationCipherPurposeResult  OperationCipherPurpose = "result"
)

// OperationCipherOwner binds ciphertext to one user generation and operation.
type OperationCipherOwner struct {
	UserID      uint
	Generation  uint64
	OperationID string
}

// OperationCipherKeyring reads historical key versions and always writes with
// the configured current version. The input map is frozen at construction.
type OperationCipherKeyring struct {
	ciphers        map[string]*pkgcrypto.Cipher
	currentVersion string
	currentCipher  *pkgcrypto.Cipher
}

// NewOperationCipherKeyring constructs the AES-GCM operation payload keyring.
func NewOperationCipherKeyring(ciphers map[string]*pkgcrypto.Cipher, currentVersion string) (*OperationCipherKeyring, error) {
	if len(ciphers) == 0 || validateCLIHomeKeyVersion(currentVersion) != nil {
		return nil, errors.New("feishu operation cipher keyring rejected")
	}
	frozen := make(map[string]*pkgcrypto.Cipher, len(ciphers))
	for version, cipher := range ciphers {
		if validateCLIHomeKeyVersion(version) != nil || cipher == nil {
			return nil, errors.New("feishu operation cipher keyring rejected")
		}
		frozen[version] = cipher
	}
	current, ok := frozen[currentVersion]
	if !ok {
		return nil, errors.New("feishu operation current cipher unavailable")
	}
	return &OperationCipherKeyring{ciphers: frozen, currentVersion: currentVersion, currentCipher: current}, nil
}

// Seal encrypts plaintext using the current key and ownership-bound AAD.
func (k *OperationCipherKeyring) Seal(purpose OperationCipherPurpose, owner OperationCipherOwner, plaintext []byte) ([]byte, string, error) {
	if k == nil || k.currentCipher == nil || !validOperationCipherContext(purpose, owner) {
		return nil, "", ErrOperationIntegrity
	}
	ciphertext, err := k.currentCipher.EncryptWithAAD(plaintext, operationCipherAAD(purpose, owner, k.currentVersion))
	if err != nil {
		return nil, "", ErrOperationIntegrity
	}
	return ciphertext, k.currentVersion, nil
}

// Open decrypts ciphertext only for the exact purpose, owner, generation,
// operation ID, and historical key version.
func (k *OperationCipherKeyring) Open(purpose OperationCipherPurpose, owner OperationCipherOwner, keyVersion string, ciphertext []byte) ([]byte, error) {
	if k == nil || !validOperationCipherContext(purpose, owner) || validateCLIHomeKeyVersion(keyVersion) != nil {
		return nil, ErrOperationIntegrity
	}
	cipher, ok := k.ciphers[keyVersion]
	if !ok {
		return nil, ErrOperationIntegrity
	}
	plaintext, err := cipher.DecryptWithAAD(ciphertext, operationCipherAAD(purpose, owner, keyVersion))
	if err != nil {
		return nil, ErrOperationIntegrity
	}
	return plaintext, nil
}

type operationCipherAADPayload struct {
	Protocol   string                 `json:"protocol"`
	Purpose    OperationCipherPurpose `json:"purpose"`
	UserID     uint                   `json:"user_id"`
	Generation uint64                 `json:"generation"`
	Operation  string                 `json:"operation_id"`
	KeyVersion string                 `json:"key_version"`
}

func operationCipherAAD(purpose OperationCipherPurpose, owner OperationCipherOwner, keyVersion string) []byte {
	encoded, _ := json.Marshal(operationCipherAADPayload{
		Protocol: "feishu-operation-v1", Purpose: purpose, UserID: owner.UserID,
		Generation: owner.Generation, Operation: owner.OperationID, KeyVersion: keyVersion,
	})
	return encoded
}

func validOperationCipherContext(purpose OperationCipherPurpose, owner OperationCipherOwner) bool {
	return (purpose == OperationCipherPurposeRequest || purpose == OperationCipherPurposeResult) &&
		owner.UserID != 0 && owner.Generation != 0 && validStableIdentifier(owner.OperationID, operationMaxOperationIDBytes)
}

type operationSealedBlob struct {
	KeyVersion string `json:"key_version"`
	Ciphertext []byte `json:"ciphertext"`
}

type persistedOperationRequest struct {
	AgentRunID              uint64          `json:"agent_run_id"`
	ToolCallID              string          `json:"tool_call_id"`
	IdempotencyKey          string          `json:"idempotency_key"`
	CommandPath             string          `json:"command_path"`
	Domain                  string          `json:"domain"`
	Action                  string          `json:"action"`
	Risk                    RiskLevel       `json:"risk"`
	LocalOnly               bool            `json:"local_only,omitempty"`
	RequiresCLIYes          bool            `json:"requires_cli_yes"`
	ReplaySafeOnAuthError   bool            `json:"replay_safe_on_auth_error"`
	Scopes                  []string        `json:"scopes"`
	Argv                    []string        `json:"argv"`
	StdinJSON               json.RawMessage `json:"stdin_json,omitempty"`
	SameRunEmptyCreateProof bool            `json:"same_run_empty_create_proof,omitempty"`
	CreateProofOperationID  string          `json:"create_proof_operation_id,omitempty"`
	ConnectionOnly          bool            `json:"connection_only,omitempty"`
}

type persistedOperationSummary struct {
	Status               string       `json:"status"`
	PublicCode           string       `json:"public_code,omitempty"`
	Phase                string       `json:"phase,omitempty"`
	SessionID            string       `json:"session_id,omitempty"`
	ExpiresAt            *time.Time   `json:"expires_at,omitempty"`
	RecoveryKind         RecoveryKind `json:"recovery_kind,omitempty"`
	RecoveryScopes       []string     `json:"recovery_scopes,omitempty"`
	RecoverySignature    string       `json:"recovery_signature,omitempty"`
	BusinessStarted      bool         `json:"business_started,omitempty"`
	SupersededSessionIDs []string     `json:"superseded_session_ids,omitempty"`
}

// OperationServiceDeps wires only small one-way Feishu interfaces.
type OperationServiceDeps struct {
	Accounts   OperationAccountStore
	Operations OperationStore
	Catalog    *CommandCatalog
	// Deprecated: accepted for rolling source compatibility but never consulted.
	Receipts                       ReceiptVerifier
	Recovery                       RecoveryStarter
	Confirmation                   ConfirmationRequester
	Vault                          OperationHomeVault
	Preflight                      ScopePreflight
	Runner                         OperationRunner
	Cipher                         *OperationCipherKeyring
	Now                            func() time.Time
	LeaseDuration                  time.Duration
	ExecutionGateHeartbeatInterval time.Duration
	Observer                       OperationObserver
	// VerifiedCLIVersion is set by the one composition root only after the
	// controlled --version probe succeeded. Empty preserves metadata omission
	// for isolated tests; any other release is rejected.
	VerifiedCLIVersion string
}

// FeishuOperationService executes idempotent, encrypted personal-workspace
// commands without importing the Agent package.
type FeishuOperationService struct {
	accounts                       OperationAccountStore
	operations                     OperationStore
	catalog                        *CommandCatalog
	recovery                       RecoveryStarter
	confirmation                   ConfirmationRequester
	vault                          OperationHomeVault
	preflight                      ScopePreflight
	runner                         OperationRunner
	cipher                         *OperationCipherKeyring
	classifier                     *ErrorClassifier
	now                            func() time.Time
	leaseDuration                  time.Duration
	executionGateHeartbeatInterval time.Duration
	verifiedCLIVersion             string
	observer                       OperationObserver
	executions                     *executionRegistry
}

// OperationObservation is the credential-free execution evidence that may
// cross into production logs. It cannot carry argv, scopes, HOME paths,
// provider output, URLs, tokens, or user content.
type OperationObservation struct {
	UserID            uint
	Generation        uint64
	OperationID       string
	Phase             string
	OutcomeClass      string
	Risk              RiskLevel
	InvocationStarted bool
	ExitCode          int
	CLIVersion        string
	Duration          time.Duration
	CLIErrorType      string
	CLIErrorSubtype   string
	CLIErrorCode      string
	FailureSource     string
}

// OperationObserver receives only the fixed OperationObservation vocabulary.
type OperationObserver interface {
	ObserveOperation(OperationObservation)
}

type executionGateGuard struct {
	service      *FeishuOperationService
	userID       uint
	generation   uint64
	operationID  string
	owner        string
	ctx          context.Context
	cancel       context.CancelFunc
	stop         chan struct{}
	done         chan struct{}
	stopOnce     sync.Once
	registration *executionRegistration
}

// executionKey identifies the lifetime that can still hold a materialized
// plaintext CLI HOME. It is deliberately narrower than a user-wide mutex: a
// registry entry exists only after the durable execution gate was claimed and
// before an OperationRunner can be invoked.
type executionKey struct {
	userID      uint
	generation  uint64
	operationID string
}

type executionRegistration struct {
	cancel   context.CancelFunc
	done     chan struct{}
	doneOnce sync.Once
}

func (r *executionRegistration) finish() {
	if r == nil {
		return
	}
	r.doneOnce.Do(func() { close(r.done) })
}

// executionStart covers the pre-registration interval from durable execution
// gate acquisition through local guard registration. Retire joins it in
// addition to active callbacks so retired tombstones can be reclaimed without
// reopening a paused old-generation invocation.
type executionStart struct {
	cancel   context.CancelFunc
	done     chan struct{}
	doneOnce sync.Once
}

func (s *executionStart) finish() {
	if s == nil {
		return
	}
	s.doneOnce.Do(func() { close(s.done) })
}

// executionRegistry is a process-local lifecycle bridge. Durable operation
// leases remain the cross-instance source of truth; this registry adds the
// missing guarantee that this process has cancelled and joined every callback
// that could still hold a plaintext runtime HOME before Unbind deletes the
// encrypted snapshot. It holds no database, vault, or account locks.
type executionRegistry struct {
	mu      sync.Mutex
	active  map[executionKey]*executionRegistration
	starts  map[executionKey]*executionStart
	retired map[uint]uint64
}

func newExecutionRegistry() *executionRegistry {
	return &executionRegistry{
		active:  make(map[executionKey]*executionRegistration),
		starts:  make(map[executionKey]*executionStart),
		retired: make(map[uint]uint64),
	}
}

// begin registers an execution before it attempts the durable CLI gate. Stop
// can cancel and join this handoff even when a goroutine has claimed the
// durable gate but has not yet registered its local guard. A concurrent caller
// for the same durable operation joins the existing local execution instead of
// treating normal idempotent recovery as an infrastructure failure.
func (r *executionRegistry) begin(ctx context.Context, key executionKey) (context.Context, *executionStart, <-chan struct{}, error) {
	if r == nil || key.userID == 0 || key.generation == 0 || key.operationID == "" {
		return nil, nil, nil, ErrOperationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	startCtx, cancel := context.WithCancel(ctx)
	start := &executionStart{cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired[key.userID] >= key.generation {
		cancel()
		return nil, nil, nil, ErrOperationUnavailable
	}
	if existing, exists := r.starts[key]; exists {
		cancel()
		return nil, nil, existing.done, nil
	}
	if existing, exists := r.active[key]; exists {
		cancel()
		return nil, nil, existing.done, nil
	}
	r.starts[key] = start
	return startCtx, start, nil, nil
}

func (r *executionRegistry) finishStart(key executionKey, start *executionStart) {
	if r == nil || start == nil {
		return
	}
	r.mu.Lock()
	if r.starts[key] == start {
		delete(r.starts, key)
	}
	r.mu.Unlock()
	start.finish()
}

func (r *executionRegistry) register(key executionKey, cancel context.CancelFunc) (*executionRegistration, error) {
	if r == nil || key.userID == 0 || key.generation == 0 || key.operationID == "" || cancel == nil {
		return nil, ErrOperationUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if retiredGeneration := r.retired[key.userID]; retiredGeneration >= key.generation {
		return nil, ErrOperationUnavailable
	}
	if _, exists := r.active[key]; exists {
		return nil, ErrOperationUnavailable
	}
	registration := &executionRegistration{cancel: cancel, done: make(chan struct{})}
	r.active[key] = registration
	return registration, nil
}

func (r *executionRegistry) unregister(key executionKey, registration *executionRegistration) {
	if r == nil || registration == nil {
		return
	}
	r.mu.Lock()
	if current := r.active[key]; current == registration {
		delete(r.active, key)
	}
	r.mu.Unlock()
	registration.finish()
}

// stopGenerationAndWait establishes the local retired-generation fence and
// waits for every registration observed before that fence. A concurrent late
// registration sees retired[user] while holding the same mutex and is rejected
// before it can open a vault HOME or call the runner.
func (r *executionRegistry) stopGenerationAndWait(ctx context.Context, userID uint, generation uint64) error {
	if r == nil || userID == 0 || generation == 0 {
		return ErrOperationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if generation > r.retired[userID] {
		r.retired[userID] = generation
	}
	registrations := make([]*executionRegistration, 0)
	starts := make([]*executionStart, 0)
	for key, registration := range r.active {
		if key.userID == userID && key.generation == generation {
			registrations = append(registrations, registration)
		}
	}
	for key, start := range r.starts {
		if key.userID == userID && key.generation == generation {
			starts = append(starts, start)
		}
	}
	r.mu.Unlock()

	for _, start := range starts {
		start.cancel()
	}
	for _, registration := range registrations {
		registration.cancel()
	}
	for _, registration := range registrations {
		select {
		case <-registration.done:
		case <-ctx.Done():
			return ErrOperationUnavailable
		}
	}
	for _, start := range starts {
		select {
		case <-start.done:
		case <-ctx.Done():
			return ErrOperationUnavailable
		}
	}
	// All locally-started old-generation work is now joined. Any future stale
	// caller must first pass the durable account-generation predicate in the
	// execution-gate store, so retaining this per-user tombstone forever adds
	// memory without adding safety.
	r.mu.Lock()
	if r.retired[userID] == generation {
		delete(r.retired, userID)
	}
	r.mu.Unlock()
	return nil
}

// NewFeishuOperationService validates all mandatory operation dependencies.
func NewFeishuOperationService(deps OperationServiceDeps) (*FeishuOperationService, error) {
	if deps.Accounts == nil || deps.Operations == nil || deps.Catalog == nil ||
		deps.Recovery == nil || deps.Vault == nil || deps.Preflight == nil ||
		deps.Runner == nil || deps.Cipher == nil {
		return nil, errors.New("feishu operation service dependencies rejected")
	}
	if deps.VerifiedCLIVersion != "" && deps.VerifiedCLIVersion != LarkCLIVersion {
		return nil, errors.New("feishu operation service CLI evidence rejected")
	}
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	leaseDuration := deps.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = operationDefaultLeaseDuration
	}
	heartbeatInterval := deps.ExecutionGateHeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = operationExecutionGateHeartbeatInterval
	}
	if heartbeatInterval >= operationExecutionGateLeaseDuration {
		return nil, errors.New("feishu operation execution gate heartbeat interval rejected")
	}
	return &FeishuOperationService{
		accounts: deps.Accounts, operations: deps.Operations, catalog: deps.Catalog,
		recovery: deps.Recovery, confirmation: deps.Confirmation,
		vault: deps.Vault, preflight: deps.Preflight, runner: deps.Runner, cipher: deps.Cipher,
		classifier: NewErrorClassifier(), now: now, leaseDuration: leaseDuration,
		executionGateHeartbeatInterval: heartbeatInterval,
		verifiedCLIVersion:             deps.VerifiedCLIVersion,
		observer:                       deps.Observer,
		executions:                     newExecutionRegistry(),
	}, nil
}

func (s *FeishuOperationService) startExecutionGateGuard(
	ctx context.Context,
	operation *model.FeishuOperation,
	owner string,
) (*executionGateGuard, error) {
	if s == nil || s.executions == nil || operation == nil || owner == "" {
		return nil, ErrOperationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// This is deliberately repeated at the local-registration boundary. The
	// normal caller has already passed the durable execution gate, but a
	// generation can retire between that claim and guard registration. It also
	// makes this private primitive fail closed if reused by a future caller.
	account, err := s.accounts.Get(ctx, operation.UserID, ProviderLark)
	if err != nil || !validOperationAccount(account, operation.UserID) || account.Generation != operation.Generation {
		return nil, ErrOperationUnavailable
	}
	executionCtx, cancel := context.WithCancel(ctx)
	guard := &executionGateGuard{
		service: s, userID: operation.UserID, generation: operation.Generation,
		operationID: operation.ID, owner: owner, ctx: executionCtx, cancel: cancel,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	registration, err := s.executions.register(executionKey{
		userID: operation.UserID, generation: operation.Generation, operationID: operation.ID,
	}, cancel)
	if err != nil {
		cancel()
		return nil, ErrOperationUnavailable
	}
	guard.registration = registration
	go guard.heartbeat()
	return guard, nil
}

// beginExecutionStart keeps the local handoff visible to lifecycle teardown
// before the durable CLI gate is claimed. The returned release must cover both
// gate acquisition and the eventual guarded invocation.
func (s *FeishuOperationService) beginExecutionStart(
	ctx context.Context,
	operation *model.FeishuOperation,
) (context.Context, func(), <-chan struct{}, error) {
	if s == nil || s.executions == nil || operation == nil {
		return nil, nil, nil, ErrOperationUnavailable
	}
	key := executionKey{userID: operation.UserID, generation: operation.Generation, operationID: operation.ID}
	startCtx, start, joined, err := s.executions.begin(ctx, key)
	if err != nil {
		return nil, nil, nil, ErrOperationUnavailable
	}
	if joined != nil {
		return nil, nil, joined, nil
	}
	return startCtx, func() { s.executions.finishStart(key, start) }, nil, nil
}

// joinExecutionStart waits for the local owner to finish its current attempt,
// then returns the durable operation result. The first owner alone may invoke
// the CLI; followers must not acquire a second lease or replay the command.
func (s *FeishuOperationService) joinExecutionStart(
	ctx context.Context,
	operation *model.FeishuOperation,
	done <-chan struct{},
) (*OperationResult, error) {
	if operation == nil || done == nil {
		return nil, ErrOperationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
		return s.reloadResult(ctx, operation)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *executionGateGuard) heartbeat() {
	defer close(g.done)
	ticker := time.NewTicker(g.service.executionGateHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := g.renew(); err != nil {
				return
			}
		case <-g.ctx.Done():
			return
		case <-g.stop:
			return
		}
	}
}

func (g *executionGateGuard) renew() error {
	if g == nil || g.service == nil {
		return ErrOperationUnavailable
	}
	if err := g.ctx.Err(); err != nil {
		return ErrOperationUnavailable
	}
	now := g.service.now().UTC()
	renewCtx, cancel := context.WithTimeout(g.ctx, operationFinalizeTimeout)
	renewed, err := g.service.operations.RenewExecutionGate(
		renewCtx,
		g.userID,
		g.generation,
		g.owner,
		g.operationID,
		now,
		now.Add(operationExecutionGateLeaseDuration),
	)
	cancel()
	if err != nil || !renewed {
		g.cancel()
		return ErrOperationUnavailable
	}
	return nil
}

func (g *executionGateGuard) stopAndWait() {
	if g == nil {
		return
	}
	g.stopOnce.Do(func() {
		close(g.stop)
		g.cancel()
		<-g.done
		if g.service != nil && g.service.executions != nil {
			g.service.executions.unregister(executionKey{
				userID: g.userID, generation: g.generation, operationID: g.operationID,
			}, g.registration)
		}
	})
}

// StopGenerationAndWait prevents any late local execution from opening a
// retired HOME, cancels and joins every already-running local execution, then
// waits for the durable account-wide execution gate to be released or expire.
// The durable part is necessary because another service process can hold a CLI
// invocation that this process cannot cancel. Any timeout/error deliberately
// leaves the account disconnecting so the caller cannot report local deletion.
func (s *FeishuOperationService) StopGenerationAndWait(ctx context.Context, userID uint, generation uint64) error {
	if s == nil || s.executions == nil || userID == 0 || generation == 0 {
		return ErrOperationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.executions.stopGenerationAndWait(ctx, userID, generation); err != nil {
		return ErrOperationUnavailable
	}
	for {
		drained, err := s.operations.RetiredExecutionGateDrained(ctx, userID, generation, s.now().UTC())
		if err != nil {
			return ErrOperationUnavailable
		}
		if drained {
			return nil
		}
		timer := time.NewTimer(operationExecutionGatePollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ErrOperationUnavailable
		case <-timer.C:
		}
	}
}

// Execute normalizes and verifies a new request before atomically creating or
// returning the per-user idempotent operation.
func (s *FeishuOperationService) Execute(ctx context.Context, request ExecuteRequest) (*OperationResult, error) {
	if err := validateExecuteRequestIdentity(request); err != nil {
		return nil, err
	}
	normalized, err := s.catalog.Normalize(append([]string(nil), request.Argv...), append([]byte(nil), request.StdinJSON...))
	if err != nil {
		return nil, newOperationRequestValidation(request.Argv, err)
	}
	account, err := s.loadOrCreateAccount(ctx, request.UserID)
	if err != nil {
		return nil, err
	}
	persisted := persistedRequestFromNormalized(request, normalized)
	if prior := s.findEquivalentSucceededBaseCreate(ctx, account, persisted); prior != nil {
		return s.resultFromOperation(prior)
	}
	if proofOperationID := s.findSameRunEmptyCreateProof(ctx, account, normalized, request.AgentRunID); proofOperationID != "" {
		persisted.SameRunEmptyCreateProof = true
		persisted.CreateProofOperationID = proofOperationID
	}
	return s.createAndStartOperation(ctx, request, account, normalized, persisted)
}

// Connect creates a durable connection-only operation. It reuses the exact
// operation/session continuation path but never asks the catalog or CLI to
// execute a made-up business command.
func (s *FeishuOperationService) Connect(ctx context.Context, request ConnectOperationRequest) (*OperationResult, error) {
	executeIdentity := ExecuteRequest{
		UserID: request.UserID, AgentRunID: request.AgentRunID,
		ToolCallID: request.ToolCallID, IdempotencyKey: request.IdempotencyKey,
	}
	if err := validateExecuteRequestIdentity(executeIdentity); err != nil {
		return nil, err
	}
	account, err := s.loadOrCreateAccount(ctx, request.UserID)
	if err != nil {
		return nil, err
	}
	normalized := &NormalizedCommand{
		Path: connectionOnlyCommandPath, Domain: connectionOnlyDomain, Action: connectionOnlyAction,
		Risk: RiskRead, Scopes: []string{"offline_access"}, Argv: []string{"workspace", "connect"},
		ReplaySafeOnAuthError: true,
	}
	persisted := persistedRequestFromNormalized(executeIdentity, normalized)
	persisted.ConnectionOnly = true
	return s.createAndStartOperation(ctx, executeIdentity, account, normalized, persisted)
}

func (s *FeishuOperationService) createAndStartOperation(
	ctx context.Context,
	request ExecuteRequest,
	account *model.UserThirdPartyAccount,
	normalized *NormalizedCommand,
	persisted persistedOperationRequest,
) (*OperationResult, error) {
	operationID := uuid.NewString()
	plaintext, err := json.Marshal(persisted)
	if err != nil {
		return nil, ErrOperationIntegrity
	}
	owner := OperationCipherOwner{UserID: request.UserID, Generation: account.Generation, OperationID: operationID}
	requestCiphertext, keyVersion, err := s.sealOperationBlob(OperationCipherPurposeRequest, owner, plaintext)
	if err != nil {
		return nil, err
	}
	fingerprint := operationFingerprint(requestCiphertext)
	candidate := &model.FeishuOperation{
		ID: operationID, UserID: request.UserID, Generation: account.Generation,
		AgentRunID: request.AgentRunID, ToolCallID: request.ToolCallID,
		IdempotencyKey: request.IdempotencyKey, CommandPath: normalized.Path,
		Domain: normalized.Domain, RiskLevel: string(normalized.Risk),
		RequestCiphertext: requestCiphertext, KeyVersion: keyVersion,
		RequestFingerprint: fingerprint, State: model.FeishuOperationNotStarted,
	}
	stored, err := s.createOrGetOperation(ctx, candidate, &persisted, owner)
	if err != nil {
		if errors.Is(err, ErrOperationConnectionInProgress) {
			return nil, ErrOperationConnectionInProgress
		}
		return nil, ErrOperationUnavailable
	}
	if !sameImmutableOperation(stored, candidate) {
		return nil, ErrOperationIdempotencyConflict
	}
	executionRequest := persisted
	if stored.ID != candidate.ID {
		executionRequest, err = s.openPersistedRequest(stored)
		if err != nil {
			return nil, err
		}
		storedComparable, marshalErr := canonicalIdempotencyRequest(executionRequest)
		if marshalErr != nil {
			return nil, ErrOperationIntegrity
		}
		candidateComparable, marshalErr := canonicalIdempotencyRequest(persisted)
		if marshalErr != nil {
			return nil, ErrOperationIntegrity
		}
		if subtle.ConstantTimeCompare(storedComparable, candidateComparable) != 1 {
			return nil, ErrOperationIdempotencyConflict
		}
	}
	if stored.State == model.FeishuOperationExecuting {
		return s.reclaimExpiredExecution(ctx, account, stored, executionRequest)
	}
	if stored.State != model.FeishuOperationNotStarted {
		return s.resultFromOperation(stored)
	}
	return s.claimAndExecute(ctx, account, stored, executionRequest, "", false)
}

// Resume advances only an existing encrypted operation for the account's
// current generation. It accepts no argv, receipts, or replacement metadata.
func (s *FeishuOperationService) Resume(ctx context.Context, userID uint, operationID string) (*OperationResult, error) {
	if userID == 0 || !validStableIdentifier(operationID, operationMaxOperationIDBytes) {
		return nil, ErrOperationRequestRejected
	}
	account, err := s.accounts.Get(ctx, userID, ProviderLark)
	if err != nil || !validOperationAccount(account, userID) {
		return nil, ErrOperationUnavailable
	}
	operation, err := s.operations.GetOperationForUser(ctx, userID, account.Generation, operationID)
	if err != nil {
		return nil, ErrOperationUnavailable
	}
	if terminalOperationState(operation.State) {
		return s.resultFromOperation(operation)
	}
	if operation.State == model.FeishuOperationWaitingConfirmation && s.confirmation != nil {
		return s.resultFromOperation(operation)
	}

	persisted, err := s.openPersistedRequest(operation)
	if err != nil {
		return nil, err
	}
	if operation.State == model.FeishuOperationExecuting {
		return s.reclaimExpiredExecution(ctx, account, operation, persisted)
	}
	priorSignature := ""
	if recoveryWaitingState(operation.State) {
		summary, summaryErr := decodeOperationSummary(operation.ResultSummaryJSON)
		if summaryErr != nil || summary.RecoveryKind == "" || summary.RecoverySignature == "" {
			return nil, ErrOperationIntegrity
		}
		priorSignature = summary.RecoverySignature
		action, recoveryErr := s.recovery.StartRecovery(ctx, RecoveryRequest{
			UserID: operation.UserID, Generation: operation.Generation, OperationID: operation.ID,
			AgentRunID: operation.AgentRunID, ToolCallID: operation.ToolCallID,
			Kind: summary.RecoveryKind, Scopes: append([]string(nil), summary.RecoveryScopes...),
		})
		if recoveryErr != nil {
			return nil, ErrOperationUnavailable
		}
		if action != nil && !s.currentGrantSatisfiesUserAuthRecovery(ctx, account, operation, persisted, summary) {
			result := baseOperationResult(operation)
			result.SupersededSessionIDs = append([]string(nil), summary.SupersededSessionIDs...)
			result.Action = cloneOperationAction(action)
			result.Action.Provider = ProviderLark
			result.Action.OperationID = operation.ID
			result.Action.Phase = summary.Phase
			result.Action.Scopes = append([]string(nil), summary.RecoveryScopes...)
			return result, nil
		}
		account, err = s.accounts.Get(ctx, userID, ProviderLark)
		if err != nil || !validOperationAccount(account, userID) || account.Generation != operation.Generation {
			return nil, ErrOperationUnavailable
		}
	}
	return s.claimAndExecute(ctx, account, operation, persisted, priorSignature, false)
}

func (s *FeishuOperationService) currentGrantSatisfiesUserAuthRecovery(
	ctx context.Context,
	account *model.UserThirdPartyAccount,
	operation *model.FeishuOperation,
	persisted persistedOperationRequest,
	summary persistedOperationSummary,
) bool {
	if s == nil || account == nil || operation == nil || s.preflight == nil || s.vault == nil ||
		operation.State != model.FeishuOperationWaitingUserAuth ||
		account.ConnectionState != model.FeishuConnectionConnected || !account.Connected ||
		persisted.LocalOnly || len(persisted.Scopes) == 0 ||
		summary.Phase != model.FeishuAuthPhaseUserAuth {
		return false
	}
	switch summary.RecoveryKind {
	case RecoveryUserScope, RecoveryReauth:
	default:
		return false
	}
	check, err := s.checkScopesForCurrentGrant(ctx, operation, persisted.Scopes)
	return err == nil && check != nil && len(check.Missing) == 0
}

func (s *FeishuOperationService) checkScopesForCurrentGrant(
	ctx context.Context,
	operation *model.FeishuOperation,
	scopes []string,
) (*ScopeCheckResult, error) {
	if s == nil || s.preflight == nil || operation == nil || len(scopes) == 0 {
		return nil, ErrOperationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var check *ScopeCheckResult
	var checkErr error
	vaultErr := s.vault.WithHome(ctx, operation.UserID, operation.Generation, func(home string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, ErrOperationUnavailable
		}
		checkCtx, cancel := context.WithTimeout(ctx, ControlledLarkCLIVersionTimeout)
		defer cancel()
		check, checkErr = s.preflight.Check(checkCtx, home, append([]string(nil), scopes...))
		return false, nil
	})
	if vaultErr != nil {
		return nil, vaultErr
	}
	if checkErr != nil || check == nil {
		return nil, ErrOperationUnavailable
	}
	return &ScopeCheckResult{
		Granted: append([]string(nil), check.Granted...), Missing: append([]string(nil), check.Missing...),
	}, nil
}

// Confirm is retained for rolling-upgrade compatibility with persisted
// waiting_confirmation operations. Production no longer injects a confirmation
// requester, so the method aliases Resume and re-opens only the original
// encrypted request after the existing ownership and generation checks.
func (s *FeishuOperationService) Confirm(ctx context.Context, userID uint, operationID string) (*OperationResult, error) {
	if s != nil && s.confirmation == nil {
		return s.Resume(ctx, userID, operationID)
	}
	if userID == 0 || !validStableIdentifier(operationID, operationMaxOperationIDBytes) {
		return nil, ErrOperationRequestRejected
	}
	account, err := s.accounts.Get(ctx, userID, ProviderLark)
	if err != nil || !validOperationAccount(account, userID) {
		return nil, ErrOperationUnavailable
	}
	existing, err := s.operations.GetOperationForUser(ctx, userID, account.Generation, operationID)
	if err != nil || existing == nil {
		return nil, ErrOperationUnavailable
	}
	if terminalOperationState(existing.State) {
		return s.resultFromOperation(existing)
	}
	account, operation, persisted, err := s.loadWaitingConfirmation(ctx, userID, operationID)
	if err != nil {
		return nil, err
	}
	return s.claimAndExecute(ctx, account, operation, persisted, "", true)
}

// Cancel closes exactly one high-risk operation while it is still waiting for
// confirmation. Once execution starts, cancellation is intentionally rejected:
// a write that may have reached Feishu must follow the unknown-result rules.
func (s *FeishuOperationService) Cancel(ctx context.Context, userID uint, operationID string) (*OperationResult, error) {
	account, operation, _, err := s.loadWaitingConfirmation(ctx, userID, operationID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	owner := uuid.NewString()
	claimed, err := s.operations.ClaimOperation(
		ctx, userID, account.Generation, operation.ID, owner,
		[]string{model.FeishuOperationWaitingConfirmation}, now, now.Add(s.leaseDuration),
	)
	if err != nil {
		return nil, ErrOperationUnavailable
	}
	if !claimed {
		return s.reloadResult(ctx, operation)
	}
	summaryJSON, err := json.Marshal(persistedOperationSummary{Status: model.FeishuOperationCancelled})
	if err != nil {
		return nil, ErrOperationIntegrity
	}
	finalizeCtx, cancel := operationFinalizeContext(ctx)
	defer cancel()
	if err := s.operations.TransitionOperation(
		finalizeCtx, userID, account.Generation, operation.ID, owner,
		[]string{model.FeishuOperationWaitingConfirmation}, model.FeishuOperationCancelled, now,
		map[string]any{
			"finished_at": now, "error_type": "", "error_subtype": "", "error_code": "",
			"result_summary_json": datatypes.JSON(summaryJSON),
		},
	); err != nil {
		return s.reloadResult(ctx, operation)
	}
	operation.State = model.FeishuOperationCancelled
	operation.FinishedAt = &now
	operation.ResultSummaryJSON = append(datatypes.JSON(nil), summaryJSON...)
	return s.resultFromOperation(operation)
}

func (s *FeishuOperationService) loadWaitingConfirmation(
	ctx context.Context,
	userID uint,
	operationID string,
) (*model.UserThirdPartyAccount, *model.FeishuOperation, persistedOperationRequest, error) {
	if userID == 0 || !validStableIdentifier(operationID, operationMaxOperationIDBytes) {
		return nil, nil, persistedOperationRequest{}, ErrOperationRequestRejected
	}
	account, err := s.accounts.Get(ctx, userID, ProviderLark)
	if err != nil || !validOperationAccount(account, userID) {
		return nil, nil, persistedOperationRequest{}, ErrOperationUnavailable
	}
	operation, err := s.operations.GetOperationForUser(ctx, userID, account.Generation, operationID)
	if err != nil || operation == nil || operation.State != model.FeishuOperationWaitingConfirmation {
		return nil, nil, persistedOperationRequest{}, ErrOperationUnavailable
	}
	persisted, err := s.openPersistedRequest(operation)
	if err != nil || persisted.Risk != RiskHigh {
		return nil, nil, persistedOperationRequest{}, ErrOperationIntegrity
	}
	summary, err := decodeOperationSummary(operation.ResultSummaryJSON)
	if err != nil || summary.Phase != "confirmation" || strings.TrimSpace(summary.SessionID) == "" ||
		summary.ExpiresAt == nil || !summary.ExpiresAt.After(s.now().UTC()) {
		return nil, nil, persistedOperationRequest{}, ErrOperationUnavailable
	}
	return account, operation, persisted, nil
}

func (s *FeishuOperationService) reclaimExpiredExecution(
	ctx context.Context,
	account *model.UserThirdPartyAccount,
	operation *model.FeishuOperation,
	persisted persistedOperationRequest,
) (*OperationResult, error) {
	now := s.now().UTC()
	if operation.LeaseUntil != nil && operation.LeaseUntil.After(now) {
		return s.resultFromOperation(operation)
	}
	owner := uuid.NewString()
	gateHeld := false
	var gateGuard *executionGateGuard
	if account.ConnectionState == model.FeishuConnectionConnected && persisted.Risk == RiskRead && !persisted.LocalOnly {
		executionCtx, finishStart, joinedStart, startErr := s.beginExecutionStart(ctx, operation)
		if startErr != nil {
			return nil, startErr
		}
		if joinedStart != nil {
			return s.joinExecutionStart(ctx, operation, joinedStart)
		}
		defer finishStart()
		if err := s.waitForExecutionGate(executionCtx, operation, owner); err != nil {
			return nil, err
		}
		gateHeld = true
		var guardErr error
		gateGuard, guardErr = s.startExecutionGateGuard(executionCtx, operation, owner)
		if guardErr != nil {
			s.releaseExecutionGateDetached(ctx, operation, owner)
			return nil, guardErr
		}
		defer func() {
			if gateGuard != nil {
				gateGuard.stopAndWait()
			}
			if gateHeld {
				s.releaseExecutionGateDetached(ctx, operation, owner)
			}
		}()
		currentAccount, err := s.accounts.Get(executionCtx, operation.UserID, ProviderLark)
		if err != nil || !validOperationAccount(currentAccount, operation.UserID) || currentAccount.Generation != operation.Generation {
			return nil, ErrOperationUnavailable
		}
		account = currentAccount
		if account.ConnectionState != model.FeishuConnectionConnected && !persisted.LocalOnly {
			gateGuard.stopAndWait()
			gateGuard = nil
			s.releaseExecutionGateDetached(ctx, operation, owner)
			gateHeld = false
		}
	}
	claimed, err := s.operations.ClaimOperation(
		ctx,
		operation.UserID,
		operation.Generation,
		operation.ID,
		owner,
		[]string{model.FeishuOperationExecuting},
		now,
		now.Add(s.leaseDuration),
	)
	if err != nil {
		return nil, ErrOperationUnavailable
	}
	if !claimed {
		return s.reloadResult(ctx, operation)
	}
	operation.LeaseOwner = owner
	if persisted.Risk != RiskRead {
		return s.commitTerminal(ctx, operation, owner, model.FeishuOperationUnknown, PublicCodeUnknownResult, nil, true)
	}
	if err := s.operations.TransitionOperation(
		ctx,
		operation.UserID,
		operation.Generation,
		operation.ID,
		owner,
		[]string{model.FeishuOperationExecuting},
		model.FeishuOperationExecuting,
		now,
		map[string]any{"attempt_count": operation.AttemptCount + 1, "started_at": now},
	); err != nil {
		return s.reloadResult(ctx, operation)
	}
	operation.AttemptCount++
	operation.StartedAt = &now
	return s.executeClaimed(ctx, account, operation, owner, persisted, "", false, false, gateGuard)
}

func (s *FeishuOperationService) loadOrCreateAccount(ctx context.Context, userID uint) (*model.UserThirdPartyAccount, error) {
	account, err := s.accounts.Get(ctx, userID, ProviderLark)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		account, err = s.accounts.EnsurePlaceholder(ctx, userID, ProviderLark)
	}
	if err != nil || !validOperationAccount(account, userID) {
		return nil, ErrOperationUnavailable
	}
	return account, nil
}

func validOperationAccount(account *model.UserThirdPartyAccount, userID uint) bool {
	return account != nil && account.UserID == userID && account.Provider == ProviderLark && account.Generation != 0 &&
		account.ConnectionState != model.FeishuConnectionDisconnecting
}

func (s *FeishuOperationService) claimAndExecute(
	ctx context.Context,
	account *model.UserThirdPartyAccount,
	operation *model.FeishuOperation,
	persisted persistedOperationRequest,
	priorRecoverySignature string,
	confirmed bool,
) (*OperationResult, error) {
	if terminalOperationState(operation.State) || operation.State == model.FeishuOperationExecuting ||
		(operation.State == model.FeishuOperationWaitingConfirmation && !confirmed && s.confirmation != nil) {
		return s.resultFromOperation(operation)
	}
	if confirmed && operation.State != model.FeishuOperationWaitingConfirmation {
		return nil, ErrOperationUnavailable
	}
	owner := uuid.NewString()
	gateHeld := false
	var gateGuard *executionGateGuard
	proofUsable := false
	// A legacy requester may still leave a high-risk operation waiting outside
	// the business CLI gate. Production has no requester, so every connected
	// operation acquires the same account-wide gate before invoking the CLI.
	if operationRequiresExecutionGate(account, persisted, s.confirmation != nil) || confirmed ||
		operation.State == model.FeishuOperationWaitingConfirmation {
		executionCtx, finishStart, joinedStart, startErr := s.beginExecutionStart(ctx, operation)
		if startErr != nil {
			return nil, startErr
		}
		if joinedStart != nil {
			return s.joinExecutionStart(ctx, operation, joinedStart)
		}
		defer finishStart()
		if err := s.waitForExecutionGate(executionCtx, operation, owner); err != nil {
			return nil, err
		}
		gateHeld = true
		var guardErr error
		gateGuard, guardErr = s.startExecutionGateGuard(executionCtx, operation, owner)
		if guardErr != nil {
			s.releaseExecutionGateDetached(ctx, operation, owner)
			return nil, guardErr
		}
		defer func() {
			if gateGuard != nil {
				gateGuard.stopAndWait()
			}
			if gateHeld {
				s.releaseExecutionGateDetached(ctx, operation, owner)
			}
		}()
		currentAccount, err := s.accounts.Get(executionCtx, operation.UserID, ProviderLark)
		if err != nil || !validOperationAccount(currentAccount, operation.UserID) || currentAccount.Generation != operation.Generation {
			return nil, ErrOperationUnavailable
		}
		account = currentAccount
		if account.ConnectionState != model.FeishuConnectionConnected && !persisted.LocalOnly {
			gateGuard.stopAndWait()
			gateGuard = nil
			s.releaseExecutionGateDetached(ctx, operation, owner)
			gateHeld = false
		} else if persisted.SameRunEmptyCreateProof {
			proofUsable, _ = s.operations.IsOperationProofUsable(
				ctx,
				operation.UserID,
				operation.Generation,
				operation.AgentRunID,
				persisted.CreateProofOperationID,
				operation.ID,
			)
		}
	}
	now := s.now().UTC()
	claimed, err := s.operations.ClaimOperation(
		ctx, operation.UserID, operation.Generation, operation.ID, owner,
		[]string{operation.State}, now, now.Add(s.leaseDuration),
	)
	if err != nil {
		return nil, ErrOperationUnavailable
	}
	if !claimed {
		return s.reloadResult(ctx, operation)
	}
	from := operation.State
	if err := s.operations.TransitionOperation(ctx, operation.UserID, operation.Generation, operation.ID, owner,
		[]string{from}, model.FeishuOperationExecuting, now,
		map[string]any{"attempt_count": operation.AttemptCount + 1, "started_at": now}); err != nil {
		return s.reloadResult(ctx, operation)
	}
	operation.State = model.FeishuOperationExecuting
	operation.AttemptCount++
	operation.LeaseOwner = owner
	operation.StartedAt = &now
	return s.executeClaimed(ctx, account, operation, owner, persisted, priorRecoverySignature, proofUsable, confirmed, gateGuard)
}

func operationRequiresExecutionGate(
	account *model.UserThirdPartyAccount,
	persisted persistedOperationRequest,
	confirmationEnabled bool,
) bool {
	if persisted.LocalOnly {
		return false
	}
	if account == nil || account.ConnectionState != model.FeishuConnectionConnected {
		return false
	}
	return !confirmationEnabled || persisted.Risk != RiskHigh || persisted.SameRunEmptyCreateProof
}

func (s *FeishuOperationService) waitForExecutionGate(
	ctx context.Context,
	operation *model.FeishuOperation,
	owner string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	waitCtx, cancel := context.WithTimeout(ctx, operationExecutionGateWaitTimeout)
	defer cancel()
	for {
		now := s.now().UTC()
		claimed, err := s.operations.TryClaimExecutionGate(
			waitCtx,
			operation.UserID,
			operation.Generation,
			owner,
			operation.ID,
			now,
			now.Add(operationExecutionGateLeaseDuration),
		)
		if err != nil {
			if waitCtx.Err() != nil {
				return waitCtx.Err()
			}
			return ErrOperationUnavailable
		}
		if claimed {
			return nil
		}
		currentAccount, err := s.accounts.Get(waitCtx, operation.UserID, ProviderLark)
		if err != nil {
			if waitCtx.Err() != nil {
				return waitCtx.Err()
			}
			return ErrOperationUnavailable
		}
		if !validOperationAccount(currentAccount, operation.UserID) || currentAccount.Generation != operation.Generation {
			return ErrOperationUnavailable
		}
		timer := time.NewTimer(operationExecutionGatePollInterval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return waitCtx.Err()
		case <-timer.C:
		}
	}
}

func (s *FeishuOperationService) releaseExecutionGateDetached(
	ctx context.Context,
	operation *model.FeishuOperation,
	owner string,
) {
	finalizeCtx, cancel := operationFinalizeContext(ctx)
	defer cancel()
	_, _ = s.operations.ReleaseExecutionGate(
		finalizeCtx,
		operation.UserID,
		operation.Generation,
		owner,
		s.now().UTC(),
	)
}

func (s *FeishuOperationService) executeClaimed(
	ctx context.Context,
	account *model.UserThirdPartyAccount,
	operation *model.FeishuOperation,
	leaseOwner string,
	persisted persistedOperationRequest,
	priorRecoverySignature string,
	proofUsable bool,
	confirmed bool,
	executionGate *executionGateGuard,
) (*OperationResult, error) {
	if account.ConnectionState != model.FeishuConnectionConnected && !persisted.LocalOnly {
		kind := RecoveryCreateApp
		waitingState := model.FeishuOperationWaitingConnection
		publicCode := PublicCodeConnectionRequired
		switch account.ConnectionState {
		case model.FeishuConnectionAppReady, model.FeishuConnectionWaitingUserAuth, model.FeishuConnectionReauthRequired:
			kind = RecoveryReauth
			waitingState = model.FeishuOperationWaitingUserAuth
			publicCode = PublicCodeReauthRequired
		}
		return s.startRecoveryAndWait(ctx, operation, leaseOwner, persisted, kind, persisted.Scopes, waitingState, priorRecoverySignature, publicCode, "")
	}
	if persisted.ConnectionOnly {
		return s.commitTerminal(
			ctx, operation, leaseOwner, model.FeishuOperationSucceeded, "",
			json.RawMessage(`{"connected":true}`), false,
		)
	}
	if persisted.LocalOnly {
		data, err := s.catalog.resolveLocal(persisted.Argv)
		if err != nil {
			return s.commitTerminal(ctx, operation, leaseOwner, model.FeishuOperationFailed, PublicCodeFailed, nil, false)
		}
		return s.commitTerminal(ctx, operation, leaseOwner, model.FeishuOperationSucceeded, "", data, false)
	}
	if writeLikeRisk(persisted.Risk) {
		check, err := s.checkScopesBeforeWrite(ctx, operation, persisted, executionGate)
		if err != nil {
			return s.commitTerminal(ctx, operation, leaseOwner, model.FeishuOperationFailed, PublicCodeTemporaryError, nil, false)
		}
		if len(check.Missing) > 0 {
			return s.startRecoveryAndWait(
				ctx, operation, leaseOwner, persisted, RecoveryUserScope, check.Missing,
				model.FeishuOperationWaitingUserAuth, priorRecoverySignature, PublicCodeScopeRequired, "",
			)
		}
	}

	if s.confirmation != nil && persisted.Risk == RiskHigh && !proofUsable && !confirmed {
		action, err := s.confirmation.RequestConfirmation(ctx, operation.ID, ConfirmationSummary{
			CommandPath: persisted.CommandPath, Domain: persisted.Domain, Action: persisted.Action,
			Risk: persisted.Risk, RequiresCLIYes: persisted.RequiresCLIYes,
		})
		if err != nil || action == nil {
			return s.commitTerminal(ctx, operation, leaseOwner, model.FeishuOperationFailed, PublicCodeFailed, nil, false)
		}
		action = cloneOperationAction(action)
		action.Provider = ProviderLark
		action.OperationID = operation.ID
		action.Phase = "confirmation"
		action.Scopes = nil
		if strings.TrimSpace(action.SessionID) == "" || !action.ExpiresAt.After(s.now().UTC()) {
			return s.commitTerminal(ctx, operation, leaseOwner, model.FeishuOperationFailed, PublicCodeFailed, nil, false)
		}
		summary := persistedOperationSummary{Status: model.FeishuOperationWaitingConfirmation, Phase: action.Phase, SessionID: action.SessionID}
		expires := action.ExpiresAt.UTC()
		summary.ExpiresAt = &expires
		if err := s.transitionWaiting(ctx, operation, leaseOwner, model.FeishuOperationWaitingConfirmation, summary, ""); err != nil {
			return nil, err
		}
		result := baseOperationResult(operation)
		result.State = model.FeishuOperationWaitingConfirmation
		result.Action = cloneOperationAction(action)
		return result, nil
	}

	maxInvocations := 1
	if persisted.Risk == RiskRead {
		maxInvocations = 2
	}
	for invocation := 0; invocation < maxInvocations; invocation++ {
		invocationStartedAt := s.now().UTC()
		result, runErr, vaultErr := s.invokeOnce(operation, persisted, confirmed, executionGate)
		started := result != nil && result.InvocationStarted
		if vaultErr == nil && runErr == nil && result != nil && started && result.Envelope != nil &&
			result.Envelope.OK && json.Valid(result.Envelope.Data) {
			s.observeInvocation(operation, persisted.Risk, result, nil, nil, "succeeded", invocationStartedAt)
			return s.commitTerminal(ctx, operation, leaseOwner, model.FeishuOperationSucceeded, "", result.Envelope.Data, true)
		}

		classification := s.classifyInvocation(result, runErr, vaultErr, persisted.Scopes, persisted.Risk)
		outcomeClass := classification.PublicCode
		if outcomeClass == "" {
			outcomeClass = PublicCodeFailed
		}
		s.observeInvocation(operation, persisted.Risk, result, runErr, vaultErr, outcomeClass, invocationStartedAt)
		if started && writeLikeRisk(persisted.Risk) {
			// Scope preflight is the only safe authorization recovery boundary for
			// writes. Once the business process starts, even a structured CLI error
			// cannot prove that Feishu observed no side effect, so never replay it.
			return s.commitTerminal(
				ctx, operation, leaseOwner, model.FeishuOperationUnknown,
				PublicCodeUnknownResult, nil, true,
			)
		}
		if classification.RetryRead && invocation+1 < maxInvocations {
			continue
		}
		if classification.RetryRead {
			classification.RetryRead = false
			classification.TerminalState = model.FeishuOperationFailed
		}
		if classification.Recovery != RecoveryNone &&
			(persisted.ReplaySafeOnAuthError || classification.ProvenNoSideEffect) {
			waitingState := waitingStateForRecovery(classification.Recovery)
			if waitingState != "" {
				recoveryScopes := append([]string(nil), classification.MissingScopes...)
				if len(recoveryScopes) == 0 &&
					(classification.Recovery == RecoveryCreateApp || classification.Recovery == RecoveryReauth) {
					recoveryScopes = append([]string(nil), persisted.Scopes...)
				}
				consoleURL := ""
				if classification.Recovery == RecoveryAppScope && result != nil && result.Envelope != nil && result.Envelope.Error != nil {
					consoleURL = result.Envelope.Error.ConsoleURL
				}
				return s.startRecoveryAndWait(ctx, operation, leaseOwner, persisted, classification.Recovery,
					recoveryScopes, waitingState, priorRecoverySignature, classification.PublicCode, consoleURL)
			}
		}
		terminal := classification.TerminalState
		if terminal == "" {
			terminal = model.FeishuOperationFailed
		}
		return s.commitTerminal(ctx, operation, leaseOwner, terminal, classification.PublicCode, nil, started)
	}
	return s.commitTerminal(ctx, operation, leaseOwner, model.FeishuOperationFailed, PublicCodeFailed, nil, false)
}

func (s *FeishuOperationService) observeInvocation(
	operation *model.FeishuOperation,
	risk RiskLevel,
	result *CLIResult,
	runErr error,
	vaultErr error,
	outcomeClass string,
	startedAt time.Time,
) {
	if s == nil || s.observer == nil || operation == nil {
		return
	}
	duration := s.now().UTC().Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	observation := OperationObservation{
		UserID: operation.UserID, Generation: operation.Generation, OperationID: operation.ID,
		Phase: "invoke", OutcomeClass: outcomeClass, Risk: risk,
		ExitCode: -1, CLIVersion: s.verifiedCLIVersion, Duration: duration,
	}
	if result != nil {
		observation.InvocationStarted = result.InvocationStarted
		observation.ExitCode = result.ExitCode
	}
	observation.FailureSource = operationFailureSource(result, runErr, vaultErr, outcomeClass)
	if result != nil && result.Envelope != nil && result.Envelope.Error != nil {
		cliErr := result.Envelope.Error
		code, present, valid := normalizeClassifierCode(cliErr.Code)
		if valid && (!present || code != "") &&
			ValidOperationDiagnosticTuple(outcomeClass, cliErr.Type, cliErr.Subtype, code) {
			observation.CLIErrorType = cliErr.Type
			observation.CLIErrorSubtype = cliErr.Subtype
			observation.CLIErrorCode = code
		}
	}
	s.observer.ObserveOperation(observation)
}

func operationFailureSource(result *CLIResult, runErr, vaultErr error, outcomeClass string) string {
	if outcomeClass == "succeeded" {
		return ""
	}
	if vaultErr != nil {
		return "vault"
	}
	if result != nil && result.Envelope != nil && !result.Envelope.OK && result.Envelope.Error != nil {
		return "structured_cli_error"
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(runErr, errControlledCLIInvalidJSON) {
		return "malformed_output"
	}
	if errors.Is(runErr, errControlledCLIOutputLimit) {
		return "output_limit"
	}
	if runErr != nil {
		return "transport"
	}
	if result == nil || !result.InvocationStarted {
		return "not_started"
	}
	return "unclassified"
}

func (s *FeishuOperationService) checkScopesBeforeWrite(
	ctx context.Context,
	operation *model.FeishuOperation,
	persisted persistedOperationRequest,
	executionGate *executionGateGuard,
) (*ScopeCheckResult, error) {
	if s == nil || s.preflight == nil || operation == nil || !writeLikeRisk(persisted.Risk) {
		return nil, ErrOperationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx := ctx
	if executionGate != nil {
		runCtx = executionGate.ctx
	}
	var check *ScopeCheckResult
	var checkErr error
	vaultErr := s.vault.WithHome(runCtx, operation.UserID, operation.Generation, func(home string) (bool, error) {
		if executionGate != nil {
			if err := executionGate.renew(); err != nil {
				return false, ErrOperationUnavailable
			}
		}
		if err := runCtx.Err(); err != nil {
			return false, ErrOperationUnavailable
		}
		checkCtx, cancel := context.WithTimeout(runCtx, ControlledLarkCLIVersionTimeout)
		defer cancel()
		check, checkErr = s.preflight.Check(checkCtx, home, append([]string(nil), persisted.Scopes...))
		return false, nil
	})
	if vaultErr != nil {
		return nil, vaultErr
	}
	if checkErr != nil || check == nil {
		return nil, ErrOperationUnavailable
	}
	return &ScopeCheckResult{
		Granted: append([]string(nil), check.Granted...), Missing: append([]string(nil), check.Missing...),
	}, nil
}

// createOrGetOperation makes proof reservation and consumer creation one
// transaction. If another distinct operation already consumed the proof, the
// request is resealed without proof metadata and follows the normal high-risk
// execution path.
func (s *FeishuOperationService) createOrGetOperation(
	ctx context.Context,
	candidate *model.FeishuOperation,
	persisted *persistedOperationRequest,
	owner OperationCipherOwner,
) (*model.FeishuOperation, error) {
	if persisted.ConnectionOnly {
		connectionStore, ok := s.operations.(interface {
			CreateOrGetConnectionOperation(context.Context, *model.FeishuOperation) (*model.FeishuOperation, error)
		})
		if !ok {
			return nil, ErrOperationUnavailable
		}
		stored, err := connectionStore.CreateOrGetConnectionOperation(ctx, candidate)
		if errors.Is(err, store.ErrFeishuConnectionOperationInProgress) {
			return nil, ErrOperationConnectionInProgress
		}
		return stored, err
	}
	if !persisted.SameRunEmptyCreateProof {
		return s.operations.CreateOrGetOperation(ctx, candidate)
	}

	stored, err := s.operations.CreateOrGetOperationWithProof(ctx, candidate, persisted.CreateProofOperationID)
	if !errors.Is(err, store.ErrFeishuProofReservationUnavailable) {
		return stored, err
	}

	persisted.SameRunEmptyCreateProof = false
	persisted.CreateProofOperationID = ""
	plaintext, err := json.Marshal(persisted)
	if err != nil {
		return nil, ErrOperationIntegrity
	}
	requestCiphertext, keyVersion, err := s.sealOperationBlob(OperationCipherPurposeRequest, owner, plaintext)
	if err != nil {
		return nil, err
	}
	candidate.RequestCiphertext = requestCiphertext
	candidate.KeyVersion = keyVersion
	candidate.RequestFingerprint = operationFingerprint(requestCiphertext)
	return s.operations.CreateOrGetOperation(ctx, candidate)
}

func (s *FeishuOperationService) invokeOnce(
	operation *model.FeishuOperation,
	persisted persistedOperationRequest,
	confirmed bool,
	executionGate *executionGateGuard,
) (*CLIResult, error, error) {
	if executionGate == nil {
		return nil, nil, ErrOperationUnavailable
	}
	var result *CLIResult
	var runErr error
	vaultErr := s.vault.WithHome(executionGate.ctx, operation.UserID, operation.Generation, func(home string) (bool, error) {
		if err := executionGate.renew(); err != nil {
			return false, ErrOperationUnavailable
		}
		argv := append([]string(nil), persisted.Argv...)
		if (confirmed || s.confirmation == nil) && persisted.RequiresCLIYes {
			argv = append(argv, "--yes")
		}
		runCtx, cancel := context.WithTimeout(executionGate.ctx, ControlledLarkCLITimeout)
		defer cancel()
		result, runErr = s.runner.Run(runCtx, home, argv, append([]byte(nil), persisted.StdinJSON...))
		return result != nil && result.InvocationStarted, nil
	})
	return result, runErr, vaultErr
}

func (s *FeishuOperationService) classifyInvocation(
	result *CLIResult,
	runErr, vaultErr error,
	expectedScopes []string,
	risk RiskLevel,
) Classification {
	started := result != nil && result.InvocationStarted
	if vaultErr != nil {
		return s.classifier.ClassifyTransport(vaultErr, risk, started)
	}
	if result != nil && result.Envelope != nil && !result.Envelope.OK {
		return s.classifier.ClassifyEnvelope(result.Envelope, expectedScopes, risk, started)
	}
	return s.classifier.ClassifyTransport(runErr, risk, started)
}

func (s *FeishuOperationService) startRecoveryAndWait(
	ctx context.Context,
	operation *model.FeishuOperation,
	leaseOwner string,
	persisted persistedOperationRequest,
	kind RecoveryKind,
	scopes []string,
	waitingState string,
	priorSignature string,
	publicCode string,
	consoleURL string,
) (*OperationResult, error) {
	signature := operationRecoverySignature(kind, scopes)
	if priorSignature != "" && priorSignature == signature {
		return s.commitTerminal(ctx, operation, leaseOwner, model.FeishuOperationFailed, PublicCodeFailed, nil, false)
	}
	action, err := s.recovery.StartRecovery(ctx, RecoveryRequest{
		UserID: operation.UserID, Generation: operation.Generation, OperationID: operation.ID,
		AgentRunID: operation.AgentRunID, ToolCallID: operation.ToolCallID,
		Kind: kind, Scopes: append([]string(nil), scopes...), ConsoleURL: consoleURL,
	})
	if err != nil || action == nil {
		return s.commitTerminal(ctx, operation, leaseOwner, model.FeishuOperationFailed, PublicCodeFailed, nil, false)
	}
	action = cloneOperationAction(action)
	action.Provider = ProviderLark
	action.OperationID = operation.ID
	action.Phase = phaseForRecovery(kind)
	action.Scopes = append([]string(nil), scopes...)
	priorSummary, _ := decodeOperationSummary(operation.ResultSummaryJSON)
	priorSummary = advanceOperationSession(priorSummary, action.SessionID)
	summary := persistedOperationSummary{
		Status: waitingState, PublicCode: publicCode, Phase: action.Phase, SessionID: priorSummary.SessionID,
		RecoveryKind: kind, RecoveryScopes: append([]string(nil), scopes...), RecoverySignature: signature,
		SupersededSessionIDs: append([]string(nil), priorSummary.SupersededSessionIDs...),
	}
	if !action.ExpiresAt.IsZero() {
		expires := action.ExpiresAt.UTC()
		summary.ExpiresAt = &expires
	}
	if err := s.transitionWaiting(ctx, operation, leaseOwner, waitingState, summary, publicCode); err != nil {
		s.recovery.Abort(action.SessionID)
		return nil, err
	}
	if err := s.recovery.Activate(ctx, action.SessionID); err != nil {
		s.recovery.Abort(action.SessionID)
		return nil, ErrOperationUnavailable
	}
	result := baseOperationResult(operation)
	result.State = waitingState
	result.SupersededSessionIDs = append([]string(nil), summary.SupersededSessionIDs...)
	result.Action = cloneOperationAction(action)
	return result, nil
}

func (s *FeishuOperationService) transitionWaiting(
	ctx context.Context,
	operation *model.FeishuOperation,
	leaseOwner string,
	waitingState string,
	summary persistedOperationSummary,
	publicCode string,
) error {
	now := s.now().UTC()
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return ErrOperationIntegrity
	}
	fields := map[string]any{
		"error_type": "", "error_subtype": "", "error_code": "",
		"result_summary_json": datatypes.JSON(summaryJSON),
	}
	if publicCode != "" {
		fields["error_type"] = "classified"
		fields["error_subtype"] = publicCode
	}
	finalizeCtx, cancel := operationFinalizeContext(ctx)
	defer cancel()
	outcome := s.capabilityOutcome(operation, waitingState, publicCode, nil)
	var transitionErr error
	if outcome != nil {
		transitionErr = s.operations.TransitionOperationWithCapabilityOutcome(finalizeCtx, operation.UserID, operation.Generation, operation.ID,
			leaseOwner, []string{model.FeishuOperationExecuting}, waitingState, now, fields, *outcome)
	} else {
		transitionErr = s.operations.TransitionOperation(finalizeCtx, operation.UserID, operation.Generation, operation.ID,
			leaseOwner, []string{model.FeishuOperationExecuting}, waitingState, now, fields)
	}
	if transitionErr != nil {
		return ErrOperationUnavailable
	}
	operation.State = waitingState
	operation.ResultSummaryJSON = append(datatypes.JSON(nil), summaryJSON...)
	return nil
}

func (s *FeishuOperationService) commitTerminal(
	ctx context.Context,
	operation *model.FeishuOperation,
	leaseOwner string,
	state string,
	publicCode string,
	data json.RawMessage,
	invocationStarted bool,
) (*OperationResult, error) {
	now := s.now().UTC()
	fields := map[string]any{"finished_at": now, "error_type": "", "error_subtype": "", "error_code": ""}
	summary := persistedOperationSummary{Status: state, PublicCode: publicCode, BusinessStarted: invocationStarted}
	if state == model.FeishuOperationSucceeded {
		owner := OperationCipherOwner{UserID: operation.UserID, Generation: operation.Generation, OperationID: operation.ID}
		ciphertext, _, err := s.sealOperationBlob(OperationCipherPurposeResult, owner, append([]byte(nil), data...))
		if err != nil {
			if invocationStarted && writeLikeRisk(RiskLevel(operation.RiskLevel)) {
				state = model.FeishuOperationUnknown
				summary = persistedOperationSummary{Status: state, PublicCode: PublicCodeUnknownResult, BusinessStarted: invocationStarted}
			} else {
				state = model.FeishuOperationFailed
				summary = persistedOperationSummary{Status: state, PublicCode: PublicCodeFailed, BusinessStarted: invocationStarted}
			}
		} else {
			fields["result_ciphertext"] = ciphertext
		}
	}
	if state != model.FeishuOperationSucceeded {
		fields["error_type"] = "classified"
		fields["error_subtype"] = summary.PublicCode
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return nil, ErrOperationIntegrity
	}
	fields["result_summary_json"] = datatypes.JSON(summaryJSON)
	finalizeCtx, cancel := operationFinalizeContext(ctx)
	defer cancel()
	outcome := s.capabilityOutcome(operation, state, summary.PublicCode, operationSuccessTime(state, now))
	var transitionErr error
	if outcome != nil {
		transitionErr = s.operations.TransitionOperationWithCapabilityOutcome(finalizeCtx, operation.UserID, operation.Generation, operation.ID,
			leaseOwner, []string{model.FeishuOperationExecuting}, state, now, fields, *outcome)
	} else {
		transitionErr = s.operations.TransitionOperation(finalizeCtx, operation.UserID, operation.Generation, operation.ID,
			leaseOwner, []string{model.FeishuOperationExecuting}, state, now, fields)
	}
	if transitionErr != nil {
		if invocationStarted && writeLikeRisk(RiskLevel(operation.RiskLevel)) {
			result := baseOperationResult(operation)
			result.State = model.FeishuOperationUnknown
			result.Failure = newOperationFailure(PublicCodeUnknownResult, result.State, true, RiskLevel(operation.RiskLevel), nil)
			result.Failure.WriteFenceKey = s.writeFenceKeyFromOperation(operation)
			return result, nil
		}
		return nil, ErrOperationUnavailable
	}
	operation.State = state
	operation.FinishedAt = &now
	operation.ResultSummaryJSON = append(datatypes.JSON(nil), summaryJSON...)
	if ciphertext, ok := fields["result_ciphertext"].([]byte); ok {
		operation.ResultCiphertext = append([]byte(nil), ciphertext...)
	}
	return s.resultFromOperation(operation)
}

func operationSuccessTime(state string, now time.Time) *time.Time {
	if state != model.FeishuOperationSucceeded {
		return nil
	}
	at := now.UTC()
	return &at
}

// capabilityOutcome maps only fixed catalog domain and classifier-derived
// states to the status cache. Unknown, transport, and generic failures do not
// fabricate a capability conclusion.
func (s *FeishuOperationService) capabilityOutcome(
	operation *model.FeishuOperation,
	operationState string,
	publicCode string,
	succeededAt *time.Time,
) *model.FeishuCapabilityOutcome {
	if s == nil || operation == nil || !supportedCapabilityDomain(operation.Domain) {
		return nil
	}
	if spec, ok := s.catalog.specs[operation.CommandPath]; ok && spec.localOnly {
		return nil
	}
	state := ""
	switch {
	case operationState == model.FeishuOperationSucceeded:
		state = model.FeishuCapabilityAvailable
	case operationState == model.FeishuOperationWaitingAppScope:
		state = model.FeishuCapabilityNeedsAppScope
	case operationState == model.FeishuOperationWaitingUserAuth:
		state = model.FeishuCapabilityNeedsUserScope
	case operationState == model.FeishuOperationFailed && publicCode == PublicCodeResourceDenied:
		state = model.FeishuCapabilityResourceDenied
	default:
		return nil
	}
	outcome := &model.FeishuCapabilityOutcome{Domain: operation.Domain, State: state}
	if state == model.FeishuCapabilityAvailable && succeededAt != nil {
		at := succeededAt.UTC()
		outcome.SucceededAt = &at
		outcome.CLIVersion = s.verifiedCLIVersion
	}
	return outcome
}

func supportedCapabilityDomain(domain string) bool {
	return domain == "docs" || domain == "base" || domain == "wiki" || domain == SkillDomainDrive
}

func operationFinalizeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), operationFinalizeTimeout)
}

func (s *FeishuOperationService) resultFromOperation(operation *model.FeishuOperation) (*OperationResult, error) {
	result := baseOperationResult(operation)
	if operation.State == model.FeishuOperationSucceeded {
		if len(operation.ResultCiphertext) == 0 {
			return nil, ErrOperationIntegrity
		}
		owner := OperationCipherOwner{UserID: operation.UserID, Generation: operation.Generation, OperationID: operation.ID}
		plaintext, _, err := s.openOperationBlob(OperationCipherPurposeResult, owner, "", operation.ResultCiphertext)
		if err != nil || !json.Valid(plaintext) {
			return nil, ErrOperationIntegrity
		}
		result.Data = append(json.RawMessage(nil), plaintext...)
		return result, nil
	}
	if recoveryWaitingState(operation.State) || operation.State == model.FeishuOperationWaitingConfirmation {
		summary, err := decodeOperationSummary(operation.ResultSummaryJSON)
		if err != nil {
			return nil, ErrOperationIntegrity
		}
		result.Action = &OperationAction{
			Provider: ProviderLark, OperationID: operation.ID, SessionID: summary.SessionID,
			Phase: summary.Phase, Scopes: append([]string(nil), summary.RecoveryScopes...),
		}
		result.SupersededSessionIDs = append([]string(nil), summary.SupersededSessionIDs...)
		if summary.ExpiresAt != nil {
			result.Action.ExpiresAt = summary.ExpiresAt.UTC()
		}
		return result, nil
	}
	if terminalOperationState(operation.State) {
		summary, err := decodeOperationSummary(operation.ResultSummaryJSON)
		legacyOrStaleSummary := err != nil || summary.Status != operation.State
		if legacyOrStaleSummary {
			// Legacy rows and a terminal transition won by another process may not
			// carry the current summary schema. Return only a conservative stable
			// failure; never infer that a business call started or expose DB fields.
			summary = persistedOperationSummary{Status: operation.State, PublicCode: PublicCodeFailed}
		}
		publicCode := summary.PublicCode
		if operation.State == model.FeishuOperationUnknown {
			publicCode = PublicCodeUnknownResult
			if legacyOrStaleSummary {
				summary.BusinessStarted = true
			}
		}
		if operation.State == model.FeishuOperationCancelled {
			publicCode = PublicCodeCancelled
		}
		result.Failure = newOperationFailure(
			publicCode, operation.State, summary.BusinessStarted, RiskLevel(operation.RiskLevel), summary.RecoveryScopes,
		)
		if operation.State == model.FeishuOperationUnknown && summary.BusinessStarted {
			result.Failure.WriteFenceKey = s.writeFenceKeyFromOperation(operation)
		}
	}
	return result, nil
}

func (s *FeishuOperationService) writeFenceKeyFromOperation(operation *model.FeishuOperation) string {
	if s == nil || operation == nil {
		return ""
	}
	persisted, err := s.openPersistedRequest(operation)
	if err != nil {
		return ""
	}
	return exactWriteFenceKey(persisted.CommandPath, persisted.Argv, persisted.StdinJSON, persisted.Risk)
}

func (s *FeishuOperationService) reloadResult(ctx context.Context, operation *model.FeishuOperation) (*OperationResult, error) {
	latest, err := s.operations.GetOperationForUser(ctx, operation.UserID, operation.Generation, operation.ID)
	if err != nil {
		return nil, ErrOperationUnavailable
	}
	return s.resultFromOperation(latest)
}

func (s *FeishuOperationService) openPersistedRequest(operation *model.FeishuOperation) (persistedOperationRequest, error) {
	owner := OperationCipherOwner{UserID: operation.UserID, Generation: operation.Generation, OperationID: operation.ID}
	plaintext, keyVersion, err := s.openOperationBlob(OperationCipherPurposeRequest, owner, operation.KeyVersion, operation.RequestCiphertext)
	if err != nil || keyVersion != operation.KeyVersion ||
		operationFingerprint(operation.RequestCiphertext) != operation.RequestFingerprint {
		return persistedOperationRequest{}, ErrOperationIntegrity
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	var persisted persistedOperationRequest
	if err := decoder.Decode(&persisted); err != nil {
		return persistedOperationRequest{}, ErrOperationIntegrity
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return persistedOperationRequest{}, ErrOperationIntegrity
	}
	canonical, err := json.Marshal(persisted)
	if err != nil || !bytes.Equal(canonical, plaintext) {
		return persistedOperationRequest{}, ErrOperationIntegrity
	}
	if persisted.AgentRunID != operation.AgentRunID || persisted.ToolCallID != operation.ToolCallID ||
		persisted.IdempotencyKey != operation.IdempotencyKey || persisted.CommandPath != operation.CommandPath ||
		persisted.Domain != operation.Domain || string(persisted.Risk) != operation.RiskLevel ||
		!validPersistedCreateProof(persisted) {
		return persistedOperationRequest{}, ErrOperationIntegrity
	}
	if persisted.ConnectionOnly {
		if !validConnectionOnlyRequest(persisted) {
			return persistedOperationRequest{}, ErrOperationIntegrity
		}
	} else {
		if len(persisted.Argv) == 0 || validateControlledCLIInput(persisted.Argv, persisted.StdinJSON) != nil {
			return persistedOperationRequest{}, ErrOperationIntegrity
		}
		spec, cataloged := s.catalog.specs[persisted.CommandPath]
		if !cataloged || persisted.LocalOnly != spec.localOnly {
			return persistedOperationRequest{}, ErrOperationIntegrity
		}
	}
	persisted.Argv = append([]string(nil), persisted.Argv...)
	persisted.Scopes = append([]string(nil), persisted.Scopes...)
	persisted.StdinJSON = append(json.RawMessage(nil), persisted.StdinJSON...)
	return persisted, nil
}

func (s *FeishuOperationService) sealOperationBlob(
	purpose OperationCipherPurpose,
	owner OperationCipherOwner,
	plaintext []byte,
) ([]byte, string, error) {
	ciphertext, keyVersion, err := s.cipher.Seal(purpose, owner, plaintext)
	if err != nil {
		return nil, "", ErrOperationIntegrity
	}
	blob, err := json.Marshal(operationSealedBlob{KeyVersion: keyVersion, Ciphertext: ciphertext})
	if err != nil {
		return nil, "", ErrOperationIntegrity
	}
	return blob, keyVersion, nil
}

func (s *FeishuOperationService) openOperationBlob(
	purpose OperationCipherPurpose,
	owner OperationCipherOwner,
	expectedKeyVersion string,
	blob []byte,
) ([]byte, string, error) {
	sealed, err := parseOperationSealedBlob(blob)
	if err != nil || (expectedKeyVersion != "" && sealed.KeyVersion != expectedKeyVersion) {
		return nil, "", ErrOperationIntegrity
	}
	plaintext, err := s.cipher.Open(purpose, owner, sealed.KeyVersion, sealed.Ciphertext)
	if err != nil {
		return nil, "", ErrOperationIntegrity
	}
	return plaintext, sealed.KeyVersion, nil
}

// OperationSealedBlobKeyVersion returns the canonical version embedded in a
// persisted operation blob without exposing its ciphertext or plaintext.
func OperationSealedBlobKeyVersion(blob []byte) (string, error) {
	sealed, err := parseOperationSealedBlob(blob)
	if err != nil {
		return "", ErrOperationIntegrity
	}
	return sealed.KeyVersion, nil
}

func parseOperationSealedBlob(blob []byte) (operationSealedBlob, error) {
	decoder := json.NewDecoder(bytes.NewReader(blob))
	decoder.DisallowUnknownFields()
	var sealed operationSealedBlob
	if err := decoder.Decode(&sealed); err != nil || len(sealed.Ciphertext) == 0 ||
		validateCLIHomeKeyVersion(sealed.KeyVersion) != nil || sealed.KeyVersion != strings.ToLower(sealed.KeyVersion) {
		return operationSealedBlob{}, ErrOperationIntegrity
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return operationSealedBlob{}, ErrOperationIntegrity
	}
	return sealed, nil
}

func persistedRequestFromNormalized(request ExecuteRequest, normalized *NormalizedCommand) persistedOperationRequest {
	return persistedOperationRequest{
		AgentRunID: request.AgentRunID, ToolCallID: request.ToolCallID, IdempotencyKey: request.IdempotencyKey,
		CommandPath: normalized.Path, Domain: normalized.Domain, Action: normalized.Action, Risk: normalized.Risk,
		LocalOnly: normalized.LocalOnly, RequiresCLIYes: normalized.RequiresCLIYes, ReplaySafeOnAuthError: normalized.ReplaySafeOnAuthError,
		Scopes: append([]string(nil), normalized.Scopes...), Argv: append([]string(nil), normalized.Argv...),
		StdinJSON: append(json.RawMessage(nil), normalized.StdinJSON...),
	}
}

func canonicalIdempotencyRequest(request persistedOperationRequest) ([]byte, error) {
	request.SameRunEmptyCreateProof = false
	request.CreateProofOperationID = ""
	return json.Marshal(request)
}

// findEquivalentSucceededBaseCreate prevents a model retry with a new tool-call
// ID from creating a second empty Base in the same Agent run. The match is
// intentionally narrow: same user/account generation/run and the exact
// normalized base-create payload. Different names, schemas, runs, or users are
// still independent operations.
func (s *FeishuOperationService) findEquivalentSucceededBaseCreate(
	ctx context.Context,
	account *model.UserThirdPartyAccount,
	request persistedOperationRequest,
) *model.FeishuOperation {
	if account == nil || request.CommandPath != "base +base-create" || request.AgentRunID == 0 {
		return nil
	}
	candidates, err := s.operations.ListSucceededBaseCreatesForRun(
		ctx, account.UserID, account.Generation, request.AgentRunID,
	)
	if err != nil {
		return nil
	}
	target, err := canonicalSameRunCreateRequest(request)
	if err != nil {
		return nil
	}
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.UserID != account.UserID || candidate.Generation != account.Generation ||
			candidate.AgentRunID != request.AgentRunID || candidate.State != model.FeishuOperationSucceeded ||
			candidate.CommandPath != request.CommandPath {
			continue
		}
		prior, openErr := s.openPersistedRequest(candidate)
		if openErr != nil {
			continue
		}
		comparable, marshalErr := canonicalSameRunCreateRequest(prior)
		if marshalErr == nil && subtle.ConstantTimeCompare(comparable, target) == 1 {
			return candidate
		}
	}
	return nil
}

func canonicalSameRunCreateRequest(request persistedOperationRequest) ([]byte, error) {
	request.ToolCallID = ""
	request.IdempotencyKey = ""
	request.SameRunEmptyCreateProof = false
	request.CreateProofOperationID = ""
	return json.Marshal(request)
}

func (s *FeishuOperationService) findSameRunEmptyCreateProof(
	ctx context.Context,
	account *model.UserThirdPartyAccount,
	command *NormalizedCommand,
	agentRunID uint64,
) string {
	target, eligible := sameRunOverwriteTarget(command)
	if !eligible {
		return ""
	}
	candidates, err := s.operations.ListSucceededCreatesForRun(ctx, account.UserID, account.Generation, agentRunID)
	if err != nil {
		return ""
	}
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.UserID != account.UserID || candidate.Generation != account.Generation ||
			candidate.AgentRunID != agentRunID || candidate.State != model.FeishuOperationSucceeded {
			continue
		}
		prior, openErr := s.openPersistedRequest(candidate)
		if openErr != nil || !eligibleEmptyCreateRequest(prior) {
			continue
		}
		result, resultErr := s.openSucceededResult(candidate)
		if resultErr != nil || !createResultProvesTarget(prior.CommandPath, result, target) {
			continue
		}
		return candidate.ID
	}
	return ""
}

func (s *FeishuOperationService) openSucceededResult(operation *model.FeishuOperation) (json.RawMessage, error) {
	if operation == nil || operation.State != model.FeishuOperationSucceeded || len(operation.ResultCiphertext) == 0 {
		return nil, ErrOperationIntegrity
	}
	owner := OperationCipherOwner{UserID: operation.UserID, Generation: operation.Generation, OperationID: operation.ID}
	plaintext, _, err := s.openOperationBlob(OperationCipherPurposeResult, owner, "", operation.ResultCiphertext)
	if err != nil || !json.Valid(plaintext) {
		return nil, ErrOperationIntegrity
	}
	return append(json.RawMessage(nil), plaintext...), nil
}

func sameRunOverwriteTarget(command *NormalizedCommand) (string, bool) {
	if command == nil || command.Path != "docs +update" || command.Risk != RiskHigh || command.Action != "update" ||
		operationArgValue(command.Argv, "--command") != "overwrite" {
		return "", false
	}
	target := operationArgValue(command.Argv, "--doc")
	target, err := normalizeSupportedRef("document", map[string]bool{"docx": true, "wiki": true}, true)(target)
	if err != nil {
		return "", false
	}
	return target, true
}

func eligibleEmptyCreateRequest(request persistedOperationRequest) bool {
	switch request.CommandPath {
	case "docs +create":
		return operationArgValue(request.Argv, "--title") != "" && !operationHasArg(request.Argv, "--content")
	case "wiki +node-create":
		nodeType := operationArgValue(request.Argv, "--node-type")
		objectType := operationArgValue(request.Argv, "--obj-type")
		return operationArgValue(request.Argv, "--title") != "" &&
			(nodeType == "" || nodeType == "origin") && (objectType == "" || objectType == "docx")
	default:
		return false
	}
}

func createResultProvesTarget(commandPath string, result json.RawMessage, target string) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(result, &fields); err != nil {
		return false
	}
	switch commandPath {
	case "docs +create":
		documentRaw, ok := fields["document"]
		if !ok {
			return false
		}
		var document map[string]json.RawMessage
		if err := json.Unmarshal(documentRaw, &document); err != nil || document == nil {
			return false
		}
		documentID, ok := strictOpaqueResultField(document, "document_id")
		return ok && documentID == target
	case "wiki +node-create":
		objectType, ok := strictStringResultField(fields, "obj_type")
		if !ok || objectType != "docx" {
			return false
		}
		nodeToken, nodeOK := strictOpaqueResultField(fields, "node_token")
		objectToken, objectOK := strictOpaqueResultField(fields, "obj_token")
		return nodeOK && objectOK && (target == nodeToken || target == objectToken)
	default:
		return false
	}
}

func strictOpaqueResultField(fields map[string]json.RawMessage, name string) (string, bool) {
	value, ok := strictStringResultField(fields, name)
	return value, ok && opaqueTokenPattern.MatchString(value)
}

func strictStringResultField(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := fields[name]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return "", false
	}
	return value, true
}

func operationArgValue(argv []string, flag string) string {
	for index := 0; index+1 < len(argv); index++ {
		if argv[index] == flag {
			return argv[index+1]
		}
	}
	return ""
}

func operationHasArg(argv []string, flag string) bool {
	for _, argument := range argv {
		if argument == flag {
			return true
		}
	}
	return false
}

func validPersistedCreateProof(request persistedOperationRequest) bool {
	if !request.SameRunEmptyCreateProof {
		return request.CreateProofOperationID == ""
	}
	return validStableIdentifier(request.CreateProofOperationID, operationMaxOperationIDBytes) &&
		request.CommandPath == "docs +update" && request.Risk == RiskHigh && request.Action == "update" &&
		operationArgValue(request.Argv, "--command") == "overwrite"
}

func validConnectionOnlyRequest(request persistedOperationRequest) bool {
	return request.ConnectionOnly && request.CommandPath == connectionOnlyCommandPath &&
		request.Domain == connectionOnlyDomain && request.Action == connectionOnlyAction &&
		request.Risk == RiskRead && !request.LocalOnly && !request.RequiresCLIYes &&
		request.ReplaySafeOnAuthError && len(request.Scopes) == 1 && request.Scopes[0] == "offline_access" &&
		len(request.StdinJSON) == 0 && len(request.Argv) == 2 &&
		request.Argv[0] == "workspace" && request.Argv[1] == "connect" &&
		!request.SameRunEmptyCreateProof && request.CreateProofOperationID == ""
}

func sameImmutableOperation(stored, candidate *model.FeishuOperation) bool {
	return stored != nil && candidate != nil && stored.UserID == candidate.UserID && stored.Generation == candidate.Generation &&
		stored.AgentRunID == candidate.AgentRunID && stored.ToolCallID == candidate.ToolCallID &&
		stored.IdempotencyKey == candidate.IdempotencyKey && stored.CommandPath == candidate.CommandPath &&
		stored.Domain == candidate.Domain && stored.RiskLevel == candidate.RiskLevel
}

func validateExecuteRequestIdentity(request ExecuteRequest) error {
	if request.UserID == 0 || request.AgentRunID == 0 || !validStableIdentifier(request.ToolCallID, operationMaxToolCallIDBytes) {
		return ErrOperationRequestRejected
	}
	expected := fmt.Sprintf("%d:%s", request.AgentRunID, request.ToolCallID)
	if len(expected) > operationMaxIdempotencyBytes || request.IdempotencyKey != expected {
		return ErrOperationRequestRejected
	}
	return nil
}

func validStableIdentifier(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func operationFingerprint(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func operationRecoverySignature(kind RecoveryKind, scopes []string) string {
	canonical, _ := json.Marshal(struct {
		Kind   RecoveryKind `json:"kind"`
		Scopes []string     `json:"scopes"`
	}{Kind: kind, Scopes: append([]string(nil), scopes...)})
	return string(canonical)
}

func waitingStateForRecovery(kind RecoveryKind) string {
	switch kind {
	case RecoveryCreateApp:
		return model.FeishuOperationWaitingConnection
	case RecoveryAppScope:
		return model.FeishuOperationWaitingAppScope
	case RecoveryUserScope, RecoveryReauth:
		return model.FeishuOperationWaitingUserAuth
	default:
		return ""
	}
}

func phaseForRecovery(kind RecoveryKind) string {
	switch kind {
	case RecoveryCreateApp:
		return model.FeishuAuthPhaseCreateApp
	case RecoveryAppScope:
		return model.FeishuAuthPhaseAppScope
	case RecoveryUserScope, RecoveryReauth:
		return model.FeishuAuthPhaseUserAuth
	default:
		return ""
	}
}

func terminalOperationState(state string) bool {
	switch state {
	case model.FeishuOperationSucceeded, model.FeishuOperationFailed, model.FeishuOperationUnknown, model.FeishuOperationCancelled:
		return true
	default:
		return false
	}
}

func recoveryWaitingState(state string) bool {
	switch state {
	case model.FeishuOperationWaitingConnection, model.FeishuOperationWaitingAppScope, model.FeishuOperationWaitingUserAuth:
		return true
	default:
		return false
	}
}

func baseOperationResult(operation *model.FeishuOperation) *OperationResult {
	return &OperationResult{
		OperationID: operation.ID, State: operation.State,
		AgentRunID: operation.AgentRunID, ToolCallID: operation.ToolCallID,
	}
}

func cloneOperationAction(action *OperationAction) *OperationAction {
	if action == nil {
		return nil
	}
	clone := *action
	clone.Scopes = append([]string(nil), action.Scopes...)
	return &clone
}

func advanceOperationSession(summary persistedOperationSummary, nextSessionID string) persistedOperationSummary {
	nextSessionID = strings.TrimSpace(nextSessionID)
	lineage := make([]string, 0, operationSessionLineageLimit)
	seen := map[string]struct{}{}
	appendSession := func(sessionID string) {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" || sessionID == nextSessionID {
			return
		}
		if _, ok := seen[sessionID]; ok {
			return
		}
		seen[sessionID] = struct{}{}
		lineage = append(lineage, sessionID)
	}
	for _, sessionID := range summary.SupersededSessionIDs {
		appendSession(sessionID)
	}
	appendSession(summary.SessionID)
	if len(lineage) > operationSessionLineageLimit {
		lineage = append([]string(nil), lineage[len(lineage)-operationSessionLineageLimit:]...)
	}
	summary.SessionID = nextSessionID
	summary.SupersededSessionIDs = lineage
	return summary
}

func restoreOperationSession(summary persistedOperationSummary, restoredSessionID string) persistedOperationSummary {
	restoredSessionID = strings.TrimSpace(restoredSessionID)
	lineage := make([]string, 0, len(summary.SupersededSessionIDs))
	for _, sessionID := range summary.SupersededSessionIDs {
		if strings.TrimSpace(sessionID) != "" && sessionID != restoredSessionID {
			lineage = append(lineage, sessionID)
		}
	}
	summary.SessionID = restoredSessionID
	summary.SupersededSessionIDs = lineage
	return summary
}

func decodeOperationSummary(raw []byte) (persistedOperationSummary, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var summary persistedOperationSummary
	if err := decoder.Decode(&summary); err != nil || summary.Status == "" {
		return persistedOperationSummary{}, ErrOperationIntegrity
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return persistedOperationSummary{}, ErrOperationIntegrity
	}
	summary.RecoveryScopes = append([]string(nil), summary.RecoveryScopes...)
	summary.SupersededSessionIDs = append([]string(nil), summary.SupersededSessionIDs...)
	return summary, nil
}
