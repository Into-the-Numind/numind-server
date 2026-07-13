package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

type classifierFixture struct {
	OK                   bool            `json:"ok"`
	Type                 string          `json:"type"`
	Subtype              string          `json:"subtype"`
	Code                 json.RawMessage `json:"code,omitempty"`
	MissingScopes        []string        `json:"missing_scopes"`
	PermissionViolations json.RawMessage `json:"permission_violations"`
	Identity             string          `json:"identity"`
	ConsoleURLPresent    bool            `json:"console_url_present"`
}

func TestErrorClassifier_RealS2MissingScopeIsReplayable(t *testing.T) {
	t.Parallel()

	// This is the sanitized tuple observed in the S2 real-tenant spike. The
	// config/not_configured and authentication/token_missing fixtures were also
	// observed with an isolated HOME; all remaining fixtures are fixed synthetic
	// contract cases. None contain URLs, app IDs, or user identifiers.
	envelope := loadErrorClassifierFixture(t, "real-docs-create-missing-scope.json")
	got := NewErrorClassifier().ClassifyEnvelope(envelope, docsCreateScopes(), RiskWrite, true)

	require.Equal(t, RecoveryUserScope, got.Recovery)
	require.Equal(t, []string{"docx:document:create"}, got.MissingScopes)
	require.True(t, got.ProvenNoSideEffect)
	require.False(t, got.RetryRead)
	require.Empty(t, got.TerminalState)
	require.Equal(t, PublicCodeScopeRequired, got.PublicCode)
}

func TestErrorClassifier_SyntheticAppScopeContractsDoNotProveStartedWrites(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	tests := []struct {
		name    string
		fixture string
		scopes  []string
		expect  []string
	}{
		{
			name:    "missing scope with exact app evidence",
			fixture: "fixed-base-app-scope-missing.json",
			scopes: []string{
				"base:app:create",
				"base:table:create",
				"base:table:delete",
				"base:table:read",
				"base:table:update",
			},
			expect: baseCreateScopes(),
		},
		{
			name:    "fixed cli app scope subtype",
			fixture: "fixed-app-scope-not-applied.json",
			scopes:  []string{"wiki:space:write_only"},
			expect:  wikiSpaceCreateScopes(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			envelope := loadErrorClassifierFixture(t, tt.fixture)

			startedWrite := classifier.ClassifyEnvelope(envelope, tt.expect, RiskWrite, true)
			require.Equal(t, RecoveryNone, startedWrite.Recovery)
			require.Empty(t, startedWrite.MissingScopes)
			require.False(t, startedWrite.ProvenNoSideEffect)
			require.Equal(t, model.FeishuOperationUnknown, startedWrite.TerminalState)
			require.Equal(t, PublicCodeUnknownResult, startedWrite.PublicCode)
			startedHighRisk := classifier.ClassifyEnvelope(envelope, tt.expect, RiskHigh, true)
			require.Equal(t, RecoveryNone, startedHighRisk.Recovery)
			require.False(t, startedHighRisk.ProvenNoSideEffect)
			require.Equal(t, model.FeishuOperationUnknown, startedHighRisk.TerminalState)

			read := classifier.ClassifyEnvelope(envelope, tt.expect, RiskRead, true)
			require.Equal(t, RecoveryAppScope, read.Recovery)
			require.Equal(t, tt.scopes, read.MissingScopes)
			require.False(t, read.ProvenNoSideEffect)
			require.Empty(t, read.TerminalState)
			require.Equal(t, PublicCodeScopeRequired, read.PublicCode)

			notStartedWrite := classifier.ClassifyEnvelope(envelope, tt.expect, RiskWrite, false)
			require.Equal(t, RecoveryAppScope, notStartedWrite.Recovery)
			require.Equal(t, tt.scopes, notStartedWrite.MissingScopes)
			require.False(t, notStartedWrite.ProvenNoSideEffect)
			require.Empty(t, notStartedWrite.TerminalState)
		})
	}
}

