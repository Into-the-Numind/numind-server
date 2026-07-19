package feishu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	deviceAuthTestCode = "secret-device-code"
	deviceAuthTestURL  = "https://open.feishu.cn/suite/passport/oauth/device?user_code=SAFE-CODE"
)

func TestControlledLarkCLIRunner_StartUserAuthStrictFixture(t *testing.T) {
	home := controlledTestHome(t)
	const fixture = `{"verification_url":"https://open.feishu.cn/suite/passport/oauth/device?user_code=SAFE-CODE","device_code":"secret-device-code","expires_in":600,"hint":"agent guidance"}`
	bin := writeControlledFakeBinary(t, fmt.Sprintf(`
printf '%%s\000' "$@" > "$HOME/device-auth-start-argv"
printf '%%s' %s
`, shellQuoteForControlledTest(fixture)))
	runner := controlledRunner(bin)

	start, err := runner.StartUserAuth(context.Background(), home, []string{
		"wiki:wiki:readonly",
		"offline_access",
		"docx:document:readonly",
		"wiki:wiki:readonly",
	})
	require.NoError(t, err)
	require.Equal(t, DeviceAuthStart{
		VerificationURL: deviceAuthTestURL,
		DeviceCode:      deviceAuthTestCode,
		ExpiresIn:       10 * time.Minute,
	}, start)

	argv, err := os.ReadFile(filepath.Join(home, "device-auth-start-argv"))
	require.NoError(t, err)
	require.Equal(t, []byte("auth\x00login\x00--scope\x00docx:document:readonly offline_access wiki:wiki:readonly\x00--no-wait\x00--json\x00"), argv)
	var contract DeviceAuthCLI = runner
	require.Same(t, runner, contract)

	t.Run("process start failure is a safe dependency error", func(t *testing.T) {
		missing := controlledRunner(filepath.Join(t.TempDir(), "missing-lark-cli"))
		got, startErr := missing.StartUserAuth(context.Background(), controlledTestHome(t), []string{"offline_access"})
		require.Empty(t, got)
		require.ErrorIs(t, startErr, errDeviceAuthCLIDependency)
		require.NotContains(t, startErr.Error(), "offline_access")
	})
}

// Regression: lark-cli v1.0.68 auth login --no-wait --json emits a
// command-specific object, not the generic {ok,identity,data} envelope used by
// business commands.
func TestControlledLarkCLIRunner_StartUserAuthAcceptsPinnedCLI1068Output(t *testing.T) {
	home := controlledTestHome(t)
	const fixture = `{"verification_url":"https://open.feishu.cn/suite/passport/oauth/device?user_code=SAFE-CODE","device_code":"secret-device-code","expires_in":600,"hint":"agent guidance"}`
	bin := writeControlledFakeBinary(t, fmt.Sprintf("printf '%%s' %s", shellQuoteForControlledTest(fixture)))

	start, err := controlledRunner(bin).StartUserAuth(context.Background(), home, []string{"offline_access"})
	require.NoError(t, err)
	require.Equal(t, DeviceAuthStart{
		VerificationURL: deviceAuthTestURL,
		DeviceCode:      deviceAuthTestCode,
		ExpiresIn:       10 * time.Minute,
	}, start)
}

// Regression: the real Feishu v1.0.68 device endpoint returns the accounts
// verification host/path plus both flow_id and user_code. Treating the older
// open.feishu.cn page shape as the only valid URL turns a successful CLI start
// into an Internal server error before the browser can authorize.
func TestControlledLarkCLIRunner_StartUserAuthAcceptsRealFeishu1068VerificationURL(t *testing.T) {
	home := controlledTestHome(t)
	const verificationURL = "https://accounts.feishu.cn/oauth/v1/device/verify?flow_id=opaque-flow&user_code=SAFE-CODE"
	const fixture = `{"verification_url":"https://accounts.feishu.cn/oauth/v1/device/verify?flow_id=opaque-flow&user_code=SAFE-CODE","device_code":"secret-device-code","expires_in":600,"hint":"agent guidance"}`
	bin := writeControlledFakeBinary(t, fmt.Sprintf("printf '%%s' %s", shellQuoteForControlledTest(fixture)))

	start, err := controlledRunner(bin).StartUserAuth(context.Background(), home, []string{"offline_access"})
	require.NoError(t, err)
	require.Equal(t, verificationURL, start.VerificationURL)
}

