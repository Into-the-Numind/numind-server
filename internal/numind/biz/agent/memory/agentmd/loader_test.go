package agentmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTestConfig configures viper with the supplied keys (under
// "agent.memory.agent_md.*") and registers a t.Cleanup to reset viper
// at the end of the test. Uses viper.Reset() to avoid leaking state into
// sibling tests within the same package run.
func withTestConfig(t *testing.T, overrides map[string]any) {
	t.Helper()
	// Snapshot existing keys we might trample, restore on cleanup.
	prev := map[string]any{
		"agent.memory.agent_md.enabled":            viper.Get("agent.memory.agent_md.enabled"),
		"agent.memory.agent_md.user_data_dir":      viper.Get("agent.memory.agent_md.user_data_dir"),
		"agent.memory.agent_md.etc_dir":            viper.Get("agent.memory.agent_md.etc_dir"),
		"agent.memory.agent_md.max_per_file_chars": viper.Get("agent.memory.agent_md.max_per_file_chars"),
		"agent.memory.agent_md.max_total_chars":    viper.Get("agent.memory.agent_md.max_total_chars"),
	}
	for k, v := range overrides {
		viper.Set(k, v)
	}
	t.Cleanup(func() {
		for k, v := range prev {
			viper.Set(k, v)
		}
	})
}

// TestLoadAgentMd_NoFilesExist: when neither deployment-level nor user-global
// AGENT.md exists on disk, LoadAgentMd returns an empty LoadResult without
// erroring. Agent runs must still proceed normally.
func TestLoadAgentMd_NoFilesExist(t *testing.T) {
	tmpEtc := t.TempDir()
	tmpUserData := t.TempDir()
	withTestConfig(t, map[string]any{
		"agent.memory.agent_md.enabled":       true,
		"agent.memory.agent_md.etc_dir":       tmpEtc,
		"agent.memory.agent_md.user_data_dir": tmpUserData,
	})

	res, err := LoadAgentMd(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, "", res.Content)
	assert.Len(t, res.Sources, 0)
	assert.Equal(t, 0, res.TotalChars)
	assert.False(t, res.Truncated)
}

// TestLoadAgentMd_OnlyDeploymentLevel: deployment-level file present, user-global
// missing. Result contains the deployment label and content, no user-global.
func TestLoadAgentMd_OnlyDeploymentLevel(t *testing.T) {
	tmpEtc := t.TempDir()
	tmpUserData := t.TempDir()
	withTestConfig(t, map[string]any{
		"agent.memory.agent_md.enabled":       true,
		"agent.memory.agent_md.etc_dir":       tmpEtc,
		"agent.memory.agent_md.user_data_dir": tmpUserData,
	})

	deployContent := "# Deployment rules\n- Always respond in Chinese\n"
	require.NoError(t, os.WriteFile(filepath.Join(tmpEtc, "AGENT.md"), []byte(deployContent), 0o644))

	res, err := LoadAgentMd(context.Background(), 42)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, res.Sources, 1)
	assert.Equal(t, "[Deployment-level]", res.Sources[0].Label)
	assert.Equal(t, filepath.Join(tmpEtc, "AGENT.md"), res.Sources[0].Path)

	assert.Contains(t, res.Content, "[Deployment-level]")
	assert.Contains(t, res.Content, "Always respond in Chinese")
	assert.NotContains(t, res.Content, "[User-global]")
	assert.False(t, res.Truncated)
}

// TestLoadAgentMd_OnlyUserGlobal: user-global file present, deployment missing.
// Result contains the user-global label only.
func TestLoadAgentMd_OnlyUserGlobal(t *testing.T) {
	tmpEtc := t.TempDir()
	tmpUserData := t.TempDir()
	withTestConfig(t, map[string]any{
		"agent.memory.agent_md.enabled":       true,
		"agent.memory.agent_md.etc_dir":       tmpEtc,
		"agent.memory.agent_md.user_data_dir": tmpUserData,
	})

	userID := uint(7)
	userDir := filepath.Join(tmpUserData, "users", "7")
	require.NoError(t, os.MkdirAll(userDir, 0o755))

	userContent := "# Personal preferences\n- Use concise language\n"
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "AGENT.md"), []byte(userContent), 0o644))

	res, err := LoadAgentMd(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, res.Sources, 1)
	assert.Equal(t, "[User-global]", res.Sources[0].Label)
	assert.Equal(t, filepath.Join(userDir, "AGENT.md"), res.Sources[0].Path)

	assert.Contains(t, res.Content, "[User-global]")
	assert.Contains(t, res.Content, "Use concise language")
	assert.NotContains(t, res.Content, "[Deployment-level]")
	assert.False(t, res.Truncated)
}

