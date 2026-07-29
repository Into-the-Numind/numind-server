// Package sandbox provides Docker pool sandbox runtime for the Agent mode
// (V5 ADR: 0002-sandbox-final.md covers blueprint §4.6).
//
// The package is layered:
//   - config.go        — SandboxConfig + DefaultSandboxConfig + LoadFromViper
//   - errors.go        — sentinel errors
//   - docker_client.go — DockerClient interface + os/exec impl + mock for tests
//   - pool.go          — Pool interface + real / disabled impls + Session
//   - runner.go        — ExecCommand / WriteFile / ReadFile primitives
//   - security.go      — Spawn args composition + V5 ADR Q2 hardening checklist
//   - network.go       — NetworkPolicy (None real, Allowlist stub)
//   - seccomp.json     — embedded seccomp profile
package sandbox

import "errors"

// ErrSandboxDisabled is returned by Pool.Borrow when SandboxConfig.Backend
// is "disabled" — used by prod where SANDBOX_BACKEND env is absent.
var ErrSandboxDisabled = errors.New("sandbox backend disabled")

// ErrPoolExhausted is returned by Pool.Borrow when the pool is empty AND
// the configured wait deadline has elapsed.
var ErrPoolExhausted = errors.New("sandbox pool exhausted; try again later")

// ErrSessionReturned is returned by Pool.Return if the session has already
// been returned (once-semantic guard against double-Return).
var ErrSessionReturned = errors.New("session already returned")

// ErrSandboxOOM is returned by sandbox.ExecCommand when Docker reports
// the container was OOM killed by the kernel.
var ErrSandboxOOM = errors.New("sandbox container OOM killed")

// Broker-facing errors intentionally contain no host path, container ID, or
// daemon detail. Callers may map them to stable product copy.
var (
	ErrBrokerUnavailable       = errors.New("sandbox broker unavailable")
	ErrSandboxPolicyDenied     = errors.New("sandbox request denied by broker policy")
	ErrSandboxTimeout          = errors.New("sandbox operation timed out")
	ErrBrokerResponseTooLarge  = errors.New("sandbox broker response exceeds limit")
	ErrBrokerProtocolViolation = errors.New("sandbox broker protocol violation")
)

// ErrNotImplemented is returned by sandbox.WriteFile / ReadFile in v1
// (file management deferred to follow-up).
var ErrNotImplemented = errors.New("not implemented in v1")

// ErrAllowlistNotImplemented is returned by NetworkPolicyForBackend when
// the requested policy is Allowlist (v1 stub; #14 follow-up).
var ErrAllowlistNotImplemented = errors.New("network allowlist policy not implemented in v1")

// ErrImageGenProviderNotConfigured is returned by the image_gen tool's
// Execute when no image-generation provider is wired (current v1 state —
// wanx2.1 / wan2.2 endpoint registration is a follow-up feature).
//
// Lives in this package so both the sandbox subpackage tests and the
// image_gen tool can reference the same sentinel.
var ErrImageGenProviderNotConfigured = errors.New("image generation provider not configured; please contact admin")

// --- Track 4 (Skill / invoke_skill) sentinel errors ---

// ErrInputTooLarge is returned by CopyFileIn / sanitizeInputFile when the
// supplied file exceeds the 50 MB input size limit.
var ErrInputTooLarge = errors.New("sandbox input file exceeds 50 MB limit")

// ErrUnsafeFilename is returned by CopyFileIn / sanitizeInputFile when the
// filename contains path traversal sequences or disallowed characters.
var ErrUnsafeFilename = errors.New("sandbox input filename is unsafe (path traversal or disallowed chars)")

// ErrMacroDetected is returned by sanitizeInputFile when an Office file
// (.docx / .xlsx) contains an embedded VBA macro (vbaProject.bin).
var ErrMacroDetected = errors.New("sandbox input file contains embedded macro (vbaProject.bin)")

// ErrOutputTooLarge is returned by ScanOutput when an output file exceeds
// the configured output_max_size_mb (hard ceiling 50 MB).
var ErrOutputTooLarge = errors.New("sandbox output file exceeds maximum size limit")

// ErrZipBomb is returned by ScanOutput when a zip/docx/xlsx/pptx file
// expands to more than 500 MB when decompressed (hard-coded safety ceiling).
var ErrZipBomb = errors.New("sandbox output file is a zip bomb (expanded size > 500 MB)")

// ErrMimeMismatch is returned by ScanOutput when the file's content (magic
// bytes) does not match the declared MIME type.
var ErrMimeMismatch = errors.New("sandbox output file MIME type does not match declared type")

// ErrSkillNotFound is returned by AcquireForSkill when the requested skill
// directory does not exist under SandboxConfig.SkillsRoot.
var ErrSkillNotFound = errors.New("skill not found in skills_root")

// ErrCOSUploadFailed is returned by CollectOutputs when one or more output
// files cannot be uploaded to COS. The temporary files are preserved for
// retry; the session is still returned to the pool.
var ErrCOSUploadFailed = errors.New("sandbox output COS upload failed")
