package sandbox

import (
	"os"
	"strings"
	"testing"
)

func TestResolveSeccompPath_WritesFileAndReturnsAbsPath(t *testing.T) {
	resetSeccompPathForTesting()
	path, err := ResolveSeccompPath()
	if err != nil {
		t.Fatalf("ResolveSeccompPath err = %v", err)
	}
	if path == "" {
		t.Fatal("ResolveSeccompPath returned empty path")
	}
	if !strings.Contains(path, "numind-sandbox-seccomp.json") {
		t.Errorf("path = %q; should contain numind-sandbox-seccomp.json", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file err = %v", err)
	}
	if !strings.Contains(string(b), "SCMP_ACT_ALLOW") {
		t.Errorf("written profile missing SCMP_ACT_ALLOW default action")
	}
	if !strings.Contains(string(b), "ptrace") {
		t.Errorf("written profile missing ptrace deny entry")
	}
}

func TestResolveSeccompPath_SyncOnce(t *testing.T) {
	resetSeccompPathForTesting()
	p1, err := ResolveSeccompPath()
	if err != nil {
		t.Fatalf("first call err = %v", err)
	}
	p2, err := ResolveSeccompPath()
	if err != nil {
		t.Fatalf("second call err = %v", err)
	}
	if p1 != p2 {
		t.Errorf("sync.Once should yield identical path: p1=%s p2=%s", p1, p2)
	}
}

// TestSeccompProfile_AllowsClone3 verifies the embedded profile no longer ERRNO-denies
// clone3 (doc-export-sandbox hotfix). Newer glibc thread creation tries clone3 first and
// does NOT fall back to clone on EPERM (only ENOSYS) — denying clone3 broke pthread_create
// (pandoc GHC RTS + weasyprint GLib/Pango), so md→pdf/docx export and the agent
// pdf-from-html skill all crashed. Other dangerous syscalls (ptrace/mount/unshare/bpf/…)
// stay denied.
func TestSeccompProfile_AllowsClone3(t *testing.T) {
	prof := string(defaultSeccompProfile)
	if strings.Contains(prof, "clone3") {
		t.Errorf("seccomp profile must NOT deny clone3 (breaks thread creation for pandoc/weasyprint)")
	}
	// Dangerous syscalls must still be denied (sanity: we only loosened clone3).
	for _, denied := range []string{"ptrace", "mount", "unshare", "bpf", "init_module"} {
		if !strings.Contains(prof, denied) {
			t.Errorf("seccomp profile lost deny entry %q — only clone3 should have been removed", denied)
		}
	}
}

// TestBuildSpawnConfig_IncludesTmpfsTmp verifies /tmp is a writable tmpfs mount
// (doc-export-sandbox hotfix). weasyprint/fontconfig/pango write to /tmp regardless of
// TMPDIR; without it, --read-only rootfs makes md→pdf export silently produce no PDF.
func TestBuildSpawnConfig_IncludesTmpfsTmp(t *testing.T) {
	resetSeccompPathForTesting()
	seccomp, err := ResolveSeccompPath()
	if err != nil {
		t.Fatalf("seccomp path err = %v", err)
	}
	sc := BuildSpawnConfig(DefaultSandboxConfig, seccomp)
	if !hasTmpfsMount(sc.Tmpfs, "/tmp") {
		t.Errorf("Tmpfs = %v; want a writable /tmp mount for weasyprint PDF export", sc.Tmpfs)
	}
	// /workdir + /skills must remain.
	if !hasTmpfsMount(sc.Tmpfs, "/workdir") {
		t.Errorf("Tmpfs lost /workdir mount: %v", sc.Tmpfs)
	}
	if !hasTmpfsMount(sc.Tmpfs, "/skills") {
		t.Errorf("Tmpfs lost /skills mount: %v", sc.Tmpfs)
	}
}

func TestBuildSpawnConfig_FromDefaults_PassesChecklist(t *testing.T) {
	resetSeccompPathForTesting()
	seccomp, err := ResolveSeccompPath()
	if err != nil {
		t.Fatalf("seccomp path err = %v", err)
	}
	sc := BuildSpawnConfig(DefaultSandboxConfig, seccomp)
	missing := ValidateSecurityChecklist(sc)
	if len(missing) > 0 {
		t.Errorf("DefaultSandboxConfig should pass checklist; missing=%v", missing)
	}
	// Specific assertions
	if sc.User != "1000:1000" {
		t.Errorf("User = %q; want 1000:1000", sc.User)
	}
	if !sc.ReadOnly {
		t.Errorf("ReadOnly = false; want true")
	}
	if !sliceContains(sc.CapDrop, "ALL") {
		t.Errorf("CapDrop = %v; want [ALL]", sc.CapDrop)
	}
	if !sliceContains(sc.CapAdd, "NET_BIND_SERVICE") {
		t.Errorf("CapAdd = %v; want [NET_BIND_SERVICE]", sc.CapAdd)
	}
	if sc.Memory != "512m" {
		t.Errorf("Memory = %q; want 512m", sc.Memory)
	}
	if sc.PIDsLimit != 64 {
		t.Errorf("PIDsLimit = %d; want 64", sc.PIDsLimit)
	}
	// Both /workdir and /skills must be writable tmpfs owned by uid 1000.
	// /skills is required by AcquireForSkill (mkdir + copy skill files); without
	// it the read-only rootfs + root-owned image /skills makes the mkdir fail.
	var hasWorkdir, hasSkills bool
	for _, m := range sc.Tmpfs {
		if strings.HasPrefix(m, "/workdir:") && strings.Contains(m, "uid=1000") {
			hasWorkdir = true
		}
		if strings.HasPrefix(m, "/skills:") && strings.Contains(m, "uid=1000") {
			hasSkills = true
		}
	}
	if !hasWorkdir {
		t.Errorf("Tmpfs missing writable /workdir (uid=1000); got %v", sc.Tmpfs)
	}
	if !hasSkills {
		t.Errorf("Tmpfs missing writable /skills (uid=1000); got %v", sc.Tmpfs)
	}
}

func TestValidateSecurityChecklist_MissingSeccomp(t *testing.T) {
	sc := BuildSpawnConfig(DefaultSandboxConfig, "") // empty seccomp path → security opt omitted
	missing := ValidateSecurityChecklist(sc)
	if !sliceContains(missing, "seccomp profile") {
		t.Errorf("expected 'seccomp profile' in missing list; got %v", missing)
	}
}

func TestValidateSecurityChecklist_AllMissingWhenEmpty(t *testing.T) {
	sc := SpawnConfig{} // empty SpawnConfig
	missing := ValidateSecurityChecklist(sc)
	if len(missing) < 9 {
		t.Errorf("empty SpawnConfig should yield 9+ missing items; got %d: %v", len(missing), missing)
	}
}

func TestValidateSecurityChecklist_RootUserFlagged(t *testing.T) {
	cfg := DefaultSandboxConfig
	cfg.UserSpec = "root"
	resetSeccompPathForTesting()
	seccomp, _ := ResolveSeccompPath()
	sc := BuildSpawnConfig(cfg, seccomp)
	missing := ValidateSecurityChecklist(sc)
	if !sliceContains(missing, "non-root user") {
		t.Errorf("UserSpec=root should be flagged; got missing=%v", missing)
	}
}
