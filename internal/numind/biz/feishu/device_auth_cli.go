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
	deviceAuthCLIMaxJSONBytes     = 1 << 20
	deviceAuthCLIMaxURLBytes      = 8 << 10
	deviceAuthCLIMaxExpiresIn     = 12 * time.Minute
	deviceAuthVerificationPath    = "/suite/passport/oauth/device"
	deviceAuthIdentityUser        = "user"
	deviceAuthSubtypePending      = "authorization_pending"
	deviceAuthSubtypeAccessDenied = "access_denied"
	deviceAuthSubtypeExpiredToken = "expired_token"
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

type strictDeviceAuthEnvelope struct {
	OK       *bool                  `json:"ok"`
	Identity string                 `json:"identity,omitempty"`
	Data     json.RawMessage        `json:"data,omitempty"`
	Error    *strictDeviceAuthError `json:"error,omitempty"`
}

// strictDeviceAuthError mirrors the bounded fields recognized by the pinned
// CLI envelope. Classification below deliberately reads Subtype only.
type strictDeviceAuthError struct {
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

type strictDeviceAuthStartData struct {
	DeviceCode      string `json:"device_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int64  `json:"expires_in"`
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
	result, runErr := r.Run(ctx, home, argv, nil)
	if result == nil || !result.InvocationStarted {
		return DeviceAuthStart{}, errDeviceAuthCLIDependency
	}
	if result.StdoutTruncated || result.StderrTruncated || len(result.Stdout) > deviceAuthCLIMaxJSONBytes {
		return DeviceAuthStart{}, errDeviceAuthCLIProtocol
	}
	if runErr != nil && len(bytes.TrimSpace(result.Stdout)) == 0 {
		return DeviceAuthStart{}, errDeviceAuthCLIDependency
	}

	envelope, decodeErr := decodeStrictDeviceAuthEnvelope(result.Stdout)
	if runErr != nil || result.ExitCode != 0 || decodeErr != nil || envelope.OK == nil || !*envelope.OK ||
		envelope.Error != nil || !validDeviceAuthEnvelopeIdentity(envelope.Identity) {
		return DeviceAuthStart{}, errDeviceAuthCLIProtocol
	}
	var data strictDeviceAuthStartData
	if decodeStrictDeviceAuthData(envelope.Data, &data) != nil ||
		!validDeviceAuthDeviceCode(data.DeviceCode) ||
		!validDeviceAuthVerificationURL(data.VerificationURL) ||
		data.ExpiresIn <= 0 || data.ExpiresIn > int64(deviceAuthCLIMaxExpiresIn/time.Second) {
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

	result, runErr := r.Run(ctx, home, []string{
		"auth", "login", "--device-code", deviceCode, "--json",
	}, nil)
	return classifyDeviceAuthCompletionResult(result, runErr), nil
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
		return DeviceAuthProtocolFailure
	}

	envelope, decodeErr := decodeStrictDeviceAuthEnvelope(result.Stdout)
	if decodeErr != nil || envelope.OK == nil || !validDeviceAuthEnvelopeIdentity(envelope.Identity) {
		return DeviceAuthProtocolFailure
	}
	if *envelope.OK {
		if !deviceAuthStructuredCompletionSafe(result, runErr) || envelope.Error != nil ||
			decodeStrictDeviceAuthEmptyData(envelope.Data) != nil {
			return DeviceAuthProtocolFailure
		}
		return DeviceAuthCompleted
	}
	if !deviceAuthStructuredCompletionSafe(result, runErr) {
		return DeviceAuthAmbiguous
	}
	if decodeStrictDeviceAuthEmptyData(envelope.Data) != nil || envelope.Error == nil ||
		envelope.Error.Type != "authorization" || envelope.Error.Subtype == "" ||
		(envelope.Error.Identity != "" && envelope.Error.Identity != deviceAuthIdentityUser) {
		return DeviceAuthProtocolFailure
	}
	switch envelope.Error.Subtype {
	case deviceAuthSubtypePending:
		return DeviceAuthPending
	case deviceAuthSubtypeAccessDenied:
		return DeviceAuthRejected
	case deviceAuthSubtypeExpiredToken:
		return DeviceAuthExpired
	default:
		return DeviceAuthProtocolFailure
	}
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

func decodeStrictDeviceAuthEnvelope(raw []byte) (*strictDeviceAuthEnvelope, error) {
	if len(raw) == 0 || len(raw) > deviceAuthCLIMaxJSONBytes || rejectDuplicateDeviceAuthJSON(raw) != nil {
		return nil, errDeviceAuthCLIProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope strictDeviceAuthEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return nil, errDeviceAuthCLIProtocol
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errDeviceAuthCLIProtocol
	}
	return &envelope, nil
}

func decodeStrictDeviceAuthData(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
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

func decodeStrictDeviceAuthEmptyData(raw json.RawMessage) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return decodeStrictDeviceAuthData(raw, &struct{}{})
}

func validDeviceAuthEnvelopeIdentity(identity string) bool {
	return identity == deviceAuthIdentityUser
}

func validDeviceAuthVerificationURL(value string) bool {
	if value == "" || len(value) > deviceAuthCLIMaxURLBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" ||
		parsed.Port() != "" || parsed.Path != deviceAuthVerificationPath {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "open.feishu.cn", "open.larksuite.com":
	default:
		return false
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(values) != 1 || len(values["user_code"]) != 1 || values.Get("user_code") == "" ||
		strings.ContainsRune(values.Get("user_code"), 0) {
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
