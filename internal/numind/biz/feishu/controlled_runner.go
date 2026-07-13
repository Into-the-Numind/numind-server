package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// LarkCLIVersion is the only lark-cli release this adapter accepts.
	LarkCLIVersion = "1.0.68"

	controlledLarkCLIBinary = "/usr/local/bin/lark-cli"

	// ControlledLarkCLITimeout is the hard ceiling for one business invocation.
	ControlledLarkCLITimeout = 30 * time.Second
	// ControlledLarkCLIVersionTimeout bounds the startup version probe.
	ControlledLarkCLIVersionTimeout = 5 * time.Second

	// ControlledLarkCLIMaxArgvCount bounds process argument count.
	ControlledLarkCLIMaxArgvCount = 128
	// ControlledLarkCLIMaxArgvBytes bounds the aggregate bytes in all arguments.
	ControlledLarkCLIMaxArgvBytes = 256 << 10
	// ControlledLarkCLIMaxStdinBytes bounds the JSON request written to stdin.
	ControlledLarkCLIMaxStdinBytes = 1 << 20
	// ControlledLarkCLIMaxStdoutBytes bounds retained response bytes.
	ControlledLarkCLIMaxStdoutBytes = 4 << 20
	// ControlledLarkCLIMaxStderrBytes bounds retained diagnostic bytes.
	ControlledLarkCLIMaxStderrBytes = 256 << 10

	controlledProcessGroupWait = time.Second
)

var (
	errControlledCLIInvalidInput = errors.New("controlled lark-cli input rejected")
	errControlledCLIOutputLimit  = errors.New("controlled lark-cli output limit exceeded")
	errControlledCLIInvalidJSON  = errors.New("controlled lark-cli returned an invalid envelope")
	errControlledCLIBusiness     = errors.New("controlled lark-cli reported a business error")
)

// CLIError is the structured, classifier-safe subset of a lark-cli error.
// Raw fields are retained for exact fixed-version classification without rendering
// their potentially sensitive contents into returned error strings or logs.
type CLIError struct {
	Type                 string          `json:"type,omitempty"`
	Subtype              string          `json:"subtype,omitempty"`
	Code                 json.RawMessage `json:"code,omitempty"`
	Message              string          `json:"message,omitempty"`
	Identity             string          `json:"identity,omitempty"`
	ConsoleURL           string          `json:"console_url,omitempty"`
	MissingScopes        []string        `json:"missing_scopes,omitempty"`
	PermissionViolations json.RawMessage `json:"permission_violations,omitempty"`
	Details              json.RawMessage `json:"details,omitempty"`
	Hint                 json.RawMessage `json:"hint,omitempty"`
}

