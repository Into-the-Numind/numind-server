package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeControlledFakeBinary(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("controlled lark-cli runner targets Linux and macOS")
	}
	path := filepath.Join(t.TempDir(), "lark-cli")
	script := "#!/bin/sh\nset -eu\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { // #nosec G306 -- executable test fixture
		t.Fatalf("write fake lark-cli: %v", err)
	}
	return path
}

func controlledTestHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatalf("create test HOME: %v", err)
	}
	return home
}

func controlledRunner(binary string) *ControlledLarkCLIRunner {
	return &ControlledLarkCLIRunner{binary: binary}
}

// controlledDeviceAuthCompleteScript validates the real secret-bearing argv in
// the child process, but persists only a redacted snapshot for test assertions.
// A mismatch exits without echoing argv or the secret into test output.
func controlledDeviceAuthCompleteScript(deviceCode, stdout string, exitCode int) string {
	return fmt.Sprintf(`
if [ "$#" -ne 5 ] || [ "$1" != "auth" ] || [ "$2" != "login" ] || [ "$3" != "--device-code" ] || [ "$4" != %s ] || [ "$5" != "--json" ]; then
  exit 97
fi
printf 'auth\000login\000--device-code\000[REDACTED]\000--json\000' > "$HOME/device-auth-argv-redacted"
printf '%%s' %s
exit %d
`, shellQuoteForControlledTest(deviceCode), shellQuoteForControlledTest(stdout), exitCode)
}

func TestControlledLarkCLIRunner_VerifyVersionAcceptsOnlyPinnedVersion(t *testing.T) {
	accepted := []string{
		"1.0.68",
		"v1.0.68",
		"lark-cli 1.0.68",
		"lark-cli v1.0.68",
		"lark-cli version 1.0.68",
		"lark-cli version: v1.0.68",
	}
	for _, output := range accepted {
		t.Run("accept_"+strings.ReplaceAll(output, " ", "_"), func(t *testing.T) {
			bin := writeControlledFakeBinary(t, fmt.Sprintf("printf '%%s\\n' %s", shellQuoteForControlledTest(output)))
			if err := controlledRunner(bin).VerifyVersion(context.Background()); err != nil {
				t.Fatalf("VerifyVersion(%q): %v", output, err)
			}
		})
	}

	rejected := []string{
		"",
		"1.0.67",
		"1.0.680",
		"1.0.68-dev",
		"lark-cli version 1.0.68 update 1.0.69",
		"prefix 1.0.68 suffix",
		"1.0.68\n1.0.69",
	}
	for _, output := range rejected {
		t.Run("reject_"+strings.ReplaceAll(output, " ", "_"), func(t *testing.T) {
			bin := writeControlledFakeBinary(t, fmt.Sprintf("printf '%%s\\n' %s", shellQuoteForControlledTest(output)))
			if err := controlledRunner(bin).VerifyVersion(context.Background()); err == nil {
				t.Fatalf("VerifyVersion(%q) unexpectedly succeeded", output)
			}
		})
	}
}

func TestControlledLarkCLIRunner_VerifyVersionFailsClosedOnExecutionFailure(t *testing.T) {
	bin := writeControlledFakeBinary(t, "printf 'lark-cli version 1.0.68\\n'\nexit 7")
	if err := controlledRunner(bin).VerifyVersion(context.Background()); err == nil {
		t.Fatal("non-zero version command must fail closed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := controlledRunner(bin).VerifyVersion(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled version verification must preserve context cause, got %v", err)
	}
}

func TestControlledLarkCLIRunner_RequiresAbsoluteFixedBinary(t *testing.T) {
	home := controlledTestHome(t)
	r := controlledRunner("lark-cli")
	if err := r.VerifyVersion(context.Background()); err == nil {
		t.Fatal("relative binary path must be rejected")
	}
	result, err := r.Run(context.Background(), home, []string{"docs", "+create"}, nil)
	if err == nil {
		t.Fatal("Run with relative binary path must be rejected")
	}
	if result == nil || result.InvocationStarted {
		t.Fatalf("validation rejection must report InvocationStarted=false, got %+v", result)
	}
}

func TestControlledLarkCLIRunner_LogoutUsesOnlyFixedJSONCommand(t *testing.T) {
	home := controlledTestHome(t)
	bin := writeControlledFakeBinary(t, `
printf '%s\000' "$@" > "$HOME/logout-argv"
printf '{"ok":true,"data":{"loggedOut":true}}\n'
`)
	require.NoError(t, controlledRunner(bin).Logout(context.Background(), home))
	argv, err := os.ReadFile(filepath.Join(home, "logout-argv"))
	require.NoError(t, err)
	require.Equal(t, []byte("auth\x00logout\x00--json\x00"), argv)
}

