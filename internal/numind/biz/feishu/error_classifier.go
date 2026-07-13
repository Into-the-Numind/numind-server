package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sort"
	"strings"

	"numind-server/internal/pkg/model"
)

// RecoveryKind is a server-owned recovery action. It is never inferred from
// human-readable CLI output.
type RecoveryKind string

const (
	RecoveryNone        RecoveryKind = "none"
	RecoveryCreateApp   RecoveryKind = "create_app"
	RecoveryAppScope    RecoveryKind = "app_scope"
	RecoveryUserScope   RecoveryKind = "user_scope"
	RecoveryReauth      RecoveryKind = "reauth"
	RecoveryResourceACL RecoveryKind = "resource_acl"
)

// Public result codes are deliberately small, stable, and non-sensitive. Raw
// CLI types, codes, messages, hints, and resource details must not be copied to
// a public code.
const (
	PublicCodeConnectionRequired = "feishu_connection_required"
	PublicCodeScopeRequired      = "feishu_scope_required"
	PublicCodeReauthRequired     = "feishu_reauth_required"
	PublicCodeResourceDenied     = "feishu_resource_denied"
	PublicCodeRateLimited        = "feishu_rate_limited"
	PublicCodeTemporaryError     = "feishu_temporary_error"
	PublicCodeUnknownResult      = "feishu_unknown_result"
	PublicCodeFailed             = "feishu_operation_failed"
	PublicCodeCancelled          = "feishu_operation_cancelled"
)

// Classification is the safe decision consumed by the operation state
// machine. MissingScopes is always a fresh slice.
type Classification struct {
	Recovery           RecoveryKind
	MissingScopes      []string
	ProvenNoSideEffect bool
	RetryRead          bool
	TerminalState      string
	PublicCode         string
}

type classifierSemantic uint8

const (
	semanticCreateApp classifierSemantic = iota + 1
	semanticMissingScope
	semanticAppScope
	semanticReauth
	semanticResourceACL
	semanticRateLimit
	semanticUpstream
	semanticValidation
	semanticNotFound
)

type classifierTuple struct {
	typeName string
	subtype  string
	code     string
	hasCode  bool
}

// ErrorClassifier is immutable after construction and safe for concurrent
// use. It is intentionally stateless: Task 7 owns recovery-loop limits.
type ErrorClassifier struct {
	allowedScopes map[string]struct{}
	tuples        map[classifierTuple]classifierSemantic
}

// NewErrorClassifier builds a fixed lark-cli 1.0.68 contract classifier. Scope
// validation is derived from the exact Docs/Base/Wiki command catalog rather
// than from a model-supplied declaration.
func NewErrorClassifier() *ErrorClassifier {
	allowedScopes := make(map[string]struct{})
	for _, command := range NewCommandCatalog().manifest().Commands {
		for _, scope := range command.Scopes {
			allowedScopes[scope] = struct{}{}
		}
	}

	tuples := make(map[classifierTuple]classifierSemantic)
	addTuple := func(typeName, subtype, code string, hasCode bool, semantic classifierSemantic) {
		tuples[classifierTuple{typeName: typeName, subtype: subtype, code: code, hasCode: hasCode}] = semantic
	}

	// The missing-scope tuple and config/not_configured shape are backed by
	// sanitized real 1.0.68 observations. The remaining tuples are explicit,
	// checked-in fixed contracts and must not be broadened by text matching.
	addTuple("authorization", "missing_scope", "99991672", true, semanticMissingScope)
	addTuple("config", "not_configured", "", false, semanticCreateApp)
	addTuple("authentication", "token_missing", "", false, semanticReauth)
	addTuple("authorization", "refresh_token_invalid", "", false, semanticReauth)
	addTuple("authorization", "refresh_token_expired", "", false, semanticReauth)
	addTuple("authorization", "refresh_token_revoked", "", false, semanticReauth)
	addTuple("authorization", "app_scope_not_applied", "", false, semanticAppScope)
	addTuple("api", "permission_denied", "RESOURCE_ACCESS_DENIED", true, semanticResourceACL)
	addTuple("api", "rate_limited", "429", true, semanticRateLimit)
	addTuple("api", "upstream_error", "503", true, semanticUpstream)
	addTuple("api", "validation_error", "400", true, semanticValidation)
	addTuple("api", "not_found", "404", true, semanticNotFound)

	return &ErrorClassifier{allowedScopes: allowedScopes, tuples: tuples}
}