// Regression: the resumed v1.0.68 command also emits a command-specific
// authorization_complete object rather than a generic envelope.
func TestControlledLarkCLIRunner_CompleteUserAuthAcceptsPinnedCLI1068Output(t *testing.T) {
	home := controlledTestHome(t)
	const fixture = `{"event":"authorization_complete","user_open_id":"ou_test","user_name":"tester","scope":"offline_access","requested":["offline_access"],"newly_granted":["offline_access"],"already_granted":[],"missing":[],"granted":["offline_access"]}`
	bin := writeControlledFakeBinary(t, controlledDeviceAuthCompleteScript(deviceAuthTestCode, fixture, 0))

	outcome, err := controlledRunner(bin).CompleteUserAuth(context.Background(), home, deviceAuthTestCode, []string{"offline_access"})
	require.NoError(t, err)
	require.Equal(t, DeviceAuthCompleted, outcome)
}

// Regression: Numind deliberately discards the temporary HOME used by the
// --no-wait start. lark-cli v1.0.68 stores its requested-scope cache only in
// that HOME, so a successful later --device-code call emits requested=[] even
// though its final granted scopes and user identity are valid. This official
// shape must reach candidate-HOME reconciliation instead of terminalizing the
// durable session and returning HTTP 500.
func TestClassifyDeviceAuthCompletionResult_OfficialNoWaitCacheLossRequiresReconciliation(t *testing.T) {
	result := &CLIResult{
		InvocationStarted: true,
		ExitCode:          0,
		Stdout:            []byte(`{"event":"authorization_complete","user_open_id":"ou_test","user_name":"tester","scope":"offline_access","requested":[],"newly_granted":[],"already_granted":[],"missing":[],"granted":["offline_access"]}`),
	}

	require.Equal(t, DeviceAuthAmbiguous, classifyDeviceAuthCompletionResult(result, nil))
}

func TestControlledLarkCLIRunner_CompleteUserAuthAcceptsOfficialNoWaitCacheLossWithDurableScopes(t *testing.T) {
	home := controlledTestHome(t)
	const fixture = `{"event":"authorization_complete","user_open_id":"ou_test","user_name":"tester","scope":"docx:document docx:document:readonly offline_access","requested":[],"newly_granted":[],"already_granted":[],"missing":[],"granted":["docx:document","docx:document:readonly","offline_access"]}`
	bin := writeControlledFakeBinary(t, controlledDeviceAuthCompleteScript(deviceAuthTestCode, fixture, 0))

	outcome, err := controlledRunner(bin).CompleteUserAuth(
		context.Background(), home, deviceAuthTestCode,
		[]string{"docx:document", "docx:document:readonly", "offline_access"},
	)
	require.NoError(t, err)
	require.Equal(t, DeviceAuthCompleted, outcome)

	underGranted, err := controlledRunner(bin).CompleteUserAuth(
		context.Background(), home, deviceAuthTestCode,
		[]string{"docx:document", "docx:document:readonly", "offline_access", "drive:drive"},
	)
	require.NoError(t, err)
	require.Equal(t, DeviceAuthProtocolFailure, underGranted)
}

func TestControlledLarkCLIRunner_CompleteUserAuthBindsCachedRequestToDurableScopes(t *testing.T) {
	home := controlledTestHome(t)
	const fixture = `{"event":"authorization_complete","user_open_id":"ou_test","user_name":"tester","scope":"docx:document:readonly offline_access","requested":["docx:document:readonly"],"newly_granted":["docx:document:readonly"],"already_granted":[],"missing":[],"granted":["docx:document:readonly","offline_access"]}`
	bin := writeControlledFakeBinary(t, controlledDeviceAuthCompleteScript(deviceAuthTestCode, fixture, 0))

	superset, err := controlledRunner(bin).CompleteUserAuth(
		context.Background(), home, deviceAuthTestCode, []string{"docx:document:readonly"},
	)
	require.NoError(t, err)
	require.Equal(t, DeviceAuthCompleted, superset)

	mismatched, err := controlledRunner(bin).CompleteUserAuth(
		context.Background(), home, deviceAuthTestCode, []string{"offline_access"},
	)
	require.NoError(t, err)
	require.Equal(t, DeviceAuthProtocolFailure, mismatched)
}