func TestControlledLarkCLIRunner_RunPreservesArgvStdinAndSafeEnvironment(t *testing.T) {
	home := controlledTestHome(t)
	marker := filepath.Join(t.TempDir(), "shell-injection-marker")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("LANG", "C")
	t.Setenv("HOME", "/must-not-be-inherited")
	t.Setenv("LARKSUITE_CLI_NO_UPDATE_NOTIFIER", "0")
	t.Setenv("LARKSUITE_CLI_NO_SKILLS_NOTIFIER", "0")
	t.Setenv("NUMIND_SECRET_SHOULD_NOT_LEAK", "sensitive-parent-value")

	bin := writeControlledFakeBinary(t, `
mkdir -p "$HOME/observed"
printf '%s\000' "$@" > "$HOME/observed/argv"
env > "$HOME/observed/env"
cat > "$HOME/observed/stdin"
printf '{"ok":true,"data":{"document_id":"doc1"}}\n'
`)
	argv := []string{"docs", "+create", "中文 标题", "; touch " + marker}
	stdin := []byte("{\"内容\":\"逐字传入\"}\n")
	result, err := controlledRunner(bin).Run(context.Background(), home, argv, stdin)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || !result.InvocationStarted || result.ExitCode != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Envelope == nil || !result.Envelope.OK || string(result.Envelope.Data) != `{"document_id":"doc1"}` {
		t.Fatalf("unexpected envelope: %+v", result.Envelope)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("semicolon argv was interpreted by a shell: stat err=%v", err)
	}

	argvRaw, err := os.ReadFile(filepath.Join(home, "observed", "argv"))
	if err != nil {
		t.Fatalf("read observed argv: %v", err)
	}
	wantArgv := []byte(strings.Join(argv, "\x00") + "\x00")
	if !bytes.Equal(argvRaw, wantArgv) {
		t.Fatalf("argv changed across exec boundary:\n got %q\nwant %q", argvRaw, wantArgv)
	}
	stdinRaw, err := os.ReadFile(filepath.Join(home, "observed", "stdin"))
	if err != nil {
		t.Fatalf("read observed stdin: %v", err)
	}
	if !bytes.Equal(stdinRaw, stdin) {
		t.Fatalf("stdin changed across exec boundary: got %q want %q", stdinRaw, stdin)
	}
	envRaw, err := os.ReadFile(filepath.Join(home, "observed", "env"))
	if err != nil {
		t.Fatalf("read observed env: %v", err)
	}
	env := string(envRaw)
	for _, want := range []string{
		"HOME=" + home + "\n",
		"PATH=/usr/bin:/bin\n",
		"LANG=C\n",
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1\n",
		"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1\n",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("safe child env missing %q:\n%s", want, env)
		}
	}
	if strings.Contains(env, "/must-not-be-inherited") || strings.Contains(env, "sensitive-parent-value") {
		t.Fatalf("unsafe parent environment leaked into child:\n%s", env)
	}
}

func TestControlledLarkCLIRunner_RunRejectsInvalidInputBeforeStart(t *testing.T) {
	validHome := controlledTestHome(t)
	bin := writeControlledFakeBinary(t, "touch \"$1\"\nprintf '{\"ok\":true}'")

	missingHome := filepath.Join(t.TempDir(), "missing")
	fileHome := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileHome, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	realHome := controlledTestHome(t)
	symlinkHome := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(realHome, symlinkHome); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		home  string
		argv  []string
		stdin []byte
	}{
		{name: "empty home", home: "", argv: []string{"docs"}},
		{name: "relative home", home: "relative/home", argv: []string{"docs"}},
		{name: "missing home", home: missingHome, argv: []string{"docs"}},
		{name: "file home", home: fileHome, argv: []string{"docs"}},
		{name: "symlink home", home: symlinkHome, argv: []string{"docs"}},
		{name: "empty argv", home: validHome},
		{name: "nul argv", home: validHome, argv: []string{"docs", "bad\x00arg"}},
		{name: "too many argv", home: validHome, argv: make([]string, ControlledLarkCLIMaxArgvCount+1)},
		{name: "argv bytes over limit", home: validHome, argv: []string{strings.Repeat("x", ControlledLarkCLIMaxArgvBytes+1)}},
		{name: "stdin over limit", home: validHome, argv: []string{"docs"}, stdin: bytes.Repeat([]byte("x"), ControlledLarkCLIMaxStdinBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "started")
			argv := tt.argv
			if len(argv) > 0 && tt.name != "too many argv" && tt.name != "argv bytes over limit" && tt.name != "nul argv" {
				argv = append(argv, marker)
			}
			result, err := controlledRunner(bin).Run(context.Background(), tt.home, argv, tt.stdin)
			if err == nil {
				t.Fatal("invalid input unexpectedly started")
			}
			if result == nil || result.InvocationStarted {
				t.Fatalf("validation rejection must preserve InvocationStarted=false, got %+v", result)
			}
			if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("fake binary started before validation completed: %v", statErr)
			}
		})
	}
}

