package config

import (
	"strings"

	"github.com/spf13/viper"
)

// EnvPrefix is the prefix for environment variables that override config_*.yaml
// values via viper. e.g. config key "web_search.tavily.api_key" is read from
// env var NUMIND_WEB_SEARCH_TAVILY_API_KEY when this prefix + the key replacer
// below are wired into viper.
const EnvPrefix = "NUMIND"

// SetupViperEnvBindings configures viper to read env vars prefixed with NUMIND_
// and map nested config keys (a.b.c -> NUMIND_A_B_C). Called once at process
// init from internal/numind/helper.go::initConfig, AND from the regression
// test in internal/pkg/aiservice/web_search_test.go that pins this binding
// contract.
//
// If anyone removes any of the three viper calls below, the production env-var
// override pathway breaks AND the regression test fails — both signals point
// here. Keep the production path and the test using this single function so
// the contract has exactly one source of truth.
func SetupViperEnvBindings(v *viper.Viper) {
	v.AutomaticEnv()
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
}
