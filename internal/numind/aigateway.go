package numind

import (
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/adapter"
)

// registerAIProviders registers every built-in provider adapter (and the
// shared-protocol aliases) on the given gateway.
//
// Shared by BOTH the user server (run / numind.go) and the admin server
// (runAdmin / admin.go) so the two processes can never drift on which
// providers are available — add a provider here once and both gateways get it.
func registerAIProviders(gw *aiservice.Gateway) {
	for _, p := range []aiservice.Provider{
		adapter.NewAliAdapter(),
		adapter.NewVolcAdapter(),
		adapter.NewDMXAPIAdapter(),
		adapter.NewBaiduOCRAdapter(),
		adapter.NewBailianFileAdapter(),
		adapter.NewFunASRAdapter(),
	} {
		gw.RegisterProvider(p)
	}
	// Aliases for providers that share the same adapter protocol.
	gw.RegisterProviderAlias("dmxapi-ssvip", "dmxapi")
	gw.RegisterProviderAlias("aihubmix", "dmxapi")
}
