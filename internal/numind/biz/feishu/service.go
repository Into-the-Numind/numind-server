// Package feishu — service.go is the feishu-integration connection service behind
// the HTTP controller (controller/v1/feishu). Connection goes entirely through
// lark-cli — config init for app-create, blocking auth login for authorization —
// with the connect PHASE read from the user's lark-cli HOME (the single truth
// source), driven by the shared *ConnectOrchestrator (fix/feishu-phase-from-home,
// 2026-06-24). There is NO redirect-OAuth / authorize URL / OAuth callback / token
// exchange / device-code file.
//
// User-facing operations:
//
//   - Connect: advance the connect flow one step (phase read from the home) and
//     return the next action (create_app page URL, authorize verification URL, or
//     done). The settings page shows the URL; the user opens it; a later
//     Connect/Status call advances. (The primary connect path is the agent
//     feishu_connect tool, which drives the SAME orchestrator with auto-resume.)
//   - Status:  report connection state from the durable DB connected flag (reconciled
//     from the home on the done path).
//   - Unbind:  delete the stored connection row (the 飞书 app + lark-cli home are kept).
//
// Layering: biz layer over the orchestrator + store. No agent imports. NOT routed
// through aiservice (飞书 is an external business API).
package feishu

import (
	"context"
	"errors"
	"fmt"

	"numind-server/internal/numind/store"

	"gorm.io/gorm"
)

// Next-step discriminants returned by Connect (device-code connect contract).
const (
	// NextStepCreateApp: the user has no self-built 飞书 app yet → open the
	// device-code page URL (config init) to create it.
	NextStepCreateApp = "create_app"
	// NextStepAuthorize: the app exists → open the verification URL and grant scopes
	// (device-code); a later Connect/Status call completes it.
	NextStepAuthorize = "authorize"
	// NextStepDone: already connected.
	NextStepDone = "done"
)

// --- DTOs ------------------------------------------------------------------

// ConnectResult is the POST /v1/feishu/connect response data.
type ConnectResult struct {
	NextStep string `json:"next_step"` // create_app | authorize | done
	URL      string `json:"url"`       // device-code page URL or verification URL ("" for done)
}

// StatusResult is the GET /v1/feishu/status response data.
type StatusResult struct {
	Connected bool   `json:"connected"`
	Status    string `json:"status"` // none | connected
	AppID     string `json:"app_id"`
}

// --- service ---------------------------------------------------------------

// connectStepper is the orchestrator surface the service drives (the concrete
// *ConnectOrchestrator satisfies it). Declared as an interface so the service is
// unit-tested with a fake.
type connectStepper interface {
	PollAndPersistApp(ctx context.Context, userID uint) (bool, error)
	NextConnectStep(ctx context.Context, userID uint, runID uint64, questionText string) (*ConnectStep, error)
}

// Deps wires FeishuService. All deps are required.
type Deps struct {
	Store        store.IThirdPartyAccountStore
	Orchestrator connectStepper
}

// FeishuService is the IFeishuService implementation.
type FeishuService struct {
	store        store.IThirdPartyAccountStore
	orchestrator connectStepper
}

// IFeishuService is the biz interface exposed via biz.IBiz.FeishuSvc().
type IFeishuService interface {
	Connect(ctx context.Context, userID uint) (*ConnectResult, error)
	Status(ctx context.Context, userID uint) (*StatusResult, error)
	Unbind(ctx context.Context, userID uint) error
}

// compile-time guard.
var _ IFeishuService = (*FeishuService)(nil)

// NewFeishuService builds the service, failing fast on a missing required dep.
func NewFeishuService(d Deps) (*FeishuService, error) {
	if d.Store == nil {
		return nil, errors.New("feishu: nil store for service")
	}
	if d.Orchestrator == nil {
		return nil, errors.New("feishu: nil orchestrator for service")
	}
	return &FeishuService{store: d.Store, orchestrator: d.Orchestrator}, nil
}

// Connect advances the device-code connect flow one step. It first bridges any
// just-finished app-create (poll+persist), then asks the orchestrator for the next
// step. runID/questionText are 0/"" on the HTTP path (no agent run to resume).
func (s *FeishuService) Connect(ctx context.Context, userID uint) (*ConnectResult, error) {
	if _, err := s.orchestrator.PollAndPersistApp(ctx, userID); err != nil {
		return nil, fmt.Errorf("feishu.Connect: poll app (user %d): %w", userID, err)
	}
	step, err := s.orchestrator.NextConnectStep(ctx, userID, 0, "")
	if err != nil {
		return nil, fmt.Errorf("feishu.Connect: next step (user %d): %w", userID, err)
	}
	return &ConnectResult{NextStep: mapPhase(step.Phase), URL: step.URL}, nil
}

// Status reports the connection state for userID from the durable DB connected
// flag. No row → none. A connected row → connected.
func (s *FeishuService) Status(ctx context.Context, userID uint) (*StatusResult, error) {
	acc, err := s.store.Get(ctx, userID, ProviderLark)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &StatusResult{Connected: false, Status: "none"}, nil
		}
		return nil, fmt.Errorf("feishu.Status: load account (user %d): %w", userID, err)
	}
	if acc.Connected {
		return &StatusResult{Connected: true, Status: "connected", AppID: acc.AppID}, nil
	}
	return &StatusResult{Connected: false, Status: "none", AppID: acc.AppID}, nil
}

// Unbind deletes the stored connection row (the 飞书 app itself + the lark-cli home
// are kept). Idempotent: deleting a non-existent connection is not an error.
func (s *FeishuService) Unbind(ctx context.Context, userID uint) error {
	if err := s.store.Delete(ctx, userID, ProviderLark); err != nil {
		return fmt.Errorf("feishu.Unbind: delete account (user %d): %w", userID, err)
	}
	return nil
}

// mapPhase maps an orchestrator ConnectPhase to the HTTP next_step discriminant.
func mapPhase(phase string) string {
	switch phase {
	case ConnectPhaseCreateApp:
		return NextStepCreateApp
	case ConnectPhaseAuthorize:
		return NextStepAuthorize
	default:
		return NextStepDone
	}
}
