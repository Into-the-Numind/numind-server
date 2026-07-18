package feishu

import (
	"context"
	"errors"
	"sort"
	"strings"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

var (
	// ErrInspectionRejected means model input or current-run skill evidence was
	// outside the server-owned catalog contract.
	ErrInspectionRejected = errors.New("feishu inspection rejected")
	// ErrInspectionUnavailable means current-user connection or read-only scope
	// inspection could not be safely completed.
	ErrInspectionUnavailable = errors.New("feishu inspection unavailable")
)

const (
	InspectionModeConnection = "connection"
	InspectionModeCommand    = "command"
)

// InspectionRequest contains only model-selectable mode/business argv plus
// server-context identities supplied by the Agent tool boundary.
type InspectionRequest struct {
	UserID        uint
	AgentRunID    uint64
	Mode          string
	Argv          []string
	SkillReceipts []string
}

// InspectionResult is deliberately narrower than workspace status and never
// exposes user IDs, generation, App ID, CLI version, tokens, URLs or receipts.
type InspectionResult struct {
	Mode            string            `json:"mode"`
	ConnectionState string            `json:"connection_state,omitempty"`
	Capabilities    map[string]string `json:"capabilities,omitempty"`
	CommandPath     string            `json:"command_path,omitempty"`
	Domain          string            `json:"domain,omitempty"`
	Risk            RiskLevel         `json:"risk,omitempty"`
	Ready           *bool             `json:"ready,omitempty"`
	GrantedScopes   []string          `json:"granted_scopes,omitempty"`
	MissingScopes   []string          `json:"missing_scopes,omitempty"`
}

// Inspect performs a current-user, read-only connection or command check. It
// never creates an operation, invokes a business command, starts recovery or
// asks for confirmation.
func (s *FeishuOperationService) Inspect(ctx context.Context, request InspectionRequest) (*InspectionResult, error) {
	if s == nil || ctx == nil || request.UserID == 0 {
		return nil, ErrInspectionRejected
	}
	switch request.Mode {
	case InspectionModeConnection:
		if request.AgentRunID == 0 || len(request.Argv) != 0 || len(request.SkillReceipts) != 0 {
			return nil, ErrInspectionRejected
		}
		return s.inspectConnection(ctx, request.UserID)
	case InspectionModeCommand:
		if request.AgentRunID == 0 || len(request.Argv) == 0 || validateOperationReceipts(request.SkillReceipts) != nil {
			return nil, ErrInspectionRejected
		}
		return s.inspectCommand(ctx, request)
	default:
		return nil, ErrInspectionRejected
	}
}

func (s *FeishuOperationService) inspectConnection(ctx context.Context, userID uint) (*InspectionResult, error) {
	account, err := s.accounts.Get(ctx, userID, ProviderLark)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &InspectionResult{
			Mode: InspectionModeConnection, ConnectionState: model.FeishuConnectionNone,
			Capabilities: unknownInspectionCapabilities(),
		}, nil
	}
	if err != nil || !validOperationAccount(account, userID) {
		return nil, ErrInspectionUnavailable
	}
	status := workspaceStatusFromAccount(account)
	capabilities := unknownInspectionCapabilities()
	for domain, capability := range status.Capabilities {
		if _, expected := capabilities[domain]; expected && validCapabilityState(capability.State) {
			capabilities[domain] = capability.State
		}
	}
	return &InspectionResult{
		Mode: InspectionModeConnection, ConnectionState: status.State, Capabilities: capabilities,
	}, nil
}

func unknownInspectionCapabilities() map[string]string {
	return map[string]string{
		"docs": model.FeishuCapabilityUnknown, "base": model.FeishuCapabilityUnknown,
		"wiki": model.FeishuCapabilityUnknown, "drive": model.FeishuCapabilityUnknown,
	}
}

func (s *FeishuOperationService) inspectCommand(
	ctx context.Context,
	request InspectionRequest,
) (*InspectionResult, error) {
	normalized, err := s.catalog.Normalize(append([]string(nil), request.Argv...), nil)
	if err != nil {
		return nil, ErrInspectionRejected
	}
	if err := s.receipts.VerifyRequired(
		append([]string(nil), request.SkillReceipts...), request.AgentRunID, operationReceiptDomain(normalized),
	); err != nil {
		return nil, ErrInspectionRejected
	}
	account, err := s.accounts.Get(ctx, request.UserID, ProviderLark)
	if err != nil || !validOperationAccount(account, request.UserID) ||
		account.ConnectionState != model.FeishuConnectionConnected || !account.Connected {
		return nil, ErrInspectionUnavailable
	}
	var check *ScopeCheckResult
	var checkErr error
	vaultErr := s.vault.WithHome(ctx, request.UserID, account.Generation, func(home string) (bool, error) {
		check, checkErr = s.preflight.Check(ctx, home, append([]string(nil), normalized.Scopes...))
		return false, nil
	})
	if vaultErr != nil || checkErr != nil || check == nil {
		return nil, ErrInspectionUnavailable
	}
	granted := append([]string(nil), check.Granted...)
	missing := append([]string(nil), check.Missing...)
	sort.Strings(granted)
	sort.Strings(missing)
	if !inspectionScopePartition(normalized.Scopes, granted, missing) {
		return nil, ErrInspectionUnavailable
	}
	ready := len(missing) == 0
	return &InspectionResult{
		Mode: InspectionModeCommand, CommandPath: normalized.Path, Domain: normalized.Domain,
		Risk: normalized.Risk, Ready: &ready,
		GrantedScopes: granted, MissingScopes: missing,
	}, nil
}

func inspectionScopePartition(requested, granted, missing []string) bool {
	requestedCopy := append([]string(nil), requested...)
	sort.Strings(requestedCopy)
	combined := append(append([]string(nil), granted...), missing...)
	sort.Strings(combined)
	if len(combined) != len(requestedCopy) {
		return false
	}
	for index := range combined {
		if combined[index] != requestedCopy[index] || strings.TrimSpace(combined[index]) != combined[index] {
			return false
		}
		if index > 0 && combined[index] == combined[index-1] {
			return false
		}
	}
	return true
}