func TestErrorClassifier_FixedConnectionAndReauthContracts(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	tests := []struct {
		name     string
		fixture  string
		recovery RecoveryKind
		public   string
	}{
		// The first two tuples were observed with an isolated lark-cli 1.0.68
		// HOME in this session. The fixtures contain no URL, app, or user IDs.
		{name: "observed cli not configured tuple", fixture: "fixed-connection-not-configured.json", recovery: RecoveryCreateApp, public: PublicCodeConnectionRequired},
		{name: "observed cli token missing tuple", fixture: "fixed-authentication-token-missing.json", recovery: RecoveryReauth, public: PublicCodeReauthRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifier.ClassifyEnvelope(loadErrorClassifierFixture(t, tt.fixture), docsCreateScopes(), RiskWrite, true)
			require.Equal(t, tt.recovery, got.Recovery)
			require.True(t, got.ProvenNoSideEffect)
			require.Empty(t, got.TerminalState)
			require.Equal(t, tt.public, got.PublicCode)
		})
	}
}

func TestErrorClassifier_SyntheticRefreshTokenContractsDoNotProveStartedWrites(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	for _, fixture := range []string{
		"fixed-refresh-token-invalid.json",
		"fixed-refresh-token-expired.json",
		"fixed-refresh-token-revoked.json",
	} {
		fixture := fixture
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			envelope := loadErrorClassifierFixture(t, fixture)

			startedWrite := classifier.ClassifyEnvelope(envelope, docsCreateScopes(), RiskWrite, true)
			require.Equal(t, RecoveryNone, startedWrite.Recovery)
			require.False(t, startedWrite.ProvenNoSideEffect)
			require.Equal(t, model.FeishuOperationUnknown, startedWrite.TerminalState)
			require.Equal(t, PublicCodeUnknownResult, startedWrite.PublicCode)
			startedHighRisk := classifier.ClassifyEnvelope(envelope, docsCreateScopes(), RiskHigh, true)
			require.Equal(t, RecoveryNone, startedHighRisk.Recovery)
			require.False(t, startedHighRisk.ProvenNoSideEffect)
			require.Equal(t, model.FeishuOperationUnknown, startedHighRisk.TerminalState)

			read := classifier.ClassifyEnvelope(envelope, docsReadScopes(), RiskRead, true)
			require.Equal(t, RecoveryReauth, read.Recovery)
			require.False(t, read.ProvenNoSideEffect)
			require.Empty(t, read.TerminalState)

			notStartedWrite := classifier.ClassifyEnvelope(envelope, docsCreateScopes(), RiskWrite, false)
			require.Equal(t, RecoveryReauth, notStartedWrite.Recovery)
			require.False(t, notStartedWrite.ProvenNoSideEffect)
			require.Empty(t, notStartedWrite.TerminalState)
		})
	}
}

func TestErrorClassifier_ResourceACLNeverRequestsOAuthOrReplay(t *testing.T) {
	t.Parallel()

	got := NewErrorClassifier().ClassifyEnvelope(loadErrorClassifierFixture(t, "fixed-resource-acl.json"), docsCreateScopes(), RiskWrite, true)
	require.Equal(t, RecoveryResourceACL, got.Recovery)
	require.Empty(t, got.MissingScopes)
	require.False(t, got.ProvenNoSideEffect)
	require.False(t, got.RetryRead)
	require.Equal(t, model.FeishuOperationFailed, got.TerminalState)
	require.Equal(t, PublicCodeResourceDenied, got.PublicCode)
}