// TestLoadAgentMd_BothDeploymentAndUserGlobal: both files present. Verifies
// cascade order (deployment first, user-global second) and that the "---"
// separator appears between sections.
func TestLoadAgentMd_BothDeploymentAndUserGlobal(t *testing.T) {
	tmpEtc := t.TempDir()
	tmpUserData := t.TempDir()
	withTestConfig(t, map[string]any{
		"agent.memory.agent_md.enabled":       true,
		"agent.memory.agent_md.etc_dir":       tmpEtc,
		"agent.memory.agent_md.user_data_dir": tmpUserData,
	})

	deployContent := "DEPLOY_MARKER_ABC"
	userContent := "USER_MARKER_XYZ"

	userID := uint(99)
	userDir := filepath.Join(tmpUserData, "users", "99")
	require.NoError(t, os.MkdirAll(userDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpEtc, "AGENT.md"), []byte(deployContent), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "AGENT.md"), []byte(userContent), 0o644))

	res, err := LoadAgentMd(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, res.Sources, 2)
	// Order matters: deployment first
	assert.Equal(t, "[Deployment-level]", res.Sources[0].Label)
	assert.Equal(t, "[User-global]", res.Sources[1].Label)

	// Both content markers present
	assert.Contains(t, res.Content, "DEPLOY_MARKER_ABC")
	assert.Contains(t, res.Content, "USER_MARKER_XYZ")

	// Deployment block appears before user-global block
	deployIdx := strings.Index(res.Content, "DEPLOY_MARKER_ABC")
	userIdx := strings.Index(res.Content, "USER_MARKER_XYZ")
	require.GreaterOrEqual(t, deployIdx, 0)
	require.GreaterOrEqual(t, userIdx, 0)
	assert.Less(t, deployIdx, userIdx, "deployment section must appear before user-global")

	// Separator "---" should be present between the two sections
	assert.Contains(t, res.Content, "\n\n---\n\n")

	assert.False(t, res.Truncated)
}

// TestLoadAgentMd_SingleFileOversize: a 20KB file exceeds the 12KB
// max_per_file_chars cap. Verifies content is truncated with a
// [truncated: ...] marker and Truncated==true is set on the result.
func TestLoadAgentMd_SingleFileOversize(t *testing.T) {
	tmpEtc := t.TempDir()
	tmpUserData := t.TempDir()
	withTestConfig(t, map[string]any{
		"agent.memory.agent_md.enabled":            true,
		"agent.memory.agent_md.etc_dir":            tmpEtc,
		"agent.memory.agent_md.user_data_dir":      tmpUserData,
		"agent.memory.agent_md.max_per_file_chars": 12288,
		"agent.memory.agent_md.max_total_chars":    51200,
	})

	// 20KB of 'a' characters
	bigContent := strings.Repeat("a", 20*1024)
	require.NoError(t, os.WriteFile(filepath.Join(tmpEtc, "AGENT.md"), []byte(bigContent), 0o644))

	res, err := LoadAgentMd(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, res.Sources, 1)
	assert.True(t, res.Truncated, "Truncated should be true when file > max_per_file_chars")

	// The content body should contain the truncation marker
	assert.Contains(t, res.Content, "[truncated: original >")
	assert.Contains(t, res.Content, "12288 chars]")

	// Source.Size should reflect the truncated (capped) content length, which
	// is the cap + length of the truncation marker we appended.
	assert.LessOrEqual(t, res.Sources[0].Size, 12288+200, "size should be near cap, not 20KB")
}

// TestLoadAgentMd_ReadPermissionError: file exists but is unreadable
// (chmod 000). Loader should skip it without failing.
//
// Skipped on Windows where the POSIX permission semantics differ, and
// skipped when running as root (chmod 000 doesn't deny root reads).
func TestLoadAgentMd_ReadPermissionError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 semantics not portable to Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses POSIX permission bits; chmod 000 does not deny root reads")
	}

	tmpEtc := t.TempDir()
	tmpUserData := t.TempDir()
	withTestConfig(t, map[string]any{
		"agent.memory.agent_md.enabled":       true,
		"agent.memory.agent_md.etc_dir":       tmpEtc,
		"agent.memory.agent_md.user_data_dir": tmpUserData,
	})

	deployPath := filepath.Join(tmpEtc, "AGENT.md")
	require.NoError(t, os.WriteFile(deployPath, []byte("DEPLOY_CONTENT"), 0o644))
	// Strip all read permissions
	require.NoError(t, os.Chmod(deployPath, 0o000))
	// Restore at cleanup so t.TempDir's RemoveAll can succeed on some kernels.
	t.Cleanup(func() {
		_ = os.Chmod(deployPath, 0o644)
	})

	// Also place a readable user-global file so we can verify the loader
	// continues past the permission-denied file.
	userID := uint(5)
	userDir := filepath.Join(tmpUserData, "users", "5")
	require.NoError(t, os.MkdirAll(userDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "AGENT.md"), []byte("USER_CONTENT"), 0o644))

	res, err := LoadAgentMd(context.Background(), userID)
	require.NoError(t, err, "permission errors must not be returned as Go errors")
	require.NotNil(t, res)

	// Deployment file unreadable → skipped; user-global readable → loaded
	require.Len(t, res.Sources, 1)
	assert.Equal(t, "[User-global]", res.Sources[0].Label)
	assert.Contains(t, res.Content, "USER_CONTENT")
	assert.NotContains(t, res.Content, "DEPLOY_CONTENT")
}

