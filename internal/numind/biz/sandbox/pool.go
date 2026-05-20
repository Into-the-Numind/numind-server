package sandbox

import (
	"context"
	"sync"
	"time"
)

// Session is one borrowed sandbox container. Each Session is owned by a
// single bash_exec tool call between Pool.Borrow and Pool.Return.
//
// Session.returned guards Pool.Return against double-call (PostToolCall +
// defer in bash_exec both invoking Return when one of them was meant to
// be a fallback only).
type Session struct {
	ContainerID string
	ImageTag    string
	Config      SandboxConfig
	BorrowedAt  time.Time

	mu       sync.Mutex
	returned bool
}

// Pool manages a warm pool of sandbox containers. Implementations must be
// safe for concurrent Borrow / Return calls.
type Pool interface {
	// Borrow returns a ready-to-exec sandbox Session, or an error if the
	// pool is disabled (ErrSandboxDisabled) / exhausted (ErrPoolExhausted)
	// / ctx is canceled.
	Borrow(ctx context.Context) (*Session, error)

	// Return destroys the borrowed container and asynchronously spawns a
	// replacement to refill the pool. exitCode and errMsg are stamped onto
	// the audit row by the SandboxHookManager — they are not used by the
	// Pool itself. Safe to call more than once on the same Session
	// (once-semantic; second call returns ErrSessionReturned).
	Return(sess *Session, exitCode int, errMsg string) error

	// Close shuts down the pool: drains the warm pool, signals the spawn
	// goroutine to exit, and (best-effort) destroys any remaining
	// containers. Returns after all goroutines have exited.
	Close() error

	// Size returns the current count of warm (ready-to-borrow) containers.
	// Borrowed-but-not-yet-returned containers are NOT counted.
	Size() int

	// DockerClient returns the DockerClient backing the pool. Used by the
	// agent.SandboxHookManager to give bash_exec.Execute the dc it needs
	// for sandbox.ExecCommand calls — without forcing callers to thread
	// dc through factory wires.
	DockerClient() DockerClient
}

// NewPool constructs a Pool. If cfg.Backend == BackendDisabled (the
// prod-safe default), it returns a no-op disabledPool whose Borrow always
// returns ErrSandboxDisabled. Otherwise, it returns an agentSandboxPool
// backed by the supplied DockerClient.
//
// In the docker-backed path, NewPool also kicks off the warm-up goroutine
// asynchronously; callers do not block on the first PoolMin container
// spawns.
func NewPool(cfg SandboxConfig, dc DockerClient, logger Logger) Pool {
	if logger == nil {
		logger = nopLogger{}
	}
	if cfg.Backend == BackendDisabled || dc == nil {
		return &disabledPool{dc: dc}
	}
	p := &agentSandboxPool{
		cfg:      cfg,
		dc:       dc,
		logger:   logger,
		warm:     make(chan *Session, cfg.PoolMin*2),
		closeCh:  make(chan struct{}),
		spawnReq: make(chan struct{}, cfg.PoolMin*4),
	}
	go p.spawnWorker()
	// Prime the warm pool asynchronously (don't block NewPool caller).
	for i := 0; i < cfg.PoolMin; i++ {
		select {
		case p.spawnReq <- struct{}{}:
		default:
		}
	}
	return p
}

// ===========================================================================
// disabledPool
// ===========================================================================

type disabledPool struct {
	dc DockerClient // may be nil; included so DockerClient() can return whatever was wired
}

var _ Pool = (*disabledPool)(nil)

func (p *disabledPool) Borrow(_ context.Context) (*Session, error) {
	return nil, ErrSandboxDisabled
}
func (p *disabledPool) Return(_ *Session, _ int, _ string) error { return nil }
func (p *disabledPool) Close() error                             { return nil }
func (p *disabledPool) Size() int                                { return 0 }
func (p *disabledPool) DockerClient() DockerClient               { return p.dc }

// ===========================================================================
// agentSandboxPool (real Docker-backed pool)
// ===========================================================================

type agentSandboxPool struct {
	cfg    SandboxConfig
	dc     DockerClient
	logger Logger

	// warm holds ready-to-borrow sessions. Buffered to absorb spike
	// spawns; size = PoolMin * 2.
	warm chan *Session
	// spawnReq is signalled (non-blocking) every time the pool wants
	// another container. The spawnWorker consumes from this channel.
	spawnReq chan struct{}
	// closeCh is closed by Close to signal the worker to exit.
	closeCh chan struct{}
	closed  bool
	mu      sync.Mutex
}

