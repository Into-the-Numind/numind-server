package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	deviceAuthCLIMaxJSONBytes   = 1 << 20
	deviceAuthCLIMaxURLBytes    = 8 << 10
	deviceAuthCLIMaxExpiresIn   = 12 * time.Minute
	deviceAuthLegacyVerifyPath  = "/suite/passport/oauth/device"
	deviceAuthAccountVerifyPath = "/oauth/v1/device/verify"
)

var (
	errDeviceAuthCLIProtocol   = errors.New("feishu device authorization protocol rejected")
	errDeviceAuthCLIDependency = errors.New("feishu device authorization dependency unavailable")
)

// DeviceAuthCLI is the typed, fixed-version boundary for split user
// authorization and its existing recovery evidence checks.
type DeviceAuthCLI interface {
	StartUserAuth(context.Context, string, []string) (DeviceAuthStart, error)
	CompleteUserAuth(context.Context, string, string) (DeviceAuthOutcome, error)
	AuthStatus(context.Context, string) (bool, error)
	AppIDFromHome(context.Context, string) (string, error)
}

// DeviceAuthStart contains the short-lived opaque values returned by the
// pinned no-wait login command. Callers must encrypt DeviceCode before storage.
type DeviceAuthStart struct {
	VerificationURL string
	DeviceCode      string
	ExpiresIn       time.Duration
}

// DeviceAuthOutcome is the safe state-machine vocabulary for one completion
// attempt. It never contains raw CLI output or credential material.
type DeviceAuthOutcome string

const (
	DeviceAuthCompleted           DeviceAuthOutcome = "completed"
	DeviceAuthPending             DeviceAuthOutcome = "pending"
	DeviceAuthRejected            DeviceAuthOutcome = "rejected"
	DeviceAuthExpired             DeviceAuthOutcome = "expired"
	DeviceAuthRetryableDependency DeviceAuthOutcome = "retryable_dependency"
	DeviceAuthProtocolFailure     DeviceAuthOutcome = "protocol_failure"
	DeviceAuthAmbiguous           DeviceAuthOutcome = "ambiguous"
)

// String returns only the fixed, non-secret outcome label.
func (o DeviceAuthOutcome) String() string {
	return string(o)
}