func TestErrorClassifier_StructuredTransientReadRetriesButStartedWriteIsUnknown(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	for _, fixture := range []string{"fixed-rate-limit.json", "fixed-upstream-5xx.json"} {
		fixture := fixture
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			envelope := loadErrorClassifierFixture(t, fixture)

			read := classifier.ClassifyEnvelope(envelope, docsReadScopes(), RiskRead, true)
			require.Equal(t, RecoveryNone, read.Recovery)
			require.True(t, read.RetryRead)
			require.Empty(t, read.TerminalState)
			require.Contains(t, []string{PublicCodeRateLimited, PublicCodeTemporaryError}, read.PublicCode)

			write := classifier.ClassifyEnvelope(envelope, docsCreateScopes(), RiskWrite, true)
			require.Equal(t, RecoveryNone, write.Recovery)
			require.False(t, write.RetryRead)
			require.Equal(t, model.FeishuOperationUnknown, write.TerminalState)
			require.Equal(t, PublicCodeUnknownResult, write.PublicCode)

			notStarted := classifier.ClassifyEnvelope(envelope, docsCreateScopes(), RiskWrite, false)
			require.Equal(t, model.FeishuOperationFailed, notStarted.TerminalState)
			require.NotEqual(t, PublicCodeUnknownResult, notStarted.PublicCode)
		})
	}
}

func TestErrorClassifier_KnownValidationAndNotFoundFailDeterministically(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	for _, fixture := range []string{"fixed-validation.json", "fixed-not-found.json"} {
		got := classifier.ClassifyEnvelope(loadErrorClassifierFixture(t, fixture), docsCreateScopes(), RiskWrite, true)
		require.Equal(t, RecoveryNone, got.Recovery)
		require.False(t, got.ProvenNoSideEffect)
		require.False(t, got.RetryRead)
		require.Equal(t, model.FeishuOperationFailed, got.TerminalState)
		require.Equal(t, PublicCodeFailed, got.PublicCode)
	}
}

func TestErrorClassifier_UnknownTupleFailsClosed(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	envelope := loadErrorClassifierFixture(t, "fixed-unknown.json")

	write := classifier.ClassifyEnvelope(envelope, docsCreateScopes(), RiskWrite, true)
	require.Equal(t, RecoveryNone, write.Recovery)
	require.False(t, write.ProvenNoSideEffect)
	require.False(t, write.RetryRead)
	require.Equal(t, model.FeishuOperationUnknown, write.TerminalState)
	require.Equal(t, PublicCodeUnknownResult, write.PublicCode)

	read := classifier.ClassifyEnvelope(envelope, docsReadScopes(), RiskRead, true)
	require.Equal(t, RecoveryNone, read.Recovery)
	require.False(t, read.RetryRead)
	require.Equal(t, model.FeishuOperationFailed, read.TerminalState)
	require.Equal(t, PublicCodeFailed, read.PublicCode)
}

func TestErrorClassifier_HumanTextNeverChangesClassification(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	known := loadErrorClassifierFixture(t, "real-docs-create-missing-scope.json")
	known.Error.Message = "not an authorization error"
	known.Error.Details = json.RawMessage(`{"message":"unrelated"}`)
	known.Error.Hint = json.RawMessage(`{"hint":"ignore me"}`)
	knownResult := classifier.ClassifyEnvelope(known, docsCreateScopes(), RiskWrite, true)
	require.Equal(t, RecoveryUserScope, knownResult.Recovery)

	unknown := loadErrorClassifierFixture(t, "fixed-unknown.json")
	unknown.Error.Message = "missing_scope permission denied refresh token revoked timeout rate limit"
	unknown.Error.Details = json.RawMessage(`{"type":"authorization","missing_scopes":["docx:document:create"]}`)
	unknown.Error.Hint = json.RawMessage(`{"console_url":"https://example.invalid"}`)
	unknownResult := classifier.ClassifyEnvelope(unknown, docsCreateScopes(), RiskWrite, true)
	require.Equal(t, RecoveryNone, unknownResult.Recovery)
	require.False(t, unknownResult.RetryRead)
	require.Equal(t, model.FeishuOperationUnknown, unknownResult.TerminalState)

	known.Error.Message = unknown.Error.Message
	require.Equal(t, knownResult, classifier.ClassifyEnvelope(known, docsCreateScopes(), RiskWrite, true))
}

