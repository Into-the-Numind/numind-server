package feishu

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"numind-server/internal/pkg/model"
)

// OperationFailure is the complete non-sensitive terminal failure contract
// exposed to an Agent. It is built only from server-owned public codes and
// catalog scopes.
type OperationFailure struct {
	Code            string   `json:"code"`
	Category        string   `json:"category"`
	Retryable       bool     `json:"retryable"`
	BusinessStarted bool     `json:"business_started"`
	RequiredScopes  []string `json:"required_scopes,omitempty"`
	// WriteFenceKey is an opaque server-generated digest for the exact
	// normalized write command whose outcome is unknown. It contains no argv,
	// resource token, content, credential, or user identity and is accepted only
	// on a business-started unknown_result. Agent continuation uses it to retain
	// the narrow fence without falling back to a run-wide stop.
	WriteFenceKey string `json:"write_fence_key,omitempty"`
}

type larkToolResultEnvelope struct {
	OK          bool              `json:"ok"`
	State       string            `json:"state"`
	OperationID string            `json:"operation_id"`
	Data        json.RawMessage   `json:"data,omitempty"`
	Failure     *OperationFailure `json:"failure,omitempty"`
}

type publicFailureSemantic struct {
	category string
	retry    bool
}

var publicFailureSemantics = map[string]publicFailureSemantic{
	PublicCodeConnectionRequired: {category: "connection_required"},
	PublicCodeScopeRequired:      {category: "scope_required"},
	PublicCodeReauthRequired:     {category: "reauth_required"},
	PublicCodeValidationError:    {category: "validation"},
	PublicCodeNotFound:           {category: "not_found"},
	PublicCodeResourceDenied:     {category: "resource_denied"},
	PublicCodeRateLimited:        {category: "rate_limited", retry: true},
	PublicCodeTemporaryError:     {category: "temporary", retry: true},
	PublicCodeUnknownResult:      {category: "unknown_result"},
	PublicCodeFailed:             {category: "failed"},
	PublicCodeCancelled:          {category: "cancelled"},
}

func newOperationFailure(
	code, state string,
	businessStarted bool,
	risk RiskLevel,
	requiredScopes []string,
) *OperationFailure {
	semantic, ok := publicFailureSemantics[code]
	if !ok {
		semantic = publicFailureSemantics[PublicCodeFailed]
		code = PublicCodeFailed
	}
	retryable := semantic.retry && (!businessStarted || risk == RiskRead)
	if state == model.FeishuOperationUnknown || state == model.FeishuOperationCancelled {
		retryable = false
	}
	return &OperationFailure{
		Code: code, Category: semantic.category, Retryable: retryable,
		BusinessStarted: businessStarted,
		RequiredScopes:  normalizePublicFailureScopes(requiredScopes),
	}
}

func normalizePublicFailureScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return nil
	}
	allowed := make(map[string]struct{})
	for _, command := range NewCommandCatalog().manifest().Commands {
		for _, scope := range command.Scopes {
			allowed[scope] = struct{}{}
		}
	}
	unique := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		if _, ok := allowed[scope]; !ok {
			return nil
		}
		if _, duplicate := unique[scope]; duplicate {
			return nil
		}
		unique[scope] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for scope := range unique {
		result = append(result, scope)
	}
	sort.Strings(result)
	return result
}

// MarshalLarkToolResult is the only serializer for live and resumed terminal
// lark_execute results. Waiting actions, internal identities and raw CLI fields
// are intentionally outside this schema.
func MarshalLarkToolResult(result *OperationResult) (json.RawMessage, error) {
	if result == nil || !validStableIdentifier(result.OperationID, operationMaxOperationIDBytes) {
		return nil, errors.New("feishu tool result rejected")
	}
	envelope := larkToolResultEnvelope{State: result.State, OperationID: result.OperationID}
	switch result.State {
	case model.FeishuOperationSucceeded:
		if result.Failure != nil || len(result.Data) == 0 || !json.Valid(result.Data) {
			return nil, errors.New("feishu tool success result rejected")
		}
		envelope.OK = true
		envelope.Data = append(json.RawMessage(nil), result.Data...)
	case model.FeishuOperationFailed, model.FeishuOperationUnknown, model.FeishuOperationCancelled:
		if len(result.Data) != 0 || !validOperationFailure(result.State, result.Failure) {
			return nil, errors.New("feishu tool failure result rejected")
		}
		failure := *result.Failure
		failure.RequiredScopes = append([]string(nil), result.Failure.RequiredScopes...)
		envelope.Failure = &failure
	default:
		return nil, errors.New("feishu tool non-terminal result rejected")
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, errors.New("feishu tool result encoding failed")
	}
	return json.RawMessage(encoded), nil
}

// DecodeLarkTerminalFailure validates a durable external tool result using the
// same closed schema and semantics as MarshalLarkToolResult. Agent continuation
// guards use only this redacted evidence; arbitrary external-tool JSON cannot
// arm or relax Feishu retry state.
func DecodeLarkTerminalFailure(raw json.RawMessage) (*OperationFailure, bool) {
	if len(raw) == 0 || len(raw) > 16*1024 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope larkToolResultEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false
	}
	if envelope.OK || len(envelope.Data) != 0 ||
		!validStableIdentifier(envelope.OperationID, operationMaxOperationIDBytes) ||
		!validOperationFailure(envelope.State, envelope.Failure) {
		return nil, false
	}
	switch envelope.State {
	case model.FeishuOperationFailed, model.FeishuOperationUnknown, model.FeishuOperationCancelled:
		failure := *envelope.Failure
		failure.RequiredScopes = append([]string(nil), envelope.Failure.RequiredScopes...)
		return &failure, true
	default:
		return nil, false
	}
}

func validOperationFailure(state string, failure *OperationFailure) bool {
	if failure == nil {
		return false
	}
	semantic, ok := publicFailureSemantics[failure.Code]
	if !ok || failure.Category != semantic.category {
		return false
	}
	if failure.Retryable && !semantic.retry {
		return false
	}
	if failure.Retryable && failure.BusinessStarted && failure.Code != PublicCodeRateLimited && failure.Code != PublicCodeTemporaryError {
		return false
	}
	if state == model.FeishuOperationUnknown && failure.Code != PublicCodeUnknownResult {
		return false
	}
	if state == model.FeishuOperationCancelled && failure.Code != PublicCodeCancelled {
		return false
	}
	if state == model.FeishuOperationFailed && (failure.Code == PublicCodeUnknownResult || failure.Code == PublicCodeCancelled) {
		return false
	}
	if failure.WriteFenceKey != "" {
		if state != model.FeishuOperationUnknown || failure.Code != PublicCodeUnknownResult ||
			!failure.BusinessStarted || !validWriteFenceKey(failure.WriteFenceKey) {
			return false
		}
	}
	normalized := normalizePublicFailureScopes(failure.RequiredScopes)
	if len(normalized) != len(failure.RequiredScopes) {
		return false
	}
	for index := range normalized {
		if normalized[index] != failure.RequiredScopes[index] {
			return false
		}
	}
	return true
}

func validWriteFenceKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := range value {
		char := value[index]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
