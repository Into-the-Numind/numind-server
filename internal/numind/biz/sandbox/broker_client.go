package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type brokerDockerClient struct {
	cfg         SandboxConfig
	socket      string
	ownerID     string
	ownerBootID string
	http        *http.Client
	transport   *http.Transport
	logger      Logger
}

var _ DockerClient = (*brokerDockerClient)(nil)

// BrokerLeaseLifecycle is implemented only by the constrained broker client.
// Warm leases are created unbound; the Agent hook binds run/session IDs after
// its durable audit row exists.
type BrokerLeaseLifecycle interface {
	Activate(ctx context.Context, leaseID string, agentRunID uint64, sandboxSessionID uint64) error
	Heartbeat(ctx context.Context, leaseID string) error
	MarkPersisting(ctx context.Context, leaseID string) error
}

var _ BrokerLeaseLifecycle = (*brokerDockerClient)(nil)

// NewBrokerDockerClient creates a DockerClient-compatible client for sandboxd.
// The returned client has no Docker CLI or socket access.
func NewBrokerDockerClient(cfg SandboxConfig, logger Logger) (DockerClient, error) {
	if logger == nil {
		logger = nopLogger{}
	}
	if strings.TrimSpace(cfg.BrokerSocket) == "" || !filepath.IsAbs(cfg.BrokerSocket) {
		return nil, fmt.Errorf("%w: broker socket must be an absolute path", ErrBrokerProtocolViolation)
	}
	ownerID := strings.TrimSpace(cfg.BrokerOwnerID)
	if ownerID == "" || len(ownerID) > 128 || sanitizeDockerLabelValue(ownerID) != ownerID {
		return nil, fmt.Errorf("%w: broker owner id must be an explicit stable identifier", ErrBrokerProtocolViolation)
	}
	if err := validateBrokerClientLimits(cfg); err != nil {
		return nil, err
	}

	socket := cfg.BrokerSocket
	transport := &http.Transport{
		DisableCompression:  true,
		MaxConnsPerHost:     cfg.BrokerMaxConnections,
		MaxIdleConnsPerHost: cfg.BrokerMaxConnections,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	return &brokerDockerClient{
		cfg:         cfg,
		socket:      socket,
		ownerID:     ownerID,
		ownerBootID: currentSandboxOwnerBootID(),
		http:        &http.Client{Transport: transport},
		transport:   transport,
		logger:      logger,
	}, nil
}

func (c *brokerDockerClient) sandboxOwnerIdentity() (string, string) {
	return c.ownerID, c.ownerBootID
}

func validateBrokerClientLimits(cfg SandboxConfig) error {
	values := map[string]struct {
		value int
		max   int
	}{
		"metadata max bytes":    {cfg.BrokerMetadataMaxBytes, DefaultBrokerMetadataMaxBytes},
		"exec output max bytes": {cfg.BrokerExecOutputMaxBytes, DefaultBrokerExecOutputMaxBytes},
		"copy-in max bytes":     {cfg.BrokerCopyInMaxBytes, DefaultBrokerCopyInMaxBytes},
		"copy-out max bytes":    {cfg.BrokerCopyOutMaxBytes, DefaultBrokerCopyOutMaxBytes},
		"single-file max bytes": {cfg.BrokerSingleFileMaxBytes, DefaultBrokerSingleFileMaxBytes},
		"max files":             {cfg.BrokerMaxFiles, DefaultBrokerMaxFiles},
		"max connections":       {cfg.BrokerMaxConnections, DefaultBrokerMaxConnections},
	}
	for name, limit := range values {
		if limit.value <= 0 || limit.value > limit.max {
			return fmt.Errorf("%w: broker %s is outside the safe range", ErrBrokerProtocolViolation, name)
		}
	}
	if cfg.BrokerSingleFileMaxBytes > cfg.BrokerCopyInMaxBytes ||
		cfg.BrokerSingleFileMaxBytes > cfg.BrokerCopyOutMaxBytes {
		return fmt.Errorf("%w: single-file limit exceeds copy limit", ErrBrokerProtocolViolation)
	}
	return nil
}

func (c *brokerDockerClient) Spawn(ctx context.Context, _ SpawnConfig) (string, error) {
	var out struct {
		LeaseID   string     `json:"lease_id"`
		State     string     `json:"state"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	err := c.doJSON(ctx, http.MethodPost, BrokerAPIPrefix+"/leases", BrokerCreateLeaseRequest{
		RequestID:        uuid.NewString(),
		OwnerID:          c.ownerID,
		OwnerBootID:      c.ownerBootID,
		AgentRunID:       0,
		SandboxSessionID: 0,
	}, &out, int64(c.cfg.BrokerMetadataMaxBytes))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out.LeaseID) == "" || out.State != "ready" ||
		out.ExpiresAt == nil || out.ExpiresAt.IsZero() || !out.ExpiresAt.After(time.Now()) {
		return "", fmt.Errorf("%w: invalid create lease response", ErrBrokerProtocolViolation)
	}
	return out.LeaseID, nil
}

func (c *brokerDockerClient) Activate(
	ctx context.Context,
	leaseID string,
	agentRunID uint64,
	sandboxSessionID uint64,
) error {
	if agentRunID == 0 || sandboxSessionID == 0 {
		return ErrSandboxPolicyDenied
	}
	return c.doNoContent(ctx, http.MethodPost, brokerLeasePath(leaseID)+"/activate", BrokerActivateRequest{
		RequestID:        uuid.NewString(),
		AgentRunID:       agentRunID,
		SandboxSessionID: sandboxSessionID,
	})
}

func (c *brokerDockerClient) Heartbeat(ctx context.Context, leaseID string) error {
	return c.doNoContent(ctx, http.MethodPost, brokerLeasePath(leaseID)+"/heartbeat", BrokerMutationRequest{
		RequestID: uuid.NewString(),
	})
}

func (c *brokerDockerClient) MarkPersisting(ctx context.Context, leaseID string) error {
	return c.doNoContent(ctx, http.MethodPost, brokerLeasePath(leaseID)+"/persisting", BrokerMutationRequest{
		RequestID: uuid.NewString(),
	})
}

func (c *brokerDockerClient) Exec(
	ctx context.Context,
	leaseID string,
	cmd []string,
	opts ExecOpts,
) (ExecResult, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout+5*time.Second)
		defer cancel()
	}
	var out struct {
		Stdout   string         `json:"stdout,omitempty"`
		Stderr   string         `json:"stderr,omitempty"`
		ExitCode *int           `json:"exit_code"`
		Duration *time.Duration `json:"duration"`
	}
	maxResponse := int64(c.cfg.BrokerExecOutputMaxBytes)*6 + int64(c.cfg.BrokerMetadataMaxBytes)
	err := c.doJSON(ctx, http.MethodPost, brokerLeasePath(leaseID)+"/exec", BrokerExecRequest{
		RequestID: uuid.NewString(),
		Argv:      append([]string(nil), cmd...),
		Env:       append([]string(nil), opts.Env...),
	}, &out, maxResponse)
	if err != nil {
		return ExecResult{}, err
	}
	if out.ExitCode == nil || out.Duration == nil || *out.Duration < 0 {
		return ExecResult{}, ErrBrokerProtocolViolation
	}
	if len(out.Stdout)+len(out.Stderr) > c.cfg.BrokerExecOutputMaxBytes {
		return ExecResult{}, ErrBrokerResponseTooLarge
	}
	return ExecResult{
		Stdout:   out.Stdout,
		Stderr:   out.Stderr,
		ExitCode: *out.ExitCode,
		Duration: *out.Duration,
	}, nil
}

func (c *brokerDockerClient) Destroy(ctx context.Context, leaseID string) error {
	return c.doNoContent(ctx, http.MethodDelete, brokerLeasePath(leaseID), BrokerMutationRequest{
		RequestID: uuid.NewString(),
	})
}

func (c *brokerDockerClient) Inspect(ctx context.Context, leaseID string) (InspectResult, error) {
	var out struct {
		Status      *string `json:"status"`
		ExitCode    *int    `json:"exit_code"`
		OOMKilled   *bool   `json:"oom_killed"`
		OwnerID     string  `json:"owner_id,omitempty"`
		OwnerBootID string  `json:"owner_boot_id,omitempty"`
	}
	if err := c.doJSON(ctx, http.MethodGet, brokerLeasePath(leaseID), nil, &out, int64(c.cfg.BrokerMetadataMaxBytes)); err != nil {
		return InspectResult{}, err
	}
	if out.Status == nil || out.ExitCode == nil || out.OOMKilled == nil ||
		!validBrokerInspectStatus(*out.Status) ||
		strings.TrimSpace(out.OwnerID) == "" || strings.TrimSpace(out.OwnerBootID) == "" {
		return InspectResult{}, ErrBrokerProtocolViolation
	}
	labelKey, labelValue, _ := strings.Cut(SandboxContainerLabel, "=")
	labels := map[string]string{
		labelKey:                          labelValue,
		SandboxContainerOwnerLabelKey:     out.OwnerID,
		SandboxContainerOwnerBootLabelKey: out.OwnerBootID,
	}
	return InspectResult{
		Status:    *out.Status,
		ExitCode:  *out.ExitCode,
		OOMKilled: *out.OOMKilled,
		Labels:    labels,
	}, nil
}

func validBrokerInspectStatus(status string) bool {
	switch status {
	case "running", "exited", "oom":
		return true
	default:
		return false
	}
}

func (c *brokerDockerClient) CopyToContainer(
	ctx context.Context,
	leaseID string,
	dstPath string,
	content io.Reader,
) error {
	if !isSafeBrokerCopyReader(content) {
		return ErrSandboxPolicyDenied
	}
	requestPath := brokerLeasePath(leaseID) + "/files?path=" + url.QueryEscape(dstPath)
	body := newMaxBytesReadCloser(ctx, content, int64(c.cfg.BrokerSingleFileMaxBytes), ErrInputTooLarge)
	defer body.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, brokerBaseURL()+requestPath, body)
	if err != nil {
		return fmt.Errorf("%w: build copy-in request", ErrBrokerProtocolViolation)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Numind-Request-ID", uuid.NewString())
	return c.doRequestNoContent(req)
}

func (c *brokerDockerClient) CopyFromContainer(
	ctx context.Context,
	leaseID string,
	srcPath string,
	hostDstDir string,
) error {
	if err := c.MarkPersisting(ctx, leaseID); err != nil {
		return err
	}
	requestPath := brokerLeasePath(leaseID) + "/files?path=" + url.QueryEscape(srcPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, brokerBaseURL()+requestPath, nil)
	if err != nil {
		return fmt.Errorf("%w: build copy-out request", ErrBrokerProtocolViolation)
	}
	req.Header.Set("X-Numind-Request-ID", uuid.NewString())
	resp, err := c.http.Do(req)
	if err != nil {
		return c.transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.decodeBrokerError(resp)
	}
	if err := extractBrokerTar(
		ctx,
		resp.Body,
		hostDstDir,
		int64(c.cfg.BrokerCopyOutMaxBytes),
		int64(c.cfg.BrokerSingleFileMaxBytes),
		c.cfg.BrokerMaxFiles,
	); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return c.transportError(ctx, err)
	}
	if resp.Trailer.Get(BrokerStreamStatusTrailer) != BrokerStreamStatusComplete {
		return ErrBrokerUnavailable
	}
	return nil
}

func (c *brokerDockerClient) ExecMkdir(ctx context.Context, leaseID string, dirs ...string) error {
	if len(dirs) == 0 {
		return nil
	}
	return c.doNoContent(ctx, http.MethodPost, brokerLeasePath(leaseID)+"/mkdir", BrokerMkdirRequest{
		RequestID: uuid.NewString(),
		Dirs:      append([]string(nil), dirs...),
	})
}

func (c *brokerDockerClient) ListByLabel(ctx context.Context, label string) ([]string, error) {
	if label != SandboxContainerLabel {
		return nil, ErrSandboxPolicyDenied
	}
	requestPath := BrokerAPIPrefix + "/leases?owner_id=" + url.QueryEscape(c.ownerID)
	var out struct {
		LeaseIDs *[]string `json:"lease_ids"`
	}
	if err := c.doJSON(ctx, http.MethodGet, requestPath, nil, &out, int64(c.cfg.BrokerMetadataMaxBytes)); err != nil {
		return nil, err
	}
	if out.LeaseIDs == nil {
		return nil, ErrBrokerProtocolViolation
	}
	seen := make(map[string]struct{}, len(*out.LeaseIDs))
	for _, leaseID := range *out.LeaseIDs {
		if strings.TrimSpace(leaseID) == "" {
			return nil, ErrBrokerProtocolViolation
		}
		if _, exists := seen[leaseID]; exists {
			return nil, ErrBrokerProtocolViolation
		}
		seen[leaseID] = struct{}{}
	}
	return append([]string(nil), (*out.LeaseIDs)...), nil
}

func (c *brokerDockerClient) doNoContent(ctx context.Context, method string, requestPath string, body any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%w: encode request", ErrBrokerProtocolViolation)
		}
		if len(payload) > c.cfg.BrokerMetadataMaxBytes {
			return ErrBrokerResponseTooLarge
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, brokerBaseURL()+requestPath, reader)
	if err != nil {
		return fmt.Errorf("%w: build request", ErrBrokerProtocolViolation)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-Numind-Request-ID", requestIDFromMutationBody(body))
	}
	return c.doRequestNoContent(req)
}

func (c *brokerDockerClient) doRequestNoContent(req *http.Request) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return c.transportError(req.Context(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.decodeBrokerError(resp)
	}
	_, err = readBounded(req.Context(), resp.Body, int64(c.cfg.BrokerMetadataMaxBytes))
	return err
}

func (c *brokerDockerClient) doJSON(
	ctx context.Context,
	method string,
	requestPath string,
	in any,
	out any,
	maxResponse int64,
) error {
	var reader io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%w: encode request", ErrBrokerProtocolViolation)
		}
		if len(payload) > c.cfg.BrokerMetadataMaxBytes {
			return ErrBrokerResponseTooLarge
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, brokerBaseURL()+requestPath, reader)
	if err != nil {
		return fmt.Errorf("%w: build request", ErrBrokerProtocolViolation)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-Numind-Request-ID", requestIDFromMutationBody(in))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.decodeBrokerError(resp)
	}
	payload, err := readBounded(ctx, resp.Body, maxResponse)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: decode response", ErrBrokerProtocolViolation)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("%w: multiple JSON values", ErrBrokerProtocolViolation)
	}
	return nil
}

func (c *brokerDockerClient) decodeBrokerError(resp *http.Response) error {
	payload, err := readBounded(resp.Request.Context(), resp.Body, int64(c.cfg.BrokerMetadataMaxBytes))
	if err != nil {
		return err
	}
	var envelope BrokerErrorResponse
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		return fmt.Errorf("%w: HTTP %d", ErrBrokerProtocolViolation, resp.StatusCode)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return ErrBrokerProtocolViolation
	}
	return brokerErrorSentinel(envelope.Error.Code)
}

func (c *brokerDockerClient) transportError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, ErrInputTooLarge) || errors.Is(err, ErrOutputTooLarge) {
		return err
	}
	c.logger.Warnw("sandbox broker request failed", "error_type", fmt.Sprintf("%T", err))
	return ErrBrokerUnavailable
}

func brokerErrorSentinel(code BrokerErrorCode) error {
	switch code {
	case BrokerErrorCapacity:
		return ErrPoolExhausted
	case BrokerErrorUnavailable:
		return ErrBrokerUnavailable
	case BrokerErrorPolicyDenied:
		return ErrSandboxPolicyDenied
	case BrokerErrorNotFound:
		return ErrBrokerProtocolViolation
	case BrokerErrorTimeout:
		return errors.Join(ErrSandboxTimeout, context.DeadlineExceeded)
	case BrokerErrorOOM:
		return ErrSandboxOOM
	case BrokerErrorInputTooLarge:
		return ErrInputTooLarge
	case BrokerErrorOutputTooLarge:
		return ErrOutputTooLarge
	default:
		return ErrBrokerProtocolViolation
	}
}

func brokerBaseURL() string {
	// Host is intentionally synthetic; the custom Transport always dials the
	// configured Unix socket and never resolves this name.
	return "http://sandboxd"
}

func brokerLeasePath(leaseID string) string {
	return BrokerAPIPrefix + "/leases/" + url.PathEscape(leaseID)
}

func readBounded(ctx context.Context, r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		return nil, ErrBrokerResponseTooLarge
	}
	payload, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, ErrBrokerUnavailable
	}
	if int64(len(payload)) > max {
		return nil, ErrBrokerResponseTooLarge
	}
	return payload, nil
}

type maxBytesReadCloser struct {
	reader    io.Reader
	remain    int64
	err       error
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func newMaxBytesReadCloser(ctx context.Context, reader io.Reader, remain int64, limitErr error) *maxBytesReadCloser {
	body := &maxBytesReadCloser{
		reader: reader,
		remain: remain,
		err:    limitErr,
		done:   make(chan struct{}),
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = body.Close()
		case <-body.done:
		}
	}()
	return body
}

func (r *maxBytesReadCloser) Read(p []byte) (int, error) {
	if r.remain > 0 {
		if int64(len(p)) > r.remain {
			p = p[:r.remain]
		}
		n, err := r.reader.Read(p)
		r.remain -= int64(n)
		return n, err
	}
	var probe [1]byte
	n, err := r.reader.Read(probe[:])
	if n > 0 {
		return 0, r.err
	}
	return 0, err
}

func (r *maxBytesReadCloser) Close() error {
	r.closeOnce.Do(func() {
		close(r.done)
		if closer, ok := r.reader.(io.Closer); ok {
			r.closeErr = closer.Close()
		}
	})
	return r.closeErr
}

func extractBrokerTar(
	ctx context.Context,
	r io.Reader,
	dest string,
	maxTotal int64,
	maxSingle int64,
	maxFiles int,
) error {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return ErrBrokerUnavailable
	}
	rootFD, err := unix.Open(dest, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return stableBrokerFileError(err)
	}
	defer unix.Close(rootFD)

	tr := tar.NewReader(io.LimitReader(r, maxTotal+(2<<20)+1))
	var total int64
	files := 0
	entries := 0
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return ErrBrokerUnavailable
			}
			return ErrBrokerProtocolViolation
		}
		entries++
		if entries > maxFiles*2+4 {
			return ErrOutputTooLarge
		}
		parts, err := safeBrokerOutputParts(hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			dirFD, openErr := openBrokerDirAt(rootFD, parts, true)
			if openErr != nil {
				return stableBrokerFileError(openErr)
			}
			_ = unix.Close(dirFD)
		case tar.TypeReg:
			files++
			if files > maxFiles || hdr.Size < 0 || hdr.Size > maxSingle || total+hdr.Size > maxTotal {
				return ErrOutputTooLarge
			}
			total += hdr.Size
			parentFD, openErr := openBrokerDirAt(rootFD, parts[:len(parts)-1], true)
			if openErr != nil {
				return stableBrokerFileError(openErr)
			}
			fd, openErr := unix.Openat(
				parentFD,
				parts[len(parts)-1],
				unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
				0o600,
			)
			_ = unix.Close(parentFD)
			if openErr != nil {
				return stableBrokerFileError(openErr)
			}
			f := os.NewFile(uintptr(fd), "broker-output")
			_, copyErr := io.CopyN(f, tr, hdr.Size)
			closeErr := f.Close()
			if copyErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				if errors.Is(copyErr, io.ErrUnexpectedEOF) {
					return ErrBrokerUnavailable
				}
				return ErrBrokerProtocolViolation
			}
			if closeErr != nil {
				return ErrBrokerUnavailable
			}
		default:
			return ErrSandboxPolicyDenied
		}
	}
}

func safeBrokerOutputParts(name string) ([]string, error) {
	clean := path.Clean(strings.TrimSpace(name))
	if clean == "." || clean == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, ErrSandboxPolicyDenied
	}
	parts := strings.Split(clean, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, 0) {
			return nil, ErrSandboxPolicyDenied
		}
	}
	return parts, nil
}

func openBrokerDirAt(rootFD int, parts []string, create bool) (int, error) {
	currentFD, err := unix.Dup(rootFD)
	if err != nil {
		return -1, err
	}
	for _, part := range parts {
		if create {
			if mkdirErr := unix.Mkdirat(currentFD, part, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(currentFD)
				return -1, mkdirErr
			}
		}
		nextFD, openErr := unix.Openat(
			currentFD,
			part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0,
		)
		_ = unix.Close(currentFD)
		if openErr != nil {
			return -1, openErr
		}
		currentFD = nextFD
	}
	return currentFD, nil
}

func stableBrokerFileError(err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EEXIST) {
		return ErrSandboxPolicyDenied
	}
	return ErrBrokerUnavailable
}

func isSafeBrokerCopyReader(reader io.Reader) bool {
	if reader == nil {
		return false
	}
	switch reader.(type) {
	case *bytes.Buffer, *bytes.Reader, *strings.Reader, *io.PipeReader:
		return true
	default:
		return false
	}
}

func requestIDFromMutationBody(body any) string {
	switch req := body.(type) {
	case BrokerCreateLeaseRequest:
		return req.RequestID
	case BrokerExecRequest:
		return req.RequestID
	case BrokerActivateRequest:
		return req.RequestID
	case BrokerMutationRequest:
		return req.RequestID
	case BrokerMkdirRequest:
		return req.RequestID
	default:
		return uuid.NewString()
	}
}

// Close releases idle Unix connections owned by this client.
func (c *brokerDockerClient) Close() error {
	c.transport.CloseIdleConnections()
	return nil
}
