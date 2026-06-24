package biz

// feishu_adapter.go bridges the biz/feishu service to the agent package WITHOUT
// biz/feishu importing biz/agent (which would create an import cycle:
// biz/agent → biz, and biz/feishu would be pulled into agent's graph). The biz
// package already imports BOTH agent and feishu, so the narrow adapter
// interfaces feishu declares (AnswerResumer / RunStateReader) are implemented
// here at the composition root and injected via biz.FeishuSvc wiring.

import (
	"context"
	"fmt"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/model"
	redispkg "numind-server/internal/pkg/redis"

	"github.com/spf13/viper"
)

// feishuAnswerAdapter adapts *agent.StudentRunService.Answer to
// feishu.AnswerResumer. The OAuth callback resumes the paused run by submitting
// the synthetic auth answer keyed by the pending question text (answer.go
// invariant: the key MUST equal Questions[i].Question).
type feishuAnswerAdapter struct {
	runSvc *agent.StudentRunService
}

// ResumeWithAnswer builds the single-question answer map and drives
// StudentRunService.Answer (which validates ownership + state, persists the
// answer turn, and restarts the runner detached). The AnswerResponse is
// discarded — the callback only needs success/failure.
func (a *feishuAnswerAdapter) ResumeWithAnswer(ctx context.Context, userID uint, runID uint64, questionText, freeText string) error {
	req := agent.AnswerRequest{
		Answers: map[string]agent.AnswerItem{
			questionText: {FreeText: freeText},
		},
	}
	_, err := a.runSvc.Answer(ctx, userID, runID, req)
	return err
}

// compile-time guard.
var _ feishu.AnswerResumer = (*feishuAnswerAdapter)(nil)

// feishuRunReaderAdapter adapts store.IAgentRunStore.Get to feishu.RunStateReader
// so the callback can read run.UserID (cross-user defense) + run.StateReason
// (idempotency) without feishu importing the agent run store's full surface.
type feishuRunReaderAdapter struct {
	runStore store.IAgentRunStore
}

// GetRun fetches the agent_run row by ID.
func (a *feishuRunReaderAdapter) GetRun(ctx context.Context, runID uint64) (*model.AgentRun, error) {
	return a.runStore.Get(ctx, runID)
}

// compile-time guard.
var _ feishu.RunStateReader = (*feishuRunReaderAdapter)(nil)

// Default 飞书 OAuth/connection endpoints + first-batch scopes. The state HMAC
// key and AES token key are read from security.* (already fail-fast validated in
// numind.go when the flag is on). The web base / redirect / authorize URLs are
// config-overridable per env (feishu.*); the defaults match prod.
//
// The actual default values live in feishu.DefaultAuthorizeURL / DefaultScopes
// (single source of truth) so the agent tool factory's connector build uses the
// identical fallbacks.
const (
	defaultFeishuAuthorizeURL = feishu.DefaultAuthorizeURL
	defaultFeishuScopes       = feishu.DefaultScopes
)

// buildFeishuService composes the biz/feishu service from its dependencies. It
// is called only when features.feishu_integration.enabled is true (NewBiz).
//
// Dependencies:
//   - cipher:      AES-256-GCM over security.thirdparty_token_key (token at-rest).
//   - state signer: HMAC over security.feishu_state_key + Redis one-time nonce.
//   - provisioner: lark-cli device-code runner + HTTP OAuth token exchanger.
//   - answer/run:  narrow adapters onto *agent.StudentRunService + agent_run store.
//
// Returns an error (logged by the caller, non-fatal) if any required key/dep is
// missing or Redis is unavailable — the feature stays off rather than starting
// half-wired.
func buildFeishuService(
	runSvc *agent.StudentRunService,
	accStore store.IThirdPartyAccountStore,
	runStore store.IAgentRunStore,
) (feishu.IFeishuService, error) {
	if runSvc == nil {
		return nil, fmt.Errorf("feishu: nil student run service (agent runtime not wired)")
	}

	// Cipher for token at-rest encryption (separate instance from the package
	// default; same key). NewCipher fail-fasts on a bad/absent key.
	cipher, err := crypto.NewCipher(viper.GetString("security.thirdparty_token_key"))
	if err != nil {
		return nil, fmt.Errorf("feishu: build token cipher: %w", err)
	}

	// State signer needs a Redis-backed one-time nonce store (replay protection).
	rdb := redispkg.GetClient()
	if rdb == nil {
		return nil, fmt.Errorf("feishu: redis client unavailable (required for OAuth state nonce store)")
	}
	nonceStore, err := feishu.NewRedisNonceStore(rdb)
	if err != nil {
		return nil, fmt.Errorf("feishu: build nonce store: %w", err)
	}
	signer, err := feishu.NewStateSigner(viper.GetString("security.feishu_state_key"), nonceStore)
	if err != nil {
		return nil, fmt.Errorf("feishu: build state signer: %w", err)
	}

	// Provisioner: lark-cli device-code runner + HTTP OAuth token exchanger.
	cliRunner, err := feishu.NewLarkCLIRunner(
		viper.GetString("feishu.lark_cli_bin"),
		viper.GetString("feishu.lark_cli_home"),
	)
	if err != nil {
		return nil, fmt.Errorf("feishu: build lark-cli runner: %w", err)
	}
	redirectURI := viper.GetString("feishu.redirect_uri")
	if redirectURI == "" {
		return nil, fmt.Errorf("feishu: feishu.redirect_uri not configured (required for OAuth exchange)")
	}
	exchanger, err := feishu.NewHTTPTokenExchanger(redirectURI)
	if err != nil {
		return nil, fmt.Errorf("feishu: build token exchanger: %w", err)
	}
	provisioner, err := feishu.NewProvisioner(cipher, cliRunner, exchanger)
	if err != nil {
		return nil, fmt.Errorf("feishu: build provisioner: %w", err)
	}

	webBaseURL := viper.GetString("feishu.web_base_url")
	if webBaseURL == "" {
		return nil, fmt.Errorf("feishu: feishu.web_base_url not configured (required for callback redirect)")
	}
	authorizeURL := viper.GetString("feishu.authorize_url")
	if authorizeURL == "" {
		authorizeURL = defaultFeishuAuthorizeURL
	}
	scopes := viper.GetString("feishu.scopes")
	if scopes == "" {
		scopes = defaultFeishuScopes
	}

	return feishu.NewFeishuService(feishu.Deps{
		Store:        accStore,
		Signer:       signer,
		Provisioner:  provisioner,
		Answer:       &feishuAnswerAdapter{runSvc: runSvc},
		Runs:         &feishuRunReaderAdapter{runStore: runStore},
		WebBaseURL:   webBaseURL,
		AuthorizeURL: authorizeURL,
		RedirectURI:  redirectURI,
		ScopesCSV:    scopes,
	})
}