func TestControlledLarkCLIRunner_CompleteUserAuthOutcomeMatrix(t *testing.T) {
	tests := []struct {
		name     string
		stdout   string
		exitCode int
		want     DeviceAuthOutcome
	}{
		{name: "success", stdout: `{"event":"authorization_complete","user_open_id":"ou_test","user_name":"tester","scope":"offline_access","requested":["offline_access"],"newly_granted":["offline_access"],"already_granted":[],"missing":[],"granted":["offline_access"]}`, want: DeviceAuthCompleted},
		{name: "nonzero completion requires home reconciliation", stdout: `{"ok":false,"error":{"type":"authentication","subtype":"unknown"}}`, exitCode: 3, want: DeviceAuthAmbiguous},
		{name: "unknown successful event fails closed", stdout: `{"event":"unknown","user_open_id":"ou_test","user_name":"tester","scope":"offline_access","requested":["offline_access"],"newly_granted":[],"already_granted":["offline_access"],"missing":[],"granted":["offline_access"]}`, want: DeviceAuthProtocolFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := controlledTestHome(t)
			bin := writeControlledFakeBinary(t, controlledDeviceAuthCompleteScript(deviceAuthTestCode, test.stdout, test.exitCode))

			outcome, err := controlledRunner(bin).CompleteUserAuth(context.Background(), home, deviceAuthTestCode, []string{"offline_access"})
			require.NoError(t, err)
			require.Equal(t, test.want, outcome)
			snapshot, readErr := os.ReadFile(filepath.Join(home, "device-auth-argv-redacted"))
			require.NoError(t, readErr)
			require.Equal(t, []byte("auth\x00login\x00--device-code\x00[REDACTED]\x00--json\x00"), snapshot)
			require.NotContains(t, string(snapshot), deviceAuthTestCode)
		})
	}

	t.Run("timeout after start is ambiguous", func(t *testing.T) {
		home := controlledTestHome(t)
		bin := writeControlledFakeBinary(t, `
printf 'started' > "$HOME/device-auth-started"
sleep 2
`)
		runner := controlledRunner(bin)
		runner.timeout = 30 * time.Millisecond
		outcome, err := runner.CompleteUserAuth(context.Background(), home, deviceAuthTestCode, []string{"offline_access"})
		require.NoError(t, err)
		require.Equal(t, DeviceAuthAmbiguous, outcome)
	})

	t.Run("timeout wins over truncated output", func(t *testing.T) {
		home := controlledTestHome(t)
		bin := writeControlledFakeBinary(t, fmt.Sprintf(`
dd if=/dev/zero bs=65536 count=%d >&2 2>/dev/null
sleep 2
`, ControlledLarkCLIMaxStderrBytes/(64<<10)+1))
		runner := controlledRunner(bin)
		runner.timeout = 100 * time.Millisecond
		outcome, err := runner.CompleteUserAuth(context.Background(), home, deviceAuthTestCode, []string{"offline_access"})
		require.NoError(t, err)
		require.Equal(t, DeviceAuthAmbiguous, outcome)
	})

	t.Run("nonzero exit with truncated output requires home reconciliation", func(t *testing.T) {
		home := controlledTestHome(t)
		bin := writeControlledFakeBinary(t, fmt.Sprintf(`
dd if=/dev/zero bs=65536 count=%d >&2 2>/dev/null
exit 3
`, ControlledLarkCLIMaxStderrBytes/(64<<10)+1))
		outcome, err := controlledRunner(bin).CompleteUserAuth(context.Background(), home, deviceAuthTestCode, []string{"offline_access"})
		require.NoError(t, err)
		require.Equal(t, DeviceAuthAmbiguous, outcome)
	})

	t.Run("unproven runner cleanup is ambiguous", func(t *testing.T) {
		pending := []byte(`{"ok":false,"identity":"user","error":{"type":"authorization","subtype":"authorization_pending"}}`)
		for _, test := range []struct {
			name   string
			result *CLIResult
			runErr error
		}{
			{
				name:   "reap error after exit zero",
				result: &CLIResult{InvocationStarted: true, ExitCode: 0, Stdout: pending},
				runErr: errors.New("process group cleanup unproven"),
			},
			{
				name:   "negative exit cannot be a safe business completion",
				result: &CLIResult{InvocationStarted: true, ExitCode: -1, Stdout: pending},
				runErr: errControlledCLIBusiness,
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				require.Equal(t, DeviceAuthAmbiguous, classifyDeviceAuthCompletionResult(test.result, test.runErr))
			})
		}
	})

	t.Run("process start failure is retryable dependency", func(t *testing.T) {
		home := controlledTestHome(t)
		runner := controlledRunner(filepath.Join(t.TempDir(), "missing-lark-cli"))
		outcome, err := runner.CompleteUserAuth(context.Background(), home, deviceAuthTestCode, []string{"offline_access"})
		require.NoError(t, err)
		require.Equal(t, DeviceAuthRetryableDependency, outcome)
	})
}