// CLIEnvelope is the machine-readable lark-cli result contract.
type CLIEnvelope struct {
	OK       bool            `json:"ok"`
	Identity string          `json:"identity,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Error    *CLIError       `json:"error,omitempty"`
}

// CLIResult preserves the bounded process evidence needed by the operation state
// machine. InvocationStarted becomes true immediately after cmd.Start succeeds and
// is never cleared, including on cancellation, timeout, malformed output, or Wait
// failure.
type CLIResult struct {
	InvocationStarted bool
	ExitCode          int
	Stdout            []byte
	Stderr            []byte
	StdoutTruncated   bool
	StderrTruncated   bool
	Envelope          *CLIEnvelope
}

// ControlledLarkCLIRunner executes one fixed lark-cli binary without a shell.
// The zero value uses the image-pinned absolute binary. binary and timeout exist
// only as package-private seams for contract tests; production leaves them unset.
type ControlledLarkCLIRunner struct {
	binary  string
	timeout time.Duration
}

// VerifyVersion fails closed unless the fixed binary reports exactly 1.0.68 in a
// known lark-cli --version presentation.
func (r *ControlledLarkCLIRunner) VerifyVersion(ctx context.Context) error {
	binary, err := r.binaryPath()
	if err != nil {
		return err
	}
	result, waitErr, processErr := r.runProcess(ctx, binary, []string{"--version"}, nil, "", ControlledLarkCLIVersionTimeout)
	if processErr != nil {
		return fmt.Errorf("feishu: verify lark-cli version: %w", processErr)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return fmt.Errorf("feishu: verify lark-cli version: %w", errControlledCLIOutputLimit)
	}
	if waitErr != nil {
		return fmt.Errorf("feishu: verify lark-cli version command: %w", waitErr)
	}
	if !isPinnedLarkCLIVersion(string(result.Stdout)) {
		return fmt.Errorf("feishu: lark-cli version mismatch: %w", errControlledCLIInvalidInput)
	}
	return nil
}

// Run executes argv directly against the fixed binary in an existing isolated
// HOME. It accepts only a single complete JSON envelope and requires both exit 0
// and ok=true for success.
func (r *ControlledLarkCLIRunner) Run(
	ctx context.Context,
	home string,
	argv []string,
	stdinJSON []byte,
) (*CLIResult, error) {
	result := &CLIResult{ExitCode: -1}
	binary, err := r.binaryPath()
	if err != nil {
		return result, err
	}
	if err := validateControlledCLIHome(home); err != nil {
		return result, err
	}
	if err := validateControlledCLIInput(argv, stdinJSON); err != nil {
		return result, err
	}

	timeout := r.timeout
	if timeout <= 0 || timeout > ControlledLarkCLITimeout {
		timeout = ControlledLarkCLITimeout
	}
	result, waitErr, processErr := r.runProcess(ctx, binary, argv, stdinJSON, home, timeout)
	if processErr != nil {
		return result, processErr
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return result, fmt.Errorf("feishu: lark-cli output rejected: %w", errControlledCLIOutputLimit)
	}

	envelope, decodeErr := decodeControlledCLIEnvelope(result.Stdout)
	if decodeErr == nil {
		result.Envelope = envelope
	}
	if waitErr != nil {
		return result, fmt.Errorf("feishu: lark-cli process failed: %w", waitErr)
	}
	if decodeErr != nil {
		return result, decodeErr
	}
	if !envelope.OK {
		return result, fmt.Errorf("feishu: lark-cli operation failed: %w", errControlledCLIBusiness)
	}
	return result, nil
}

func (r *ControlledLarkCLIRunner) binaryPath() (string, error) {
	binary := controlledLarkCLIBinary
	if r != nil && r.binary != "" {
		binary = r.binary
	}
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary || strings.IndexByte(binary, 0) >= 0 {
		return "", fmt.Errorf("feishu: lark-cli binary path rejected: %w", errControlledCLIInvalidInput)
	}
	return binary, nil
}

func validateControlledCLIHome(home string) error {
	if home == "" || !filepath.IsAbs(home) || filepath.Clean(home) != home || strings.IndexByte(home, 0) >= 0 {
		return fmt.Errorf("feishu: lark-cli HOME path rejected: %w", errControlledCLIInvalidInput)
	}
	info, err := os.Lstat(home)
	if err != nil {
		return fmt.Errorf("feishu: inspect lark-cli HOME: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("feishu: lark-cli HOME is not a real directory: %w", errControlledCLIInvalidInput)
	}
	return nil
}

func validateControlledCLIInput(argv []string, stdinJSON []byte) error {
	if len(argv) == 0 || len(argv) > ControlledLarkCLIMaxArgvCount {
		return fmt.Errorf("feishu: lark-cli argv count rejected: %w", errControlledCLIInvalidInput)
	}
	total := 0
	for _, arg := range argv {
		if strings.IndexByte(arg, 0) >= 0 || len(arg) > ControlledLarkCLIMaxArgvBytes-total {
			return fmt.Errorf("feishu: lark-cli argv bytes rejected: %w", errControlledCLIInvalidInput)
		}
		total += len(arg)
	}
	if len(stdinJSON) > ControlledLarkCLIMaxStdinBytes {
		return fmt.Errorf("feishu: lark-cli stdin rejected: %w", errControlledCLIInvalidInput)
	}
	if len(stdinJSON) > 0 && (bytes.IndexByte(stdinJSON, 0) >= 0 || !json.Valid(stdinJSON)) {
		return fmt.Errorf("feishu: lark-cli stdin JSON rejected: %w", errControlledCLIInvalidInput)
	}
	return nil
}

func isPinnedLarkCLIVersion(stdout string) bool {
	value := strings.TrimSpace(stdout)
	accepted := [...]string{
		LarkCLIVersion,
		"v" + LarkCLIVersion,
		"lark-cli " + LarkCLIVersion,
		"lark-cli v" + LarkCLIVersion,
		"lark-cli version " + LarkCLIVersion,
		"lark-cli version v" + LarkCLIVersion,
		"lark-cli version: " + LarkCLIVersion,
		"lark-cli version: v" + LarkCLIVersion,
	}
	for _, exact := range accepted {
		if value == exact {
			return true
		}
	}
	return false
}

func decodeControlledCLIEnvelope(raw []byte) (*CLIEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil {
		return nil, fmt.Errorf("feishu: decode lark-cli envelope: %w: %w", errControlledCLIInvalidJSON, err)
	}
	okRaw, present := object["ok"]
	if !present {
		return nil, fmt.Errorf("feishu: lark-cli envelope missing ok: %w", errControlledCLIInvalidJSON)
	}
	var okValue bool
	if err := json.Unmarshal(okRaw, &okValue); err != nil {
		return nil, fmt.Errorf("feishu: lark-cli envelope ok is not boolean: %w: %w", errControlledCLIInvalidJSON, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("feishu: lark-cli envelope has trailing JSON: %w", errControlledCLIInvalidJSON)
		}
		return nil, fmt.Errorf("feishu: lark-cli envelope has trailing bytes: %w: %w", errControlledCLIInvalidJSON, err)
	}
	var envelope CLIEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("feishu: decode lark-cli envelope fields: %w: %w", errControlledCLIInvalidJSON, err)
	}
	envelope.OK = okValue
	return &envelope, nil
}

type controlledCapture struct {
	bytes     []byte
	truncated bool
}

func captureControlledStream(reader *os.File, limit int) <-chan controlledCapture {
	result := make(chan controlledCapture, 1)
	go func() {
		defer close(result)
		defer reader.Close()
		capture := controlledCapture{bytes: make([]byte, 0, min(limit, 32<<10))}
		chunk := make([]byte, 32<<10)
		for {
			n, err := reader.Read(chunk)
			if n > 0 {
				remaining := limit - len(capture.bytes)
				if remaining > 0 {
					keep := min(n, remaining)
					capture.bytes = append(capture.bytes, chunk[:keep]...)
					if keep < n {
						capture.truncated = true
					}
				} else {
					capture.truncated = true
				}
			}
			if err != nil {
				result <- capture
				return
			}
		}
	}()
	return result
}

func (r *ControlledLarkCLIRunner) runProcess(
	ctx context.Context,
	binary string,
	argv []string,
	stdin []byte,
	home string,
	timeout time.Duration,
) (*CLIResult, error, error) {
	result := &CLIResult{ExitCode: -1}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return result, nil, fmt.Errorf("feishu: create lark-cli stdout pipe: %w", err)
	}
	defer stdoutReader.Close()
	defer stdoutWriter.Close()
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		return result, nil, fmt.Errorf("feishu: create lark-cli stderr pipe: %w", err)
	}
	defer stderrReader.Close()
	defer stderrWriter.Close()

	cmd := exec.CommandContext(runCtx, binary, argv...) // #nosec G204 -- binary is fixed absolute path; argv is never shell-evaluated
	cmd.Env = controlledCLIEnvironment(home)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = controlledProcessGroupWait
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}

	if err := cmd.Start(); err != nil {
		return result, nil, fmt.Errorf("feishu: start lark-cli: %w", err)
	}
	result.InvocationStarted = true
	pid := cmd.Process.Pid
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	stdoutCapture := captureControlledStream(stdoutReader, ControlledLarkCLIMaxStdoutBytes)
	stderrCapture := captureControlledStream(stderrReader, ControlledLarkCLIMaxStderrBytes)

	waitErr := cmd.Wait()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	groupErr := terminateAndWaitControlledProcessGroup(pid, controlledProcessGroupWait)
	stdout := waitControlledCapture(stdoutCapture, stdoutReader, controlledProcessGroupWait)
	stderr := waitControlledCapture(stderrCapture, stderrReader, controlledProcessGroupWait)
	result.Stdout = stdout.bytes
	result.Stderr = stderr.bytes
	result.StdoutTruncated = stdout.truncated
	result.StderrTruncated = stderr.truncated

	if runErr := runCtx.Err(); runErr != nil {
		return result, waitErr, fmt.Errorf("feishu: lark-cli context ended: %w", runErr)
	}
	if groupErr != nil {
		return result, waitErr, fmt.Errorf("feishu: reap lark-cli process group: %w", groupErr)
	}
	return result, waitErr, nil
}

func waitControlledCapture(ch <-chan controlledCapture, reader *os.File, timeout time.Duration) controlledCapture {
	select {
	case captured := <-ch:
		return captured
	case <-time.After(timeout):
		_ = reader.Close()
		return <-ch
	}
}

func terminateAndWaitControlledProcessGroup(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		// Darwin may report EPERM for a just-killed, already-reparented process
		// group. Because the runner and every descendant start under the same
		// unprivileged uid, EPERM also proves there is no remaining descendant we
		// are permitted to signal. Stream EOF below is the second cleanup gate.
		if errors.Is(err, syscall.EPERM) {
			return nil
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process group %d still exists after %s", pid, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func controlledCLIEnvironment(home string) []string {
	allowed := map[string]struct{}{
		"PATH": {}, "LANG": {}, "LC_ALL": {}, "LC_CTYPE": {}, "TZ": {},
		"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "NO_PROXY": {},
		"http_proxy": {}, "https_proxy": {}, "no_proxy": {},
		"SSL_CERT_FILE": {}, "SSL_CERT_DIR": {},
	}
	values := make(map[string]string, len(allowed)+3)
	for _, item := range os.Environ() {
		key, value, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		if _, ok := allowed[key]; ok {
			values[key] = value
		}
	}
	if home != "" {
		values["HOME"] = home
	}
	values["LARKSUITE_CLI_NO_UPDATE_NOTIFIER"] = "1"
	values["LARKSUITE_CLI_NO_SKILLS_NOTIFIER"] = "1"

	order := []string{
		"PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "HOME",
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER",
	}
	environment := make([]string, 0, len(values))
	for _, key := range order {
		if value, ok := values[key]; ok {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}