var _ Pool = (*agentSandboxPool)(nil)

func (p *agentSandboxPool) DockerClient() DockerClient { return p.dc }

// Borrow waits up to cfg.PoolMaxWaitMs for a warm container.
// If none arrive in time, returns ErrPoolExhausted.
func (p *agentSandboxPool) Borrow(ctx context.Context) (*Session, error) {
	timeout := time.Duration(p.cfg.PoolMaxWaitMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case sess, ok := <-p.warm:
		if !ok {
			return nil, ErrSandboxDisabled // pool closed
		}
		sess.BorrowedAt = time.Now()
		return sess, nil
	case <-timer.C:
		return nil, ErrPoolExhausted
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Return destroys the container and requests a spawn-replacement. Safe to
// call more than once; second call returns ErrSessionReturned without
// re-destroying.
func (p *agentSandboxPool) Return(sess *Session, exitCode int, errMsg string) error {
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	if sess.returned {
		sess.mu.Unlock()
		return ErrSessionReturned
	}
	sess.returned = true
	sess.mu.Unlock()

	// Destroy synchronously to free the container slot before requesting
	// a replacement spawn.
	destroyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.dc.Destroy(destroyCtx, sess.ContainerID); err != nil {
		p.logger.Warnw("sandbox.Pool.Return: destroy failed",
			"container_id", sess.ContainerID,
			"exit_code", exitCode,
			"error", err)
		// continue — we still want to spawn a replacement
	}

	// Non-blocking spawn request. The worker will absorb the signal.
	select {
	case p.spawnReq <- struct{}{}:
	default:
		// channel is at capacity — pool is already plenty saturated;
		// skip the spawn request.
	}
	return nil
}

// Close drains the warm pool and signals the worker to exit.
func (p *agentSandboxPool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.closeCh)
	p.mu.Unlock()

	// Drain warm pool synchronously (best-effort destroy).
	destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		select {
		case sess := <-p.warm:
			if sess == nil {
				continue
			}
			_ = p.dc.Destroy(destroyCtx, sess.ContainerID)
		default:
			return nil
		}
	}
}

// Size returns the current count of warm containers ready for Borrow.
func (p *agentSandboxPool) Size() int {
	return len(p.warm)
}

// spawnWorker is the single goroutine that owns the warm pool slot.
// It blocks on spawnReq, spawns one container per request, and pushes the
// resulting Session into the warm channel. Failures are logged and the
// loop continues.
func (p *agentSandboxPool) spawnWorker() {
	for {
		select {
		case <-p.closeCh:
			return
		case <-p.spawnReq:
			if p.spawnOne() {
				// keep going
			}
		}
	}
}

func (p *agentSandboxPool) spawnOne() bool {
	spawnCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	seccompPath, err := ResolveSeccompPath()
	if err != nil {
		p.logger.Warnw("sandbox.Pool: resolve seccomp path failed", "error", err)
		// Continue anyway; BuildSpawnConfig will simply omit the seccomp opt
		seccompPath = ""
	}
	spawnCfg := BuildSpawnConfig(p.cfg, seccompPath)
	if missing := ValidateSecurityChecklist(spawnCfg); len(missing) > 0 {
		p.logger.Warnw("sandbox.Pool: security checklist missing items",
			"missing", missing)
		// Hard rule: if seccomp/cap-drop/no-new-priv missing, refuse to spawn
		// — log + return; the warm pool stays empty, Borrow will time out.
		if containsAny(missing, []string{"seccomp profile", "cap-drop ALL", "no-new-privileges"}) {
			return false
		}
	}

	id, err := p.dc.Spawn(spawnCtx, spawnCfg)
	if err != nil {
		p.logger.Warnw("sandbox.Pool: docker spawn failed", "error", err)
		return false
	}
	sess := &Session{
		ContainerID: id,
		ImageTag:    spawnCfg.ImageTag,
		Config:      p.cfg,
		BorrowedAt:  time.Now(),
	}
	select {
	case p.warm <- sess:
		return true
	case <-p.closeCh:
		// Pool closing — destroy the orphan container.
		destroyCtx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		_ = p.dc.Destroy(destroyCtx, id)
		return false
	}
}

func containsAny(haystack []string, needles []string) bool {
	for _, n := range needles {
		for _, h := range haystack {
			if h == n {
				return true
			}
		}
	}
	return false
}
