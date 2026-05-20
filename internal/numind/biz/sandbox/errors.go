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