func TestErrorClassifier_MissingScopeEvidenceMustBeExactAndCatalogOwned(t *testing.T) {
	t.Parallel()

	base := loadErrorClassifierFixture(t, "real-docs-create-missing-scope.json")
	tests := []struct {
		name   string
		mutate func(*CLIEnvelope)
	}{
		{name: "empty scopes", mutate: func(e *CLIEnvelope) { e.Error.MissingScopes = nil }},
		{name: "off catalog im scope", mutate: func(e *CLIEnvelope) { e.Error.MissingScopes = []string{"im:message:send"} }},
		{name: "malformed scope", mutate: func(e *CLIEnvelope) { e.Error.MissingScopes = []string{" docx:document:create"} }},
		{name: "wrong identity", mutate: func(e *CLIEnvelope) { e.Identity = "bot" }},
		{name: "unknown code", mutate: func(e *CLIEnvelope) { e.Error.Code = json.RawMessage(`99991673`) }},
		{name: "object code", mutate: func(e *CLIEnvelope) { e.Error.Code = json.RawMessage(`{"value":99991672}`) }},
		{name: "array code", mutate: func(e *CLIEnvelope) { e.Error.Code = json.RawMessage(`[99991672]`) }},
		{name: "boolean code", mutate: func(e *CLIEnvelope) { e.Error.Code = json.RawMessage(`true`) }},
		{name: "null code", mutate: func(e *CLIEnvelope) { e.Error.Code = json.RawMessage(`null`) }},
		{name: "empty string code", mutate: func(e *CLIEnvelope) { e.Error.Code = json.RawMessage(`""`) }},
		{name: "malformed permission evidence", mutate: func(e *CLIEnvelope) { e.Error.PermissionViolations = json.RawMessage(`{"level":"app"}`) }},
		{name: "app evidence without console url", mutate: func(e *CLIEnvelope) { e.Error.PermissionViolations = json.RawMessage(`[{"level":"app"}]`) }},
		{name: "console url without app evidence", mutate: func(e *CLIEnvelope) { e.Error.ConsoleURL = "https://open.feishu.cn/sanitized" }},
	}
	classifier := NewErrorClassifier()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			envelope := cloneErrorClassifierEnvelope(t, base)
			tt.mutate(envelope)
			got := classifier.ClassifyEnvelope(envelope, docsCreateScopes(), RiskWrite, true)
			require.Equal(t, RecoveryNone, got.Recovery)
			require.Empty(t, got.MissingScopes)
			require.False(t, got.ProvenNoSideEffect)
			require.Equal(t, model.FeishuOperationUnknown, got.TerminalState)
			require.Equal(t, PublicCodeUnknownResult, got.PublicCode)
		})
	}
}

func TestErrorClassifier_ReturnedDuplicateScopesFailClosed(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	t.Run("observed docs tuple cannot prove a duplicated response", func(t *testing.T) {
		t.Parallel()
		envelope := loadErrorClassifierFixture(t, "real-docs-create-missing-scope.json")
		envelope.Error.MissingScopes = append(envelope.Error.MissingScopes, "docx:document:create")

		got := classifier.ClassifyEnvelope(envelope, docsCreateScopes(), RiskWrite, true)
		require.Equal(t, RecoveryNone, got.Recovery)
		require.Empty(t, got.MissingScopes)
		require.False(t, got.ProvenNoSideEffect)
		require.Equal(t, model.FeishuOperationUnknown, got.TerminalState)
		require.Equal(t, PublicCodeUnknownResult, got.PublicCode)
	})

	t.Run("duplicate returned scope is invalid for any recovery", func(t *testing.T) {
		t.Parallel()
		envelope := loadErrorClassifierFixture(t, "fixed-base-app-scope-missing.json")
		envelope.Error.MissingScopes = append(envelope.Error.MissingScopes, "base:app:create")

		got := classifier.ClassifyEnvelope(envelope, baseCreateScopes(), RiskRead, true)
		require.Equal(t, RecoveryNone, got.Recovery)
		require.Empty(t, got.MissingScopes)
		require.False(t, got.ProvenNoSideEffect)
		require.False(t, got.RetryRead)
		require.Equal(t, model.FeishuOperationFailed, got.TerminalState)
		require.Equal(t, PublicCodeFailed, got.PublicCode)
	})
}

