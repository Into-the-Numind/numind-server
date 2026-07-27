package agent

import "testing"

// Standard Office create tools are native backend generators. Only tools that
// execute commands inside a borrowed container should appear here; otherwise
// high-frequency file generation needlessly consumes sandbox capacity.
func TestIsSandboxIsolatedExecTool_OnlyExecTools(t *testing.T) {
	needSandbox := []string{"bash_exec", "run_python"}
	for _, name := range needSandbox {
		if !IsSandboxIsolatedExecTool(name) {
			t.Errorf("IsSandboxIsolatedExecTool(%q) = false, want true (sandbox session would never be borrowed)", name)
		}
	}
	// Pure-Go tools (no sandbox) must stay out, or the pool would borrow a
	// container needlessly.
	noSandbox := []string{"create_docx", "create_xlsx", "create_pptx", "create_html", "create_csv", "create_json", "create_text", "web_search", "file_read"}
	for _, name := range noSandbox {
		if IsSandboxIsolatedExecTool(name) {
			t.Errorf("IsSandboxIsolatedExecTool(%q) = true, want false (pure-Go tool needs no sandbox)", name)
		}
	}
}