type strictDeviceAuthStartData struct {
	DeviceCode      string `json:"device_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int64  `json:"expires_in"`
	Hint            string `json:"hint"`
}

type strictDeviceAuthCompletionData struct {
	Event          string   `json:"event"`
	UserOpenID     string   `json:"user_open_id"`
	UserName       *string  `json:"user_name"`
	Scope          string   `json:"scope"`
	Requested      []string `json:"requested"`
	NewlyGranted   []string `json:"newly_granted"`
	AlreadyGranted []string `json:"already_granted"`
	Missing        []string `json:"missing"`
	Granted        []string `json:"granted"`
}

// StartUserAuth starts the pinned short no-wait process and accepts only its
// exact structured contract. No shell or caller-supplied argv is involved.
func (r *ControlledLarkCLIRunner) StartUserAuth(
	ctx context.Context,
	home string,
	scopes []string,
) (DeviceAuthStart, error) {
	canonicalScopes, err := canonicalAuthScopes(scopes)
	if err != nil || len(canonicalScopes) == 0 {
		return DeviceAuthStart{}, errDeviceAuthCLIProtocol
	}
	if validateControlledCLIHome(home) != nil {
		return DeviceAuthStart{}, errDeviceAuthCLIProtocol
	}
	if _, err := r.binaryPath(); err != nil {
		return DeviceAuthStart{}, errDeviceAuthCLIProtocol
	}
	argv := []string{
		"auth", "login", "--scope", strings.Join(canonicalScopes, " "), "--no-wait", "--json",
	}
	result, runErr := r.runDeviceAuthProcess(ctx, home, argv)
	if result == nil || !result.InvocationStarted {
		return DeviceAuthStart{}, errDeviceAuthCLIDependency
	}
	if result.StdoutTruncated || result.StderrTruncated || len(result.Stdout) > deviceAuthCLIMaxJSONBytes {
		return DeviceAuthStart{}, errDeviceAuthCLIProtocol
	}
	if runErr != nil || result.ExitCode != 0 {
		return DeviceAuthStart{}, errDeviceAuthCLIDependency
	}

	var data strictDeviceAuthStartData
	if decodeStrictDeviceAuthObject(result.Stdout, &data) != nil ||
		!validDeviceAuthDeviceCode(data.DeviceCode) ||
		!validDeviceAuthVerificationURL(data.VerificationURL) ||
		data.ExpiresIn <= 0 || data.ExpiresIn > int64(deviceAuthCLIMaxExpiresIn/time.Second) ||
		len(data.Hint) > 64<<10 || !utf8.ValidString(data.Hint) || strings.ContainsRune(data.Hint, 0) {
		return DeviceAuthStart{}, errDeviceAuthCLIProtocol
	}
	return DeviceAuthStart{
		VerificationURL: data.VerificationURL,
		DeviceCode:      data.DeviceCode,
		ExpiresIn:       time.Duration(data.ExpiresIn) * time.Second,
	}, nil
}

// CompleteUserAuth passes the opaque device code as the pinned CLI's direct
// argv element and returns only a normalized, non-secret outcome.
func (r *ControlledLarkCLIRunner) CompleteUserAuth(
	ctx context.Context,
	home string,
	deviceCode string,
) (DeviceAuthOutcome, error) {
	if !validDeviceAuthDeviceCode(deviceCode) || validateControlledCLIHome(home) != nil {
		return DeviceAuthProtocolFailure, nil
	}
	if _, err := r.binaryPath(); err != nil {
		return DeviceAuthProtocolFailure, nil
	}

	result, runErr := r.runDeviceAuthProcess(ctx, home, []string{
		"auth", "login", "--device-code", deviceCode, "--json",
	})
	return classifyDeviceAuthCompletionResult(result, runErr), nil
}

// runDeviceAuthProcess executes the two auth-login commands without applying
// ControlledLarkCLIRunner.Run's generic business-command envelope parser.
// lark-cli v1.0.68 intentionally gives auth login its own JSON contracts.
func (r *ControlledLarkCLIRunner) runDeviceAuthProcess(
	ctx context.Context,
	home string,
	argv []string,
) (*CLIResult, error) {
	result := &CLIResult{ExitCode: -1}
	binary, err := r.binaryPath()
	if err != nil {
		return result, err
	}
	if err := validateControlledCLIHome(home); err != nil {
		return result, err
	}
	if err := validateControlledCLIInput(argv, nil); err != nil {
		return result, err
	}
	timeout := r.timeout
	if timeout <= 0 || timeout > ControlledLarkCLITimeout {
		timeout = ControlledLarkCLITimeout
	}
	result, waitErr, processErr := r.runProcess(ctx, binary, argv, nil, home, timeout)
	if processErr != nil {
		return result, processErr
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return result, errControlledCLIOutputLimit
	}
	if waitErr != nil {
		return result, waitErr
	}
	return result, nil
}

func classifyDeviceAuthCompletionResult(result *CLIResult, runErr error) DeviceAuthOutcome {
	if result == nil {
		return DeviceAuthRetryableDependency
	}
	if !result.InvocationStarted {
		return DeviceAuthRetryableDependency
	}
	// A timeout or caller cancellation after cmd.Start always leaves the HOME
	// outcome ambiguous, even when a stream limit was reached concurrently.
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return DeviceAuthAmbiguous
	}
	if !deviceAuthRunnerReturnedSafely(result, runErr) {
		return DeviceAuthAmbiguous
	}
	if result.StdoutTruncated || result.StderrTruncated || len(result.Stdout) > deviceAuthCLIMaxJSONBytes {
		// A non-zero process may have written the token before emitting a large
		// diagnostic. Its candidate HOME is the only safe completion evidence.
		if result.ExitCode != 0 {
			return DeviceAuthAmbiguous
		}
		return DeviceAuthProtocolFailure
	}
	// In v1.0.68 typed command errors are written to stderr and collapse the
	// provider's device-flow reason into localized prose. Do not classify that
	// prose or mistake it for protocol evidence; reconcile the candidate HOME.
	if result.ExitCode != 0 {
		return DeviceAuthAmbiguous
	}

	if !deviceAuthStructuredCompletionSafe(result, runErr) {
		return DeviceAuthAmbiguous
	}
	var completion strictDeviceAuthCompletionData
	if decodeStrictDeviceAuthObject(result.Stdout, &completion) != nil ||
		completion.Event != "authorization_complete" ||
		strings.TrimSpace(completion.UserOpenID) == "" || completion.UserName == nil ||
		!validDeviceAuthCompletionScopes(completion) {
		return DeviceAuthProtocolFailure
	}
	return DeviceAuthCompleted
}

func deviceAuthRunnerReturnedSafely(result *CLIResult, runErr error) bool {
	if deviceAuthStructuredCompletionSafe(result, runErr) {
		return true
	}
	// These sentinels are created only after runProcess has completed Wait and
	// process-group cleanup; they are safe evidence for protocol rejection, but
	// never authorize a structured business subtype.
	return result != nil && result.ExitCode >= 0 &&
		(errors.Is(runErr, errControlledCLIInvalidJSON) || errors.Is(runErr, errControlledCLIOutputLimit))
}

func deviceAuthStructuredCompletionSafe(result *CLIResult, runErr error) bool {
	if result == nil || result.ExitCode < 0 {
		return false
	}
	if runErr == nil {
		return result.ExitCode == 0
	}
	if errors.Is(runErr, errControlledCLIBusiness) {
		return result.ExitCode == 0
	}
	var exitErr *exec.ExitError
	return errors.As(runErr, &exitErr) && exitErr.ProcessState != nil &&
		exitErr.ProcessState.Exited() && exitErr.ExitCode() >= 0 &&
		result.ExitCode == exitErr.ExitCode()
}

func decodeStrictDeviceAuthObject(raw []byte, destination any) error {
	if len(raw) == 0 || len(raw) > deviceAuthCLIMaxJSONBytes || rejectDuplicateDeviceAuthJSON(raw) != nil {
		return errDeviceAuthCLIProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errDeviceAuthCLIProtocol
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errDeviceAuthCLIProtocol
	}
	return nil
}

func validDeviceAuthCompletionScopes(data strictDeviceAuthCompletionData) bool {
	if data.Scope == "" || data.Requested == nil || data.NewlyGranted == nil ||
		data.AlreadyGranted == nil || data.Missing == nil || data.Granted == nil ||
		len(data.Requested) == 0 || len(data.Granted) == 0 || len(data.Missing) != 0 {
		return false
	}
	for _, values := range [][]string{data.Requested, data.NewlyGranted, data.AlreadyGranted, data.Missing, data.Granted} {
		canonical, err := canonicalAuthScopes(values)
		if err != nil || len(canonical) != len(values) {
			return false
		}
	}
	granted, err := canonicalAuthScopes(data.Granted)
	if err != nil {
		return false
	}
	scope, err := canonicalAuthScopes(strings.Fields(data.Scope))
	if err != nil || len(granted) != len(scope) {
		return false
	}
	for index := range granted {
		if granted[index] != scope[index] {
			return false
		}
	}
	grantedSet := make(map[string]struct{}, len(granted))
	for _, value := range granted {
		grantedSet[value] = struct{}{}
	}
	requestedSet := make(map[string]struct{}, len(data.Requested))
	for _, value := range data.Requested {
		if _, ok := grantedSet[value]; !ok {
			return false
		}
		requestedSet[value] = struct{}{}
	}
	classified := make(map[string]struct{}, len(data.NewlyGranted)+len(data.AlreadyGranted))
	for _, values := range [][]string{data.NewlyGranted, data.AlreadyGranted} {
		for _, value := range values {
			if _, ok := requestedSet[value]; !ok {
				return false
			}
			if _, duplicate := classified[value]; duplicate {
				return false
			}
			classified[value] = struct{}{}
		}
	}
	return len(classified) == len(requestedSet)
}

func validDeviceAuthVerificationURL(value string) bool {
	if value == "" || len(value) > deviceAuthCLIMaxURLBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" ||
		parsed.Port() != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "open.feishu.cn", "open.larksuite.com":
		if parsed.Path != deviceAuthLegacyVerifyPath {
			return false
		}
	case "accounts.feishu.cn", "accounts.larksuite.com":
		if parsed.Path != deviceAuthAccountVerifyPath {
			return false
		}
	default:
		return false
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(values["user_code"]) != 1 || values.Get("user_code") == "" ||
		strings.ContainsRune(values.Get("user_code"), 0) {
		return false
	}
	if strings.HasPrefix(host, "accounts.") {
		return len(values) == 2 && len(values["flow_id"]) == 1 && values.Get("flow_id") != "" &&
			!strings.ContainsRune(values.Get("flow_id"), 0)
	}
	if len(values) != 1 {
		return false
	}
	return true
}

// rejectDuplicateDeviceAuthJSON validates one complete JSON value while
// rejecting duplicate object keys at every nesting level.
func rejectDuplicateDeviceAuthJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkUniqueDeviceAuthJSON(decoder, true); err != nil {
		return errDeviceAuthCLIProtocol
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errDeviceAuthCLIProtocol
	}
	return nil
}

func walkUniqueDeviceAuthJSON(decoder *json.Decoder, requireObject bool) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if requireObject && (!isDelimiter || delimiter != '{') {
		return errDeviceAuthCLIProtocol
	}
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return errDeviceAuthCLIProtocol
			}
			if _, exists := seen[key]; exists {
				return errDeviceAuthCLIProtocol
			}
			seen[key] = struct{}{}
			if err := walkUniqueDeviceAuthJSON(decoder, false); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return errDeviceAuthCLIProtocol
		}
	case '[':
		for decoder.More() {
			if err := walkUniqueDeviceAuthJSON(decoder, false); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return errDeviceAuthCLIProtocol
		}
	default:
		return errDeviceAuthCLIProtocol
	}
	return nil
}

var _ DeviceAuthCLI = (*ControlledLarkCLIRunner)(nil)
