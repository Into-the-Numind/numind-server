package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebFetch_SSRF_ReturnsSoftResult: a blocked SSRF target must come back as a SOFT
// tool result (non-empty string, nil error) — consistent with soft interception — rather
// than a Go error. (agent-security-hardening T4; reviewer behavioral-test gap.)
func TestWebFetch_SSRF_ReturnsSoftResult(t *testing.T) {
	tool := &webFetchTool{} // skipSSRFCheck=false → real validateFetchURL
	for _, url := range []string{
		`http://169.254.169.254/latest/meta-data/`,
		`http://127.0.0.1/admin`,
		`http://10.0.0.1/internal`,
		`http://[::1]:8080/admin`, // IPv6 loopback
	} {
		res, err := tool.Execute(context.Background(), ToolInput(`{"url":"`+url+`"}`))
		require.NoError(t, err, "SSRF block for %s must be a soft tool result, not a Go error", url)
		assert.NotEmpty(t, string(res), "soft result must be non-empty for %s", url)
		assert.Contains(t, string(res), "安全策略",
			"soft result should carry the SSRF-specific '安全策略' wording, got %q", string(res))
	}
}

// TestRunPython_downloadInputFile_SSRF_BlocksInternal: run_python's input-file download
// must reject internal / cloud-metadata targets (it previously had NO SSRF check). The
// Execute caller wraps the error into a soft runPythonFriendlyError, so the run continues.
func TestRunPython_downloadInputFile_SSRF_BlocksInternal(t *testing.T) {
	tool := &runPythonTool{}
	for _, url := range []string{
		`http://169.254.169.254/latest/meta-data/iam/`,
		`http://127.0.0.1:6379/`,
		`http://10.0.0.1/x`,
		`http://192.168.1.1/x`,
		`http://172.16.0.1/x`,
		`http://[::1]:6379/x`, // IPv6 loopback
	} {
		_, err := tool.downloadInputFile(context.Background(), url)
		require.Error(t, err, "internal input_file URL %s must be rejected before download", url)
		assert.Contains(t, err.Error(), "SSRF", "rejection should be a clear SSRF-policy error for %s, got %v", url, err)
	}
}

// TestRunPython_downloadInputFile_SSRF_AllowsPublicLiteral: a public IP literal passes the
// SSRF guard (the guard rejects only internal/metadata, never public) — proves the check
// does not over-block. (We only assert the guard does not reject; the actual HTTP GET to a
// public IP is out of scope for a unit test.)
func TestRunPython_downloadInputFile_SSRF_AllowsPublicLiteral(t *testing.T) {
	// validateFetchURL is the wired guard; confirm it permits a public address.
	if _, err := validateFetchURL("http://8.8.8.8/file.csv", false); err != nil {
		t.Errorf("public IP literal must pass the SSRF guard, got %v", err)
	}
}
