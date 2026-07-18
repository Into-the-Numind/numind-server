package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// ScopeCheckResult is the fixed-version, caller-safe partition returned by
// lark-cli auth check. It never contains CLI diagnostics or suggestions.
type ScopeCheckResult struct {
	Granted []string
	Missing []string
}

// ScopePreflight checks the current user grant without invoking a business
// command. Implementations must fail closed when the result is ambiguous.
type ScopePreflight interface {
	Check(context.Context, string, []string) (*ScopeCheckResult, error)
}

// ControlledScopePreflight adapts the fixed lark-cli 1.0.68 auth-check
// contract. The accepted scope universe and exact scope sets are derived from
// the server-owned command catalog, never from model input.
type ControlledScopePreflight struct {
	runner            *ControlledLarkCLIRunner
	allowedScopes     map[string]struct{}
	expectedScopeSets map[string]struct{}
}

// NewControlledScopePreflight creates a strict scope checker for the pinned
// CLI. A nil runner is retained and rejected by Check so construction remains
// side-effect free and easy to wire through dependency factories.
func NewControlledScopePreflight(runner *ControlledLarkCLIRunner) *ControlledScopePreflight {
	manifest := NewCommandCatalog().manifest()
	allowedScopes := make(map[string]struct{})
	for _, command := range manifest.Commands {
		for _, scope := range command.Scopes {
			allowedScopes[scope] = struct{}{}
		}
	}
	expectedScopeSets := make(map[string]struct{})
	for _, command := range manifest.Commands {
		normalized, valid := normalizeExpectedScopeSet(command.Scopes, allowedScopes)
		if valid {
			expectedScopeSets[scopeSetKey(normalized)] = struct{}{}
		}
	}
	return &ControlledScopePreflight{
		runner:            runner,
		allowedScopes:     allowedScopes,
		expectedScopeSets: expectedScopeSets,
	}
}

// Check executes only `auth check` and accepts exactly the observed 1.0.68 JSON
// contract. Exit 0 means every requested scope is granted; exit 1 means at
// least one requested scope is missing. Every other shape is rejected.
func (p *ControlledScopePreflight) Check(
	ctx context.Context,
	home string,
	scopes []string,
) (*ScopeCheckResult, error) {
	if p == nil || p.runner == nil || ctx == nil {
		return nil, fmt.Errorf("feishu: scope preflight unavailable: %w", errControlledCLIInvalidInput)
	}
	if err := validateControlledCLIHome(home); err != nil {
		return nil, err
	}
	normalized, valid := normalizeExpectedScopeSet(scopes, p.allowedScopes)
	if !valid {
		return nil, fmt.Errorf("feishu: scope preflight scope set rejected: %w", errControlledCLIInvalidInput)
	}
	if _, registered := p.expectedScopeSets[scopeSetKey(normalized)]; !registered {
		return nil, fmt.Errorf("feishu: scope preflight scope set is not registered: %w", errControlledCLIInvalidInput)
	}

	binary, err := p.runner.binaryPath()
	if err != nil {
		return nil, err
	}
	argv := []string{"auth", "check", "--scope", strings.Join(normalized, " "), "--json"}
	if err := validateControlledCLIInput(argv, nil); err != nil {
		return nil, err
	}
	timeout := ControlledLarkCLIVersionTimeout
	if p.runner.timeout > 0 && p.runner.timeout <= ControlledLarkCLIVersionTimeout {
		timeout = p.runner.timeout
	}
	result, waitErr, processErr := p.runner.runProcess(ctx, binary, argv, nil, home, timeout)
	if processErr != nil {
		return nil, fmt.Errorf("feishu: scope preflight process failed: %w", processErr)
	}
	if result == nil || result.StdoutTruncated || result.StderrTruncated {
		return nil, fmt.Errorf("feishu: scope preflight output rejected: %w", errControlledCLIOutputLimit)
	}
	if len(result.Stderr) != 0 {
		return nil, fmt.Errorf("feishu: scope preflight diagnostic output rejected: %w", errControlledCLIInvalidJSON)
	}
	if result.ExitCode != 0 && result.ExitCode != 1 {
		return nil, fmt.Errorf("feishu: scope preflight exit code rejected: %w", errControlledCLIInvalidJSON)
	}
	if result.ExitCode == 0 && waitErr != nil {
		return nil, fmt.Errorf("feishu: scope preflight success process mismatch: %w", errControlledCLIInvalidJSON)
	}
	if result.ExitCode == 1 {
		var exitErr *exec.ExitError
		if waitErr == nil || !errors.As(waitErr, &exitErr) {
			return nil, fmt.Errorf("feishu: scope preflight missing-scope process mismatch: %w", errControlledCLIInvalidJSON)
		}
	}

	payload, err := decodeScopePreflight(result.Stdout)
	if err != nil {
		return nil, err
	}
	checked, err := p.validatePartition(normalized, payload)
	if err != nil {
		return nil, err
	}
	if result.ExitCode == 0 && (!payload.OK || len(checked.Missing) != 0 || len(checked.Granted) != len(normalized)) {
		return nil, fmt.Errorf("feishu: scope preflight success contract mismatch: %w", errControlledCLIInvalidJSON)
	}
	if result.ExitCode == 0 && (payload.AuthError != "" || payload.Suggestion != nil) {
		return nil, fmt.Errorf("feishu: scope preflight success metadata mismatch: %w", errControlledCLIInvalidJSON)
	}
	if result.ExitCode == 1 && (payload.OK || len(checked.Missing) == 0) {
		return nil, fmt.Errorf("feishu: scope preflight missing-scope contract mismatch: %w", errControlledCLIInvalidJSON)
	}
	if result.ExitCode == 1 && payload.AuthError == "" {
		expectedSuggestion := fmt.Sprintf(
			`lark-cli auth login --scope "%s"`, strings.Join(checked.Missing, " "),
		)
		if payload.Suggestion == nil || *payload.Suggestion != expectedSuggestion {
			return nil, fmt.Errorf("feishu: scope preflight suggestion mismatch: %w", errControlledCLIInvalidJSON)
		}
	}
	return checked, nil
}

