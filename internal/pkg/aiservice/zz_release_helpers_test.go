package aiservice_test

import "strings"

// contains is a test helper (rerank-routing gateway_perroute_test.go uses it; its
// upstream definition lives in a gateway_test.go variant not on this release line).
func contains(s, sub string) bool { return strings.Contains(s, sub) }