func TestClassifyDeviceAuthCompletionResult_PreservesSafePollingDiagnosis(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   DeviceAuthOutcome
	}{
		{name: "ordinary pending timeout", want: DeviceAuthOutcome("polling_pending_timeout")},
		{
			name: "provider network warning",
			stderr: "[lark-cli] [WARN] device-flow: poll network error: redacted upstream detail\n",
			want: DeviceAuthOutcome("polling_network_failure"),
		},
		{
			name: "provider read warning",
			stderr: "[lark-cli] [WARN] device-flow: poll read error: redacted upstream detail\n",
			want: DeviceAuthOutcome("polling_read_failure"),
		},
		{
			name: "provider parse warning",
			stderr: "[lark-cli] [WARN] device-flow: poll parse error: redacted upstream detail\n",
			want: DeviceAuthOutcome("polling_parse_failure"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := &CLIResult{InvocationStarted: true, ExitCode: -1, Stderr: []byte(test.stderr)}
			require.Equal(t, test.want, classifyDeviceAuthCompletionResult(result, context.DeadlineExceeded))
		})
	}
}

func TestControlledLarkCLIRunner_DeviceCodeNeverAppearsInError(t *testing.T) {
	secret := "device-secret-MUST-NEVER-LEAK"
	tests := []struct {
		name string
		body string
	}{
		{name: "business failure", body: controlledDeviceAuthCompleteScript(secret, `{"ok":false,"identity":"user","error":{"type":"authorization","subtype":"unknown"}}`, 3)},
		{name: "malformed output", body: controlledDeviceAuthCompleteScript(secret, `not-json`, 0)},
		{name: "process stderr", body: `printf 'private process diagnostic' >&2; exit 7`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := controlledTestHome(t)
			bin := writeControlledFakeBinary(t, test.body)
			outcome, err := controlledRunner(bin).CompleteUserAuth(context.Background(), home, secret, []string{"offline_access"})
			require.NotContains(t, outcome.String(), secret)
			if err != nil {
				require.NotContains(t, err.Error(), secret)
			}
			if snapshot, readErr := os.ReadFile(filepath.Join(home, "device-auth-argv-redacted")); readErr == nil {
				require.NotContains(t, string(snapshot), secret)
			}
		})
	}

	home := controlledTestHome(t)
	marker := filepath.Join(home, "must-not-start")
	bin := writeControlledFakeBinary(t, fmt.Sprintf("touch %s", shellQuoteForControlledTest(marker)))
	outcome, err := controlledRunner(bin).CompleteUserAuth(context.Background(), home, "", []string{"offline_access"})
	require.NoError(t, err)
	require.Equal(t, DeviceAuthProtocolFailure, outcome)
	require.NoFileExists(t, marker)
}