type scopePreflightPayload struct {
	OK         bool
	AuthError  string
	Granted    []string
	Missing    []string
	Suggestion *string
}

func decodeScopePreflight(raw []byte) (*scopePreflightPayload, error) {
	if len(raw) == 0 || rejectDuplicateDeviceAuthJSON(raw) != nil || rejectScopePreflightFieldNames(raw) != nil {
		return nil, fmt.Errorf("feishu: scope preflight JSON rejected: %w", errControlledCLIInvalidJSON)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire struct {
		OK         *bool           `json:"ok"`
		AuthError  json.RawMessage `json:"error"`
		Granted    json.RawMessage `json:"granted"`
		Missing    json.RawMessage `json:"missing"`
		Suggestion json.RawMessage `json:"suggestion"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("feishu: decode scope preflight JSON: %w", errControlledCLIInvalidJSON)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("feishu: scope preflight trailing JSON rejected: %w", errControlledCLIInvalidJSON)
	}
	if wire.OK == nil || len(wire.Missing) == 0 {
		return nil, fmt.Errorf("feishu: scope preflight required fields missing: %w", errControlledCLIInvalidJSON)
	}
	missing, err := decodeScopePreflightList(wire.Missing)
	if err != nil {
		return nil, fmt.Errorf("feishu: scope preflight missing list rejected: %w", errControlledCLIInvalidJSON)
	}
	authError, authErrorPresent, err := decodeScopePreflightOptionalString(wire.AuthError)
	if err != nil {
		return nil, fmt.Errorf("feishu: scope preflight auth state rejected: %w", errControlledCLIInvalidJSON)
	}
	suggestion, suggestionPresent, err := decodeScopePreflightOptionalString(wire.Suggestion)
	if err != nil {
		return nil, fmt.Errorf("feishu: scope preflight suggestion rejected: %w", errControlledCLIInvalidJSON)
	}
	granted := []string{}
	if authErrorPresent {
		if *wire.OK || (authError != "not_logged_in" && authError != "no_token") ||
			len(wire.Granted) != 0 || suggestionPresent {
			return nil, fmt.Errorf("feishu: scope preflight auth state rejected: %w", errControlledCLIInvalidJSON)
		}
	} else if len(wire.Granted) == 0 {
		return nil, fmt.Errorf("feishu: scope preflight required fields missing: %w", errControlledCLIInvalidJSON)
	} else {
		granted, err = decodeScopePreflightList(wire.Granted)
		if err != nil {
			return nil, fmt.Errorf("feishu: scope preflight granted list rejected: %w", errControlledCLIInvalidJSON)
		}
	}
	var suggestionValue *string
	if suggestionPresent {
		suggestionValue = &suggestion
	}
	return &scopePreflightPayload{
		OK: *wire.OK, AuthError: authError, Granted: granted, Missing: missing, Suggestion: suggestionValue,
	}, nil
}

func rejectScopePreflightFieldNames(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errControlledCLIInvalidJSON
	}
	allowed := map[string]struct{}{
		"ok": {}, "error": {}, "granted": {}, "missing": {}, "suggestion": {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return errControlledCLIInvalidJSON
		}
	}
	return nil
}

// decodeScopePreflightList preserves the distinction made by the pinned CLI:
// an absent field is rejected by the caller, while a present JSON null is the
// official encoding of an empty nil slice.
func decodeScopePreflightList(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return []string{}, nil
	}
	var values []string
	if len(trimmed) == 0 || json.Unmarshal(trimmed, &values) != nil || values == nil {
		return nil, errControlledCLIInvalidJSON
	}
	return values, nil
}

func decodeScopePreflightOptionalString(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	trimmed := bytes.TrimSpace(raw)
	var value string
	if bytes.Equal(trimmed, []byte("null")) || json.Unmarshal(trimmed, &value) != nil {
		return "", false, errControlledCLIInvalidJSON
	}
	return value, true, nil
}

func (p *ControlledScopePreflight) validatePartition(
	requested []string,
	payload *scopePreflightPayload,
) (*ScopeCheckResult, error) {
	if p == nil || payload == nil {
		return nil, fmt.Errorf("feishu: scope preflight partition missing: %w", errControlledCLIInvalidJSON)
	}
	requestedSet := make(map[string]struct{}, len(requested))
	for _, scope := range requested {
		requestedSet[scope] = struct{}{}
	}
	seen := make(map[string]struct{}, len(requested))
	validate := func(values []string) ([]string, error) {
		normalized := append([]string(nil), values...)
		for _, scope := range normalized {
			if scope == "" || strings.TrimSpace(scope) != scope || strings.IndexByte(scope, 0) >= 0 {
				return nil, errControlledCLIInvalidJSON
			}
			if _, allowed := p.allowedScopes[scope]; !allowed {
				return nil, errControlledCLIInvalidJSON
			}
			if _, expected := requestedSet[scope]; !expected {
				return nil, errControlledCLIInvalidJSON
			}
			if _, duplicateOrOverlap := seen[scope]; duplicateOrOverlap {
				return nil, errControlledCLIInvalidJSON
			}
			seen[scope] = struct{}{}
		}
		sort.Strings(normalized)
		return normalized, nil
	}
	granted, err := validate(payload.Granted)
	if err != nil {
		return nil, fmt.Errorf("feishu: scope preflight granted partition rejected: %w", errControlledCLIInvalidJSON)
	}
	missing, err := validate(payload.Missing)
	if err != nil {
		return nil, fmt.Errorf("feishu: scope preflight missing partition rejected: %w", errControlledCLIInvalidJSON)
	}
	if len(seen) != len(requestedSet) {
		return nil, fmt.Errorf("feishu: scope preflight partition incomplete: %w", errControlledCLIInvalidJSON)
	}
	return &ScopeCheckResult{Granted: granted, Missing: missing}, nil
}

var _ ScopePreflight = (*ControlledScopePreflight)(nil)