// ClassifyEnvelope classifies a failed, complete CLI envelope. The outer
// identity is authoritative in lark-cli 1.0.68. An inner compatibility identity
// is accepted only when it agrees or the outer identity is absent.
func (c *ErrorClassifier) ClassifyEnvelope(envelope *CLIEnvelope, risk RiskLevel, invocationStarted bool) Classification {
	if envelope == nil || envelope.OK || envelope.Error == nil {
		return failClosedClassification(risk, invocationStarted)
	}
	cliErr := *envelope.Error
	if envelope.Identity != "" && cliErr.Identity != "" && envelope.Identity != cliErr.Identity {
		return failClosedClassification(risk, invocationStarted)
	}
	if envelope.Identity != "" {
		cliErr.Identity = envelope.Identity
	}
	return c.Classify(&cliErr, risk, invocationStarted)
}

// Classify uses only fixed structured fields. Message, Details, Hint, stderr,
// and all human-readable text are intentionally never read.
func (c *ErrorClassifier) Classify(cliErr *CLIError, risk RiskLevel, invocationStarted bool) Classification {
	if c == nil || cliErr == nil || cliErr.Identity != "user" {
		return failClosedClassification(risk, invocationStarted)
	}
	code, hasCode, validCode := normalizeClassifierCode(cliErr.Code)
	if !validCode {
		return failClosedClassification(risk, invocationStarted)
	}
	semantic, ok := c.tuples[classifierTuple{
		typeName: cliErr.Type,
		subtype:  cliErr.Subtype,
		code:     code,
		hasCode:  hasCode,
	}]
	if !ok {
		return failClosedClassification(risk, invocationStarted)
	}

	evidence, validEvidence := parsePermissionEvidence(cliErr.PermissionViolations, cliErr.ConsoleURL != "")
	if !validEvidence {
		return failClosedClassification(risk, invocationStarted)
	}

	switch semantic {
	case semanticMissingScope:
		scopes, valid := c.normalizeMissingScopes(cliErr.MissingScopes)
		if !valid {
			return failClosedClassification(risk, invocationStarted)
		}
		recovery := RecoveryUserScope
		if evidence == permissionEvidenceApp {
			recovery = RecoveryAppScope
		}
		return Classification{
			Recovery:           recovery,
			MissingScopes:      scopes,
			ProvenNoSideEffect: true,
			PublicCode:         PublicCodeScopeRequired,
		}
	case semanticAppScope:
		scopes, valid := c.normalizeMissingScopes(cliErr.MissingScopes)
		if !valid || evidence != permissionEvidenceApp {
			return failClosedClassification(risk, invocationStarted)
		}
		return Classification{
			Recovery:           RecoveryAppScope,
			MissingScopes:      scopes,
			ProvenNoSideEffect: true,
			PublicCode:         PublicCodeScopeRequired,
		}
	case semanticCreateApp:
		if !emptyClassifierEvidence(cliErr, evidence) {
			return failClosedClassification(risk, invocationStarted)
		}
		return Classification{
			Recovery:           RecoveryCreateApp,
			ProvenNoSideEffect: true,
			PublicCode:         PublicCodeConnectionRequired,
		}
	case semanticReauth:
		if !emptyClassifierEvidence(cliErr, evidence) {
			return failClosedClassification(risk, invocationStarted)
		}
		return Classification{
			Recovery:           RecoveryReauth,
			ProvenNoSideEffect: true,
			PublicCode:         PublicCodeReauthRequired,
		}
	case semanticResourceACL:
		if !emptyClassifierEvidence(cliErr, evidence) {
			return failClosedClassification(risk, invocationStarted)
		}
		return Classification{
			Recovery:      RecoveryResourceACL,
			TerminalState: model.FeishuOperationFailed,
			PublicCode:    PublicCodeResourceDenied,
		}
	case semanticRateLimit:
		if !emptyClassifierEvidence(cliErr, evidence) {
			return failClosedClassification(risk, invocationStarted)
		}
		return transientClassification(risk, invocationStarted, PublicCodeRateLimited)
	case semanticUpstream:
		if !emptyClassifierEvidence(cliErr, evidence) {
			return failClosedClassification(risk, invocationStarted)
		}
		return transientClassification(risk, invocationStarted, PublicCodeTemporaryError)
	case semanticValidation, semanticNotFound:
		if !emptyClassifierEvidence(cliErr, evidence) {
			return failClosedClassification(risk, invocationStarted)
		}
		return Classification{
			Recovery:           RecoveryNone,
			ProvenNoSideEffect: true,
			TerminalState:      model.FeishuOperationFailed,
			PublicCode:         PublicCodeFailed,
		}
	default:
		return failClosedClassification(risk, invocationStarted)
	}
}