func TestControlledLarkCLIRunner_RunRejectsInvalidStdinJSONBeforeStart(t *testing.T) {
	invalid := []struct {
		name  string
		stdin []byte
	}{
		{name: "not json", stdin: []byte("not-json")},
		{name: "concatenated json", stdin: []byte(`{}{} `)},
		{name: "nul suffix", stdin: []byte("{\"x\":1}\x00")},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			home := controlledTestHome(t)
			marker := filepath.Join(t.TempDir(), "started")
			bin := writeControlledFakeBinary(t, `
touch "$1"
printf '{"ok":true}'
`)
			result, err := controlledRunner(bin).Run(context.Background(), home, []string{marker}, tt.stdin)
			if err == nil {
				t.Fatal("invalid stdin JSON unexpectedly succeeded")
			}
			if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("invalid stdin JSON reached cmd.Start: %v", statErr)
			}
			if result == nil || result.InvocationStarted || result.ExitCode != -1 {
				t.Fatalf("pre-start stdin rejection metadata mismatch: %+v", result)
			}
		})
	}
}

func TestControlledLarkCLIRunner_RunPassesWhitespaceWrappedJSONUnchanged(t *testing.T) {
	home := controlledTestHome(t)
	marker := filepath.Join(t.TempDir(), "started")
	captured := filepath.Join(t.TempDir(), "stdin")
	bin := writeControlledFakeBinary(t, `
touch "$1"
cat > "$2"
printf '{"ok":true}'
`)
	stdin := []byte(" \n\t{\"x\":[1,true]}\r\n")
	result, err := controlledRunner(bin).Run(context.Background(), home, []string{marker, captured}, stdin)
	if err != nil {
		t.Fatalf("valid whitespace-wrapped JSON: %v", err)
	}
	if result == nil || !result.InvocationStarted || result.ExitCode != 0 {
		t.Fatalf("valid stdin invocation metadata mismatch: %+v", result)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("valid stdin did not reach cmd.Start: %v", err)
	}
	got, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	if !bytes.Equal(got, stdin) {
		t.Fatalf("valid stdin was rewritten: got %q want %q", got, stdin)
	}
}

func TestControlledLarkCLIRunner_RunStartFailureReportsNotStarted(t *testing.T) {
	home := controlledTestHome(t)
	r := controlledRunner(filepath.Join(t.TempDir(), "missing-lark-cli"))
	result, err := r.Run(context.Background(), home, []string{"docs", "+create"}, nil)
	if err == nil {
		t.Fatal("missing binary must fail to start")
	}
	if result == nil || result.InvocationStarted || result.ExitCode != -1 {
		t.Fatalf("Start failure must report InvocationStarted=false and exit=-1, got %+v", result)
	}
}

func TestControlledLarkCLIRunner_RunRejectsMalformedOrIncompleteEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
	}{
		{name: "empty", stdout: ""},
		{name: "non json", stdout: "not-json"},
		{name: "array", stdout: `[{"ok":true}]`},
		{name: "missing ok", stdout: `{"data":{}}`},
		{name: "trailing object", stdout: `{"ok":true}{"ok":true}`},
		{name: "trailing junk", stdout: `{"ok":true}junk`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := controlledTestHome(t)
			bin := writeControlledFakeBinary(t, fmt.Sprintf("printf '%%s' %s", shellQuoteForControlledTest(tt.stdout)))
			result, err := controlledRunner(bin).Run(context.Background(), home, []string{"docs", "+get"}, nil)
			if err == nil {
				t.Fatalf("malformed stdout %q unexpectedly succeeded", tt.stdout)
			}
			if result == nil || !result.InvocationStarted {
				t.Fatalf("started malformed-output invocation lost start state: %+v", result)
			}
			if !bytes.Equal(result.Stdout, []byte(tt.stdout)) {
				t.Fatalf("raw bounded stdout not preserved: got %q want %q", result.Stdout, tt.stdout)
			}
		})
	}
}

