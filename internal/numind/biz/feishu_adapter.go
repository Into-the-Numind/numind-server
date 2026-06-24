package biz

// feishu_adapter.go composes the biz/feishu service at the biz composition root.
//
// G2-authorize device-code redesign (2026-06-24): the service no longer does
// redirect-OAuth (no signer / nonce / token exchanger / answer-resumer / run-reader
// / redirect_uri / web_base_url). It is now a thin layer over the shared
// *ConnectOrchestrator, which drives the device-code connect flow via lark-cli. The
// agent feishu_connect tool drives the SAME orchestrator (built in
// biz/agent/factory_platform.go) for the in-conversation path.

import (
	"fmt"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/crypto"

	"github.com/spf13/viper"
)

// buildFeishuService composes the biz/feishu service from its dependencies. It is
// called only when features.feishu_integration.enabled is true (NewBiz).
//
// Dependencies (device-code):
//   - cipher:      AES-256-GCM over security.thirdparty_token_key (app-secret
//     ciphertext at the provisioner boundary; the user token itself lives in
//     lark-cli's home, never in our store).
//   - provisioner: lark-cli config-init runner (app-create) + device-code auth runner.
//   - orchestrator: ConnectOrchestrator over the store + provisioner.
//
// Returns an error (logged by the caller, non-fatal) if any required key/dep is
// missing — the feature stays off rather than starting half-wired.
func buildFeishuService(
	accStore store.IThirdPartyAccountStore,
) (feishu.IFeishuService, error) {
	// Cipher for app-secret ciphertext at the provisioner boundary. NewCipher
	// fail-fasts on a bad/absent key.
	cipher, err := crypto.NewCipher(viper.GetString("security.thirdparty_token_key"))
	if err != nil {
		return nil, fmt.Errorf("feishu: build token cipher: %w", err)
	}

	// Provisioner: lark-cli runner pinned to a PERSISTENT per-user home base.
	// G1-home: each user's lark-cli home is <feishu.home_base>/u<userID>, which MUST
	// live on a durable volume (e.g. dev /opt/numind/dev/feishu-homes) so the app
	// credentials + tokens lark-cli stores there survive a redeploy and a user
	// reconnecting reuses the same home (idempotent). feishu.lark_cli_home is the
	// pre-G1 key, kept as a fallback so an un-migrated config does not break.
	homeBase := viper.GetString("feishu.home_base")
	if homeBase == "" {
		homeBase = viper.GetString("feishu.lark_cli_home")
	}
	cliRunner, err := feishu.NewLarkCLIRunner(
		viper.GetString("feishu.lark_cli_bin"),
		homeBase,
	)
	if err != nil {
		return nil, fmt.Errorf("feishu: build lark-cli runner: %w", err)
	}
	provisioner, err := feishu.NewProvisioner(cipher, cliRunner)
	if err != nil {
		return nil, fmt.Errorf("feishu: build provisioner: %w", err)
	}

	orch, err := feishu.NewConnectOrchestrator(feishu.ConnectOrchestratorDeps{
		Store:      accStore,
		Starter:    provisioner,
		Poller:     provisioner,
		Authorizer: provisioner,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: build connect orchestrator: %w", err)
	}

	return feishu.NewFeishuService(feishu.Deps{
		Store:        accStore,
		Orchestrator: orch,
	})
}