// ClassifyTransport recognizes cancellation/deadline and net.Error using
// errors.Is/errors.As only. Arbitrary error text is never interpreted.
func (c *ErrorClassifier) ClassifyTransport(err error, risk RiskLevel, invocationStarted bool) Classification {
	if errors.Is(err, context.Canceled) {
		if invocationStarted && writeLikeRisk(risk) {
			return unknownResultClassification()
		}
		return Classification{
			Recovery:      RecoveryNone,
			TerminalState: model.FeishuOperationCancelled,
			PublicCode:    PublicCodeCancelled,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return transientClassification(risk, invocationStarted, PublicCodeTemporaryError)
	}
	var networkError net.Error
	if err != nil && errors.As(err, &networkError) {
		return transientClassification(risk, invocationStarted, PublicCodeTemporaryError)
	}
	return failClosedClassification(risk, invocationStarted)
}

func (c *ErrorClassifier) normalizeMissingScopes(scopes []string) ([]string, bool) {
	if c == nil || len(scopes) == 0 {
		return nil, false
	}
	unique := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		if scope == "" || strings.TrimSpace(scope) != scope || strings.IndexByte(scope, 0) >= 0 {
			return nil, false
		}
		if _, allowed := c.allowedScopes[scope]; !allowed {
			return nil, false
		}
		unique[scope] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for scope := range unique {
		normalized = append(normalized, scope)
	}
	sort.Strings(normalized)
	return normalized, true
}

type permissionEvidence uint8

const (
	permissionEvidenceNone permissionEvidence = iota
	permissionEvidenceApp
)

type permissionViolation struct {
	Level string `json:"level"`
}

func parsePermissionEvidence(raw json.RawMessage, consoleURLPresent bool) (permissionEvidence, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		if consoleURLPresent {
			return permissionEvidenceNone, false
		}
		return permissionEvidenceNone, true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var violations []permissionViolation
	if err := decoder.Decode(&violations); err != nil || violations == nil {
		return permissionEvidenceNone, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return permissionEvidenceNone, false
	}
	hasApp := false
	for _, violation := range violations {
		switch violation.Level {
		case "user":
		case "app":
			hasApp = true
		default:
			return permissionEvidenceNone, false
		}
	}
	if hasApp != consoleURLPresent {
		return permissionEvidenceNone, false
	}
	if hasApp {
		return permissionEvidenceApp, true
	}
	return permissionEvidenceNone, true
}

func emptyClassifierEvidence(cliErr *CLIError, evidence permissionEvidence) bool {
	return len(cliErr.MissingScopes) == 0 && evidence == permissionEvidenceNone && cliErr.ConsoleURL == ""
}

func normalizeClassifierCode(raw json.RawMessage) (code string, present bool, valid bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", false, true
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", false, false
	}
	switch typed := value.(type) {
	case string:
		if !validClassifierCodeString(typed) {
			return "", false, false
		}
		return typed, true, true
	case json.Number:
		text := typed.String()
		if !allASCIIDigits(text) {
			return "", false, false
		}
		return text, true, true
	default:
		return "", false, false
	}
}

func validClassifierCodeString(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

func allASCIIDigits(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func transientClassification(risk RiskLevel, invocationStarted bool, publicCode string) Classification {
	if risk == RiskRead {
		return Classification{
			Recovery:   RecoveryNone,
			RetryRead:  true,
			PublicCode: publicCode,
		}
	}
	if invocationStarted && writeLikeRisk(risk) {
		return unknownResultClassification()
	}
	return Classification{
		Recovery:      RecoveryNone,
		TerminalState: model.FeishuOperationFailed,
		PublicCode:    publicCode,
	}
}

func failClosedClassification(risk RiskLevel, invocationStarted bool) Classification {
	if invocationStarted && writeLikeRisk(risk) {
		return unknownResultClassification()
	}
	return Classification{
		Recovery:      RecoveryNone,
		TerminalState: model.FeishuOperationFailed,
		PublicCode:    PublicCodeFailed,
	}
}

func unknownResultClassification() Classification {
	return Classification{
		Recovery:      RecoveryNone,
		TerminalState: model.FeishuOperationUnknown,
		PublicCode:    PublicCodeUnknownResult,
	}
}

func writeLikeRisk(risk RiskLevel) bool {
	return risk != RiskRead
}