func TestControlledLarkCLIRunner_RunPreservesJSONDecodeCause(t *testing.T) {
	t.Run("syntax", func(t *testing.T) {
		home := controlledTestHome(t)
		bin := writeControlledFakeBinary(t, "printf 'not-json'")
		result, err := controlledRunner(bin).Run(context.Background(), home, []string{"docs", "+get"}, nil)
		if result == nil || !result.InvocationStarted {
			t.Fatalf("malformed-output invocation lost start state: %+v", result)
		}
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) {
			t.Fatalf("JSON decoder cause must remain in the %%w chain, got %v", err)
		}
	})
	t.Run("ok type", func(t *testing.T) {
		home := controlledTestHome(t)
		bin := writeControlledFakeBinary(t, `printf '{"ok":"yes"}'`)
		_, err := controlledRunner(bin).Run(context.Background(), home, []string{"docs", "+get"}, nil)
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			t.Fatalf("ok type cause must remain in the %%w chain, got %v", err)
		}
	})
}

func TestControlledLarkCLIRunner_RunExitZeroOKFalseIsFailure(t *testing.T) {
	home := controlledTestHome(t)
	const stdout = `{"ok":false,"error":{"type":"authorization","subtype":"missing_scope","code":"99991672","message":"sensitive resource detail","identity":"user","console_url":"https://example.invalid/private","missing_scopes":["docx:document:create"],"permission_violations":[{"level":"app"}]}}`
	bin := writeControlledFakeBinary(t, fmt.Sprintf("printf '%%s' %s", shellQuoteForControlledTest(stdout)))
	result, err := controlledRunner(bin).Run(context.Background(), home, []string{"docs", "+create"}, nil)
	if err == nil {
		t.Fatal("exit 0 with ok=false must fail")
	}
	if strings.Contains(err.Error(), "sensitive resource detail") || strings.Contains(err.Error(), "example.invalid") {
		t.Fatalf("business error leaked sensitive envelope details: %v", err)
	}
	if result == nil || !result.InvocationStarted || result.ExitCode != 0 || result.Envelope == nil || result.Envelope.Error == nil {
		t.Fatalf("structured business error was not preserved: %+v", result)
	}
	cliErr := result.Envelope.Error
	if cliErr.Type != "authorization" || cliErr.Subtype != "missing_scope" || cliErr.Identity != "user" || cliErr.ConsoleURL == "" {
		t.Fatalf("classifier fields lost: %+v", cliErr)
	}
	if len(cliErr.MissingScopes) != 1 || cliErr.MissingScopes[0] != "docx:document:create" || len(cliErr.PermissionViolations) == 0 {
		t.Fatalf("classifier collections lost: %+v", cliErr)
	}
}

func TestControlledLarkCLIRunner_PreservesTopLevelIdentity(t *testing.T) {
	home := controlledTestHome(t)
	const stdout = `{"ok":false,"identity":"user","error":{"type":"authentication","subtype":"token_missing","message":"sensitive","user_open_id":"ou_sensitive"}}`
	bin := writeControlledFakeBinary(t, fmt.Sprintf("printf '%%s' %s", shellQuoteForControlledTest(stdout)))

	result, err := controlledRunner(bin).Run(context.Background(), home, []string{"wiki", "+node-list"}, nil)
	if err == nil {
		t.Fatal("ok=false must fail")
	}
	if result == nil || result.Envelope == nil {
		t.Fatalf("structured envelope was not preserved: %+v", result)
	}
	if result.Envelope.Identity != "user" {
		t.Fatalf("top-level identity lost: got %q", result.Envelope.Identity)
	}
	if strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "ou_sensitive") {
		t.Fatalf("sensitive envelope fields leaked through error: %v", err)
	}
}

