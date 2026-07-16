package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTrackedConfigsDoNotContainDashScopeAPIKeys prevents a production
// DashScope key from being committed in the developer config templates.
func TestTrackedConfigsDoNotContainDashScopeAPIKeys(t *testing.T) {
	configFiles := []string{"config_local.yaml", "config_dev.yaml"}
	keyPaths := map[string]struct{}{
		"ali.api_key":                  {},
		"ali.vision.api_key":           {},
		"ai_providers.ali.api_key":     {},
		"ai_providers.bailian.api_key": {},
	}

	for _, filename := range configFiles {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", filename))
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}

		assertNoDashScopeAPIKeys(t, filename, string(data), keyPaths)
	}
}

type yamlScope struct {
	indent int
	key    string
}

func assertNoDashScopeAPIKeys(t *testing.T, filename, document string, keyPaths map[string]struct{}) {
	t.Helper()
	var scopes []yamlScope
	for _, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " "))
		key, value, found := strings.Cut(trimmed, ":")
		if !found || strings.Contains(key, " ") {
			continue
		}
		for len(scopes) > 0 && scopes[len(scopes)-1].indent >= indent {
			scopes = scopes[:len(scopes)-1]
		}

		pathParts := make([]string, 0, len(scopes)+1)
		for _, scope := range scopes {
			pathParts = append(pathParts, scope.key)
		}
		pathParts = append(pathParts, strings.TrimSpace(key))
		path := strings.Join(pathParts, ".")
		if _, watched := keyPaths[path]; watched && strings.HasPrefix(strings.Trim(strings.TrimSpace(value), "\"'"), "sk-") {
			t.Errorf("%s contains a committed DashScope API key at %s", filename, path)
		}

		if strings.TrimSpace(value) == "" {
			scopes = append(scopes, yamlScope{indent: indent, key: strings.TrimSpace(key)})
		}
	}
}