func TestErrorClassifier_MissingScopesMustBelongToCurrentCommand(t *testing.T) {
	t.Parallel()

	base := loadErrorClassifierFixture(t, "real-docs-create-missing-scope.json")
	tests := []struct {
		name           string
		returnedScopes []string
		expectedScopes []string
	}{
		{
			name:           "cross domain base scope for docs command",
			returnedScopes: []string{"base:record:create"},
			expectedScopes: docsCreateScopes(),
		},
		{
			name:           "same domain different docs command scope",
			returnedScopes: []string{"docx:document:readonly"},
			expectedScopes: docsCreateScopes(),
		},
		{
			name:           "nil expected scopes",
			returnedScopes: []string{"docx:document:create"},
			expectedScopes: nil,
		},
		{
			name:           "empty expected scopes",
			returnedScopes: []string{"docx:document:create"},
			expectedScopes: []string{},
		},
		{
			name:           "off catalog expected scope",
			returnedScopes: []string{"docx:document:create"},
			expectedScopes: []string{"im:message:send"},
		},
		{
			name:           "mixed command expected scope set",
			returnedScopes: []string{"docx:document:create"},
			expectedScopes: []string{"docx:document:create", "base:record:create"},
		},
		{
			name:           "duplicate expected scope",
			returnedScopes: []string{"docx:document:create"},
			expectedScopes: []string{"docx:document:create", "docx:document:create"},
		},
		{
			name:           "malformed expected scope",
			returnedScopes: []string{"docx:document:create"},
			expectedScopes: []string{" docx:document:create"},
		},
	}
	classifier := NewErrorClassifier()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			envelope := cloneErrorClassifierEnvelope(t, base)
			envelope.Error.MissingScopes = append([]string(nil), tt.returnedScopes...)
			got := classifier.ClassifyEnvelope(envelope, tt.expectedScopes, RiskWrite, true)
			require.Equal(t, RecoveryNone, got.Recovery)
			require.Empty(t, got.MissingScopes)
			require.False(t, got.ProvenNoSideEffect)
			require.Equal(t, model.FeishuOperationUnknown, got.TerminalState)
			require.Equal(t, PublicCodeUnknownResult, got.PublicCode)
		})
	}
}

func TestErrorClassifier_CodeCanonicalizationIsNarrow(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	stringCode := loadErrorClassifierFixture(t, "real-docs-create-missing-scope.json")
	numberCode := cloneErrorClassifierEnvelope(t, stringCode)
	numberCode.Error.Code = json.RawMessage(`99991672`)
	require.Equal(
		t,
		classifier.ClassifyEnvelope(stringCode, docsCreateScopes(), RiskWrite, true),
		classifier.ClassifyEnvelope(numberCode, docsCreateScopes(), RiskWrite, true),
	)

	exponent := cloneErrorClassifierEnvelope(t, stringCode)
	exponent.Error.Code = json.RawMessage(`9.9991672e7`)
	got := classifier.ClassifyEnvelope(exponent, docsCreateScopes(), RiskWrite, true)
	require.Equal(t, RecoveryNone, got.Recovery)
	require.Equal(t, model.FeishuOperationUnknown, got.TerminalState)
}