func TestControlledLarkCLIRunner_RejectsMalformedDeviceAuthOutput(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		tests := []struct {
			name   string
			stdout string
		}{
			{name: "trailing object", stdout: `{"device_code":"secret-device-code","verification_url":"https://open.feishu.cn/suite/passport/oauth/device?user_code=SAFE-CODE","expires_in":600,"hint":"safe"} {"extra":true}`},
			{name: "duplicate field", stdout: `{"device_code":"secret-device-code","device_code":"secret-device-code","verification_url":"https://open.feishu.cn/suite/passport/oauth/device?user_code=SAFE-CODE","expires_in":600,"hint":"safe"}`},
			{name: "unknown field", stdout: `{"device_code":"secret-device-code","verification_url":"https://open.feishu.cn/suite/passport/oauth/device?user_code=SAFE-CODE","expires_in":600,"hint":"safe","extra":true}`},
			{name: "bad URL", stdout: `{"device_code":"secret-device-code","verification_url":"http://evil.invalid/device","expires_in":600,"hint":"safe"}`},
			{name: "extra URL query key", stdout: `{"device_code":"secret-device-code","verification_url":"https://open.feishu.cn/suite/passport/oauth/device?user_code=SAFE-CODE&redirect_uri=https%3A%2F%2Fevil.invalid","expires_in":600,"hint":"safe"}`},
			{name: "zero expiry", stdout: `{"device_code":"secret-device-code","verification_url":"https://open.feishu.cn/suite/passport/oauth/device?user_code=SAFE-CODE","expires_in":0,"hint":"safe"}`},
			{name: "oversized expiry", stdout: `{"device_code":"secret-device-code","verification_url":"https://open.feishu.cn/suite/passport/oauth/device?user_code=SAFE-CODE","expires_in":721,"hint":"safe"}`},
			{name: "overflowing expiry", stdout: `{"device_code":"secret-device-code","verification_url":"https://open.feishu.cn/suite/passport/oauth/device?user_code=SAFE-CODE","expires_in":9223372036854775807,"hint":"safe"}`},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				home := controlledTestHome(t)
				bin := writeControlledFakeBinary(t, fmt.Sprintf("printf '%%s' %s", shellQuoteForControlledTest(test.stdout)))
				start, err := controlledRunner(bin).StartUserAuth(context.Background(), home, []string{"offline_access"})
				require.Error(t, err)
				require.Empty(t, start)
				require.NotContains(t, err.Error(), deviceAuthTestCode)
			})
		}
	})

	t.Run("complete", func(t *testing.T) {
		tests := []struct {
			name     string
			stdout   string
			exitCode int
		}{
			{name: "trailing object", stdout: `{"event":"authorization_complete"} {"extra":true}`},
			{name: "duplicate field", stdout: `{"event":"authorization_complete","event":"authorization_complete"}`},
			{name: "unknown field", stdout: `{"event":"authorization_complete","extra":true}`},
			{name: "secret field", stdout: `{"event":"authorization_complete","token":"must-not-be-accepted"}`},
			{name: "missing required identity evidence", stdout: `{"event":"authorization_complete","scope":"offline_access","requested":[],"newly_granted":[],"already_granted":[],"missing":[],"granted":["offline_access"]}`},
			{name: "missing permission evidence fields", stdout: `{"event":"authorization_complete","user_open_id":"ou_test","user_name":"tester"}`},
			{name: "missing requested scope", stdout: `{"event":"authorization_complete","user_open_id":"ou_test","user_name":"tester","scope":"offline_access","requested":["offline_access"],"newly_granted":[],"already_granted":[],"missing":["offline_access"],"granted":["offline_access"]}`},
			{name: "requested classification mismatch", stdout: `{"event":"authorization_complete","user_open_id":"ou_test","user_name":"tester","scope":"offline_access","requested":["offline_access"],"newly_granted":[],"already_granted":[],"missing":[],"granted":["offline_access"]}`},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				home := controlledTestHome(t)
				bin := writeControlledFakeBinary(t, controlledDeviceAuthCompleteScript(deviceAuthTestCode, test.stdout, test.exitCode))
				outcome, err := controlledRunner(bin).CompleteUserAuth(context.Background(), home, deviceAuthTestCode, []string{"offline_access"})
				require.NoError(t, err)
				require.Equal(t, DeviceAuthProtocolFailure, outcome)
			})
		}
	})

	t.Run("fixed output limits", func(t *testing.T) {
		tests := []struct {
			name string
			body string
		}{
			{name: "stdout", body: fmt.Sprintf("dd if=/dev/zero bs=65536 count=%d 2>/dev/null", deviceAuthCLIMaxJSONBytes/(64<<10)+1)},
			{name: "stderr", body: fmt.Sprintf("dd if=/dev/zero bs=65536 count=%d >&2 2>/dev/null\nprintf '{\\\"event\\\":\\\"authorization_complete\\\"}'", ControlledLarkCLIMaxStderrBytes/(64<<10)+1)},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				home := controlledTestHome(t)
				bin := writeControlledFakeBinary(t, test.body)
				outcome, err := controlledRunner(bin).CompleteUserAuth(context.Background(), home, deviceAuthTestCode, []string{"offline_access"})
				require.NoError(t, err)
				require.Equal(t, DeviceAuthProtocolFailure, outcome)
			})
		}
	})

	t.Run("start rejects invalid scopes before process start", func(t *testing.T) {
		for _, scopes := range [][]string{nil, {"im:message:send"}, {" scope"}, {strings.Repeat("s", 129)}} {
			home := controlledTestHome(t)
			marker := filepath.Join(home, "started")
			bin := writeControlledFakeBinary(t, fmt.Sprintf("touch %s", shellQuoteForControlledTest(marker)))
			_, err := controlledRunner(bin).StartUserAuth(context.Background(), home, scopes)
			require.Error(t, err)
			require.NoFileExists(t, marker)
		}
	})

}
