package agent

import "testing"

// followup3 BE-2 review (P0 regression guard): create_docx runs its entire
// execution inside a borrowed sandbox container (it execs the embedded
// md_to_docx.py). If it is NOT in IsSandboxIsolatedExecTool, the sandbox hook
// never borrows a session, sandboxSessionForCurrentCall returns nil, and every
// create_docx call dies on the "沙箱当前不可用" soft error before reaching the
// conversion step — i.e. the tool is silently dead. Pin membership here.
func TestIsSandboxIsolatedExecTool_IncludesCreateDocx(t *testing.T) {
	needSandbox := []string{"bash_exec", "run_python", "create_docx"}
	for _, name := range needSandbox {
		if !IsSandboxIsolatedExecTool(name) {
			t.Errorf("IsSandboxIsolatedExecTool(%q) = false, want true (sandbox session would never be borrowed)", name)
		}
	}
	// Pure-Go tools (no sandbox) must stay out, or the pool would borrow a
	// container needlessly.
	noSandbox := []string{"create_html", "create_csv", "create_json", "create_text", "web_search", "file_read"}
	for _, name := range noSandbox {
		if IsSandboxIsolatedExecTool(name) {
			t.Errorf("IsSandboxIsolatedExecTool(%q) = true, want false (pure-Go tool needs no sandbox)", name)
		}
	}
}