func TestErrorClassifier_EnvelopeIdentityIsOuterAndConflictsFailClosed(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	envelope := loadErrorClassifierFixture(t, "real-docs-create-missing-scope.json")
	envelope.Error.Identity = "user"
	require.Equal(t, RecoveryUserScope, classifier.ClassifyEnvelope(envelope, docsCreateScopes(), RiskWrite, true).Recovery)

	conflicting := cloneErrorClassifierEnvelope(t, envelope)
	conflicting.Error.Identity = "bot"
	got := classifier.ClassifyEnvelope(conflicting, docsCreateScopes(), RiskWrite, true)
	require.Equal(t, RecoveryNone, got.Recovery)
	require.Equal(t, model.FeishuOperationUnknown, got.TerminalState)

	missingOuter := cloneErrorClassifierEnvelope(t, envelope)
	missingOuter.Identity = ""
	require.Equal(t, RecoveryUserScope, classifier.ClassifyEnvelope(missingOuter, docsCreateScopes(), RiskWrite, true).Recovery)
}

func TestErrorClassifier_InvalidEnvelopeFailsClosed(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	for _, envelope := range []*CLIEnvelope{
		nil,
		{OK: true, Identity: "user", Error: loadErrorClassifierFixture(t, "real-docs-create-missing-scope.json").Error},
		{OK: false, Identity: "user", Error: nil},
	} {
		got := classifier.ClassifyEnvelope(envelope, docsCreateScopes(), RiskWrite, true)
		require.Equal(t, RecoveryNone, got.Recovery)
		require.Equal(t, model.FeishuOperationUnknown, got.TerminalState)
	}

	got := classifier.Classify(nil, docsReadScopes(), RiskRead, true)
	require.Equal(t, RecoveryNone, got.Recovery)
	require.Equal(t, model.FeishuOperationFailed, got.TerminalState)
}

func TestErrorClassifier_TransportClassificationUsesErrorTypes(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	tests := []struct {
		name     string
		err      error
		risk     RiskLevel
		started  bool
		retry    bool
		terminal string
		public   string
	}{
		{name: "deadline read retries", err: context.DeadlineExceeded, risk: RiskRead, started: true, retry: true, public: PublicCodeTemporaryError},
		{name: "wrapped network read retries", err: fmt.Errorf("wrapped: %w", classifierNetError{}), risk: RiskRead, started: true, retry: true, public: PublicCodeTemporaryError},
		{name: "permanent network error fails closed", err: classifierPermanentNetError{}, risk: RiskRead, started: true, terminal: model.FeishuOperationFailed, public: PublicCodeFailed},
		{name: "wrapped permanent network error fails closed", err: fmt.Errorf("wrapped: %w", classifierPermanentNetError{}), risk: RiskRead, started: true, terminal: model.FeishuOperationFailed, public: PublicCodeFailed},
		{name: "deadline started write unknown", err: context.DeadlineExceeded, risk: RiskWrite, started: true, terminal: model.FeishuOperationUnknown, public: PublicCodeUnknownResult},
		{name: "network started high risk unknown", err: classifierNetError{}, risk: RiskHigh, started: true, terminal: model.FeishuOperationUnknown, public: PublicCodeUnknownResult},
		{name: "deadline write not started failed", err: context.DeadlineExceeded, risk: RiskWrite, started: false, terminal: model.FeishuOperationFailed, public: PublicCodeTemporaryError},
		{name: "cancelled read stays cancelled", err: context.Canceled, risk: RiskRead, started: true, terminal: model.FeishuOperationCancelled, public: PublicCodeCancelled},
		{name: "cancelled started write unknown", err: context.Canceled, risk: RiskWrite, started: true, terminal: model.FeishuOperationUnknown, public: PublicCodeUnknownResult},
		{name: "arbitrary timeout message is not transient", err: errors.New("network timeout missing_scope"), risk: RiskRead, started: true, terminal: model.FeishuOperationFailed, public: PublicCodeFailed},
		{name: "nil is malformed", err: nil, risk: RiskRead, started: true, terminal: model.FeishuOperationFailed, public: PublicCodeFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifier.ClassifyTransport(tt.err, tt.risk, tt.started)
			require.Equal(t, RecoveryNone, got.Recovery)
			require.Equal(t, tt.retry, got.RetryRead)
			require.Equal(t, tt.terminal, got.TerminalState)
			require.Equal(t, tt.public, got.PublicCode)
		})
	}
}

