# T10 sandboxd process and drain verification

## Scope

Implemented the deployable `numind-sandboxd` composition root for the existing Prod host plan. The main product API still does not receive Docker control; it will talk to this daemon over the bounded Unix RPC path in later wiring work.

## What changed

- Added `cmd/numind-sandboxd` with an absolute, sandboxd-only config path requirement.
- Explicitly rejects `config_prod.yaml` and any config file containing business sections.
- Wires journal, fixed Docker runtime adapter, scheduler, recovery, Unix RPC server, Linux readiness probes, Linux pressure sampling, and bounded shutdown drain.
- Allows startup to continue after durable recovery has already marked incomplete leases for compensation, while still failing closed on unrecoverable startup errors.
- Adds a Docker runtime adapter that uses fixed command shapes, a configured Rootless Docker endpoint, broker-instance labels, bounded exec output, bounded copy streams, and validated tar output.
- Adds Linux host pressure/readiness probes for `/proc`, cgroup v2, data-root mount identity, disk/inode usage, and pinned image digest checks.
- Adds a pressure runner that starts with admission closed, samples every two seconds, closes on sampling gaps, and reclaims shed leases before readiness can reopen admission.

## Product meaning

This is the backend daemon needed for AI Agent / plugin / document-tool sandbox execution on Prod. In plain terms: the feature gets a separate “tool execution guard room” on the same Prod server, instead of giving the customer-facing API direct Docker control.

## Safety notes

- `config_prod.yaml` was not modified and is rejected by the new daemon.
- No Prod database, Prod server, or Prod customer data was touched.
- Docker host validation rejects `/var/run/docker.sock` and `/run/docker.sock`; the accepted path must be an explicitly configured Rootless or sandbox-owned Unix socket.
- Shutdown closes new admissions first and waits up to 300 seconds before marking live leases for durable recovery.
- The previously recorded T3 residual isolation risks remain deferred per product-owner direction; this task did not expand that scope.

## Verification

- `go test ./cmd/numind-sandboxd ./internal/numind/sandboxbroker -run 'Sandboxd|Pressure|RuntimeAdapter|StaticCapacity|Metrics|Health|Readiness' -count=1`
- `go test ./cmd/numind-sandboxd ./internal/numind/sandboxbroker -race -count=1`
- `GOOS=linux GOARCH=amd64 go test -c ./cmd/numind-sandboxd`
- `GOOS=linux GOARCH=amd64 go test -c ./internal/numind/sandboxbroker`
- `go test ./...`
- `PATH="$(go env GOPATH)/bin:$PATH" GOPROXY=https://goproxy.cn,direct task lint`

All passed. macOS emitted the existing sqlite-vec deprecation warnings during `go test`/`go vet`; they were warnings only, not failures.