// TestLoadAgentMd_UserIDZero: userID==0 (anonymous / system context) skips
// the user-global candidate and reads only the deployment-level file.
func TestLoadAgentMd_UserIDZero(t *testing.T) {
	tmpEtc := t.TempDir()
	tmpUserData := t.TempDir()
	withTestConfig(t, map[string]any{
		"agent.memory.agent_md.enabled":       true,
		"agent.memory.agent_md.etc_dir":       tmpEtc,
		"agent.memory.agent_md.user_data_dir": tmpUserData,
	})

	require.NoError(t, os.WriteFile(filepath.Join(tmpEtc, "AGENT.md"), []byte("DEPLOY_ONLY"), 0o644))

	// Place a stray user-global file under "users/0" to prove buildCandidates
	// does NOT generate that path when userID==0.
	userZeroDir := filepath.Join(tmpUserData, "users", "0")
	require.NoError(t, os.MkdirAll(userZeroDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userZeroDir, "AGENT.md"), []byte("LEAKED_USER_ZERO"), 0o644))

	res, err := LoadAgentMd(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, res.Sources, 1)
	assert.Equal(t, "[Deployment-level]", res.Sources[0].Label)
	assert.Contains(t, res.Content, "DEPLOY_ONLY")
	assert.NotContains(t, res.Content, "LEAKED_USER_ZERO", "userID=0 must not read users/0/AGENT.md")
}

// TestLoadAgentMd_Disabled: cfg.Enabled=false returns an empty result without
// touching the filesystem. Loader should be a fast no-op when disabled.
func TestLoadAgentMd_Disabled(t *testing.T) {
	tmpEtc := t.TempDir()
	tmpUserData := t.TempDir()
	withTestConfig(t, map[string]any{
		"agent.memory.agent_md.enabled":       false,
		"agent.memory.agent_md.etc_dir":       tmpEtc,
		"agent.memory.agent_md.user_data_dir": tmpUserData,
	})

	// Place files that would otherwise be loaded — disabled loader should ignore them.
	require.NoError(t, os.WriteFile(filepath.Join(tmpEtc, "AGENT.md"), []byte("DEPLOY"), 0o644))
	userDir := filepath.Join(tmpUserData, "users", "1")
	require.NoError(t, os.MkdirAll(userDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "AGENT.md"), []byte("USER"), 0o644))

	res, err := LoadAgentMd(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, "", res.Content)
	assert.Len(t, res.Sources, 0)
	assert.Equal(t, 0, res.TotalChars)
	assert.False(t, res.Truncated)
}

// ---------------------------------------------------------------------------
// Helper / internal coverage tests (not part of the spec's 8-case checklist)
// ---------------------------------------------------------------------------

// TestBuildCandidates_UserIDZero_NoUserPath: pure-function check that
// buildCandidates omits the user-global candidate when userID==0.
func TestBuildCandidates_UserIDZero_NoUserPath(t *testing.T) {
	cfg := Config{
		EtcDir:      "/etc/numind",
		UserDataDir: "/data/numind/user_files",
	}
	cands := buildCandidates(cfg, 0)
	require.Len(t, cands, 1)
	assert.Equal(t, "[Deployment-level]", cands[0].Label)
}

// TestBuildCandidates_EmptyUserDataDir: when UserDataDir is unset (production
// misconfiguration), the user-global candidate is omitted even for userID>0.
// This avoids generating a relative path like "users/N/AGENT.md" that would
// resolve against an unpredictable cwd.
func TestBuildCandidates_EmptyUserDataDir(t *testing.T) {
	cfg := Config{
		EtcDir:      "/etc/numind",
		UserDataDir: "",
	}
	cands := buildCandidates(cfg, 42)
	require.Len(t, cands, 1)
	assert.Equal(t, "[Deployment-level]", cands[0].Label)
}

// TestNormalizeLineEndings: CRLF and lone CR are converted to LF.
func TestNormalizeLineEndings(t *testing.T) {
	in := "line1\r\nline2\rline3\nline4"
	out := normalizeLineEndings(in)
	assert.Equal(t, "line1\nline2\nline3\nline4", out)
}