func TestErrorClassifier_ReturnsDefensiveCopiesAndIsConcurrentSafe(t *testing.T) {
	t.Parallel()

	classifier := NewErrorClassifier()
	envelope := loadErrorClassifierFixture(t, "fixed-base-app-scope-missing.json")
	want := classifier.ClassifyEnvelope(envelope, baseCreateScopes(), RiskRead, true)
	want.MissingScopes[0] = "im:message:send"
	again := classifier.ClassifyEnvelope(envelope, baseCreateScopes(), RiskRead, true)
	require.Equal(t, "base:app:create", again.MissingScopes[0])

	const workers = 64
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			got := classifier.ClassifyEnvelope(envelope, baseCreateScopes(), RiskRead, true)
			require.Equal(t, RecoveryAppScope, got.Recovery)
			got.MissingScopes[0] = "mutated"
		}()
	}
	wg.Wait()
	require.Equal(t, "base:app:create", classifier.ClassifyEnvelope(envelope, baseCreateScopes(), RiskRead, true).MissingScopes[0])
}

func loadErrorClassifierFixture(t *testing.T, name string) *CLIEnvelope {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..", ".ndf", "features", "feishu-personal-workspace", "fixtures", name)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var fixture classifierFixture
	require.NoError(t, decoder.Decode(&fixture))
	require.False(t, fixture.OK)
	require.NotEmpty(t, fixture.Type)
	require.NotEmpty(t, fixture.Subtype)
	require.NotEmpty(t, fixture.Identity)

	cliErr := &CLIError{
		Type:                 fixture.Type,
		Subtype:              fixture.Subtype,
		Code:                 append(json.RawMessage(nil), fixture.Code...),
		MissingScopes:        append([]string(nil), fixture.MissingScopes...),
		PermissionViolations: append(json.RawMessage(nil), fixture.PermissionViolations...),
	}
	if fixture.ConsoleURLPresent {
		cliErr.ConsoleURL = "https://open.feishu.cn/sanitized"
	}
	return &CLIEnvelope{OK: fixture.OK, Identity: fixture.Identity, Error: cliErr}
}

func cloneErrorClassifierEnvelope(t *testing.T, source *CLIEnvelope) *CLIEnvelope {
	t.Helper()
	require.NotNil(t, source)
	require.NotNil(t, source.Error)
	clone := *source
	errClone := *source.Error
	errClone.Code = append(json.RawMessage(nil), source.Error.Code...)
	errClone.MissingScopes = append([]string(nil), source.Error.MissingScopes...)
	errClone.PermissionViolations = append(json.RawMessage(nil), source.Error.PermissionViolations...)
	errClone.Details = append(json.RawMessage(nil), source.Error.Details...)
	errClone.Hint = append(json.RawMessage(nil), source.Error.Hint...)
	clone.Error = &errClone
	return &clone
}

type classifierNetError struct{}

func (classifierNetError) Error() string   { return "classified network failure" }
func (classifierNetError) Timeout() bool   { return true }
func (classifierNetError) Temporary() bool { return true }

type classifierPermanentNetError struct{}

func (classifierPermanentNetError) Error() string   { return "permanent classified network failure" }
func (classifierPermanentNetError) Timeout() bool   { return false }
func (classifierPermanentNetError) Temporary() bool { return false }

func docsCreateScopes() []string {
	return []string{"docx:document:create"}
}

func docsReadScopes() []string {
	return []string{"docx:document:readonly"}
}

func baseCreateScopes() []string {
	return []string{
		"base:app:create",
		"base:table:read",
		"base:table:create",
		"base:table:update",
		"base:table:delete",
	}
}

func wikiSpaceCreateScopes() []string {
	return []string{"wiki:space:write_only"}
}