func TestControlledLarkCLIRunner_RunNonZeroPreservesEnvelopeExitAndBoundedStderr(t *testing.T) {
	home := controlledTestHome(t)
	const stdout = `{"ok":false,"error":{"type":"api","subtype":"permission_denied","code":403,"message":"no scope"}}`
	const stderr = "sensitive diagnostic must not enter error"
	bin := writeControlledFakeBinary(t, fmt.Sprintf("printf '%%s' %s\nprintf '%%s' %s >&2\nexit 3",
		shellQuoteForControlledTest(stdout), shellQuoteForControlledTest(stderr)))
	result, err := controlledRunner(bin).Run(context.Background(), home, []string{"docs", "+create"}, nil)
	if err == nil {
		t.Fatal("non-zero exit must fail")
	}
	if strings.Contains(err.Error(), stderr) {
		t.Fatalf("stderr leaked into returned error: %v", err)
	}
	if result == nil || !result.InvocationStarted || result.ExitCode != 3 {
		t.Fatalf("non-zero result metadata lost: %+v", result)
	}
	if result.Envelope == nil || result.Envelope.Error == nil || result.Envelope.Error.Subtype != "permission_denied" {
		t.Fatalf("non-zero structured envelope lost: %+v", result.Envelope)
	}
	if string(result.Stdout) != stdout || string(result.Stderr) != stderr {
		t.Fatalf("raw bounded streams lost: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

func TestControlledLarkCLIRunner_RunBoundsStdoutAndStderrSeparately(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantStdout   int
		wantStderr   int
		stdoutCutoff bool
		stderrCutoff bool
	}{
		{
			name:         "stdout",
			body:         fmt.Sprintf("dd if=/dev/zero bs=65536 count=%d 2>/dev/null", ControlledLarkCLIMaxStdoutBytes/(64<<10)+1),
			wantStdout:   ControlledLarkCLIMaxStdoutBytes,
			stdoutCutoff: true,
		},
		{
			name:         "stderr",
			body:         fmt.Sprintf("dd if=/dev/zero bs=65536 count=%d >&2 2>/dev/null\nprintf '{\\\"ok\\\":true}'", ControlledLarkCLIMaxStderrBytes/(64<<10)+1),
			wantStdout:   len(`{"ok":true}`),
			wantStderr:   ControlledLarkCLIMaxStderrBytes,
			stderrCutoff: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := controlledTestHome(t)
			bin := writeControlledFakeBinary(t, tt.body)
			result, err := controlledRunner(bin).Run(context.Background(), home, []string{"docs", "+get"}, nil)
			if err == nil {
				t.Fatal("over-limit output must fail closed")
			}
			if result == nil || !result.InvocationStarted {
				t.Fatalf("output-limit failure lost started state: %+v", result)
			}
			if len(result.Stdout) != tt.wantStdout || len(result.Stderr) != tt.wantStderr {
				t.Fatalf("bounded lengths mismatch: stdout=%d stderr=%d", len(result.Stdout), len(result.Stderr))
			}
			if result.StdoutTruncated != tt.stdoutCutoff || result.StderrTruncated != tt.stderrCutoff {
				t.Fatalf("truncation flags mismatch: %+v", result)
			}
		})
	}
}

func TestControlledLarkCLIRunner_RunContextCancellationKillsWholeProcessGroup(t *testing.T) {
	home := controlledTestHome(t)
	marker := filepath.Join(t.TempDir(), "orphan-child-marker")
	bin := writeControlledFakeBinary(t, `
printf started > "$HOME/started"
(sleep 0.25; touch "$1") &
wait
`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var result *CLIResult
	var runErr error
	go func() {
		result, runErr = controlledRunner(bin).Run(ctx, home, []string{marker}, nil)
		close(done)
	}()
	waitForControlledFile(t, filepath.Join(home, "started"), time.Second)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not reap cancelled process group")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("cancel cause not preserved: %v", runErr)
	}
	if result == nil || !result.InvocationStarted {
		t.Fatalf("cancelled invocation lost started state: %+v", result)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancel left a live child process: %v", err)
	}
}

func TestControlledLarkCLIRunner_RunTimeoutKillsWholeProcessGroup(t *testing.T) {
	home := controlledTestHome(t)
	marker := filepath.Join(t.TempDir(), "timeout-child-marker")
	bin := writeControlledFakeBinary(t, `
(sleep 0.25; touch "$1") &
wait
`)
	r := controlledRunner(bin)
	r.timeout = 50 * time.Millisecond
	result, err := r.Run(context.Background(), home, []string{marker}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runner timeout cause not preserved: %v", err)
	}
	if result == nil || !result.InvocationStarted {
		t.Fatalf("timed-out invocation lost started state: %+v", result)
	}
	time.Sleep(400 * time.Millisecond)
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("timeout left a live child process: %v", statErr)
	}
}

func TestControlledLarkCLIRunner_RunNormalExitDoesNotLeaveBackgroundChild(t *testing.T) {
	home := controlledTestHome(t)
	marker := filepath.Join(t.TempDir(), "normal-exit-child-marker")
	bin := writeControlledFakeBinary(t, `
(sleep 0.25; touch "$1") &
printf '{"ok":true,"data":{}}\n'
exit 0
`)
	result, err := controlledRunner(bin).Run(context.Background(), home, []string{marker}, nil)
	if err != nil {
		t.Fatalf("normal run: %v", err)
	}
	if result == nil || !result.InvocationStarted {
		t.Fatalf("normal invocation lost started state: %+v", result)
	}
	time.Sleep(400 * time.Millisecond)
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("normal leader exit left a live background child: %v", statErr)
	}
}

func shellQuoteForControlledTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func waitForControlledFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
