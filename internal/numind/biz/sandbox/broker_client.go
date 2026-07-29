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
	"time"

	"github.com/google/uuid"
)

type brokerDockerClient struct {
	cfg         SandboxConfig
	socket      string
	ownerID     string
	ownerBootID string
	http        *http.Client
	logger      Logger
}

var _ DockerClient = (*brokerDockerClient)(nil)

// NewBrokerDockerClient creates a DockerClient-compatible client for sandboxd.
// The returned client has no Docker CLI or socket access.
func NewBrokerDockerClient(cfg SandboxConfig, logger Logger) (DockerClient, error) {
	if logger == nil {
		logger = nopLogger{}
	}
	if strings.TrimSpace(cfg.BrokerSocket) == "" || !filepath.IsAbs(cfg.BrokerSocket) {
		return nil, fmt.Errorf("%w: broker socket must be an absolute path", ErrBrokerProtocolViolation)
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
		ownerID:     defaultSandboxOwnerID(),
		ownerBootID: currentSandboxOwnerBootID(),
		http:        &http.Client{Transport: transport},
		logger:      logger,
	}, nil
}

func validateBrokerClientLimits(cfg SandboxConfig) error {
	values := map[string]int{
		"metadata max bytes":    cfg.BrokerMetadataMaxBytes,
		"exec output max bytes": cfg.BrokerExecOutputMaxBytes,
		"copy-in max bytes":     cfg.BrokerCopyInMaxBytes,
		"copy-out max bytes":    cfg.BrokerCopyOutMaxBytes,
		"single-file max bytes": cfg.BrokerSingleFileMaxBytes,
		"max files":             cfg.BrokerMaxFiles,
		"max connections":       cfg.BrokerMaxConnections,
	}
	for name, value := range values {
		if value <= 0 {
			return fmt.Errorf("%w: broker %s must be positive", ErrBrokerProtocolViolation, name)
		}
	}
	if cfg.BrokerSingleFileMaxBytes > cfg.BrokerCopyInMaxBytes ||
		cfg.BrokerSingleFileMaxBytes > cfg.BrokerCopyOutMaxBytes {
		return fmt.Errorf("%w: single-file limit exceeds copy limit", ErrBrokerProtocolViolation)
	}
	return nil
}

func (c *brokerDockerClient) Spawn(ctx context.Context, cfg SpawnConfig) (string, error) {
	ownerID, ownerBootID := brokerOwnerFromLabels(cfg.Labels, c.ownerID, c.ownerBootID)
	var out BrokerCreateLeaseResponse
	err := c.doJSON(ctx, http.MethodPost, BrokerAPIPrefix+"/leases", BrokerCreateLeaseRequest{
		RequestID:   uuid.NewString(),
		OwnerID:     ownerID,
		OwnerBootID: ownerBootID,
	}, &out, int64(c.cfg.BrokerMetadataMaxBytes))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out.LeaseID) == "" {
		return "", fmt.Errorf("%w: create lease returned an empty id", ErrBrokerProtocolViolation)
	}
	return out.LeaseID, nil
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
	var out BrokerExecResponse
	maxResponse := int64(c.cfg.BrokerExecOutputMaxBytes)*6 + int64(c.cfg.BrokerMetadataMaxBytes)
	err := c.doJSON(ctx, http.MethodPost, brokerLeasePath(leaseID)+"/exec", BrokerExecRequest{
		RequestID: uuid.NewString(),
		Argv:      append([]string(nil), cmd...),
		Env:       append([]string(nil), opts.Env...),
	}, &out, maxResponse)
	if err != nil {
		return ExecResult{}, err
	}
	if len(out.Stdout)+len(out.Stderr) > c.cfg.BrokerExecOutputMaxBytes {
		return ExecResult{}, ErrBrokerResponseTooLarge
	}
	return ExecResult(out), nil
}

func (c *brokerDockerClient) Destroy(ctx context.Context, leaseID string) error {
	return c.doNoContent(ctx, http.MethodDelete, brokerLeasePath(leaseID), nil)
}

func (c *brokerDockerClient) Inspect(ctx context.Context, leaseID string) (InspectResult, error) {
	var out BrokerInspectResponse
	if err := c.doJSON(ctx, http.MethodGet, brokerLeasePath(leaseID), nil, &out, int64(c.cfg.BrokerMetadataMaxBytes)); err != nil {
		return InspectResult{}, err
	}
	labels := map[string]string{SandboxContainerLabel[:len(SandboxContainerLabel)-2]: "1"}
	if out.OwnerID != "" {
		labels[SandboxContainerOwnerLabelKey] = out.OwnerID
	}
	if out.OwnerBootID != "" {
		labels[SandboxContainerOwnerBootLabelKey] = out.OwnerBootID
	}
	return InspectResult{
		Status:    out.Status,
		ExitCode:  out.ExitCode,
		OOMKilled: out.OOMKilled,
		Labels:    labels,
	}, nil
}

func (c *brokerDockerClient) CopyToContainer(
	ctx context.Context,
	leaseID string,
	dstPath string,
	content io.Reader,
) error {
	requestPath := brokerLeasePath(leaseID) + "/files?path=" + url.QueryEscape(dstPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, brokerBaseURL()+requestPath,
		&maxBytesReader{
			reader: content,
			remain: int64(c.cfg.BrokerSingleFileMaxBytes),
			err:    ErrInputTooLarge,
		})
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
	return extractBrokerTar(
		resp.Body,
		hostDstDir,
		int64(c.cfg.BrokerCopyOutMaxBytes),
		int64(c.cfg.BrokerSingleFileMaxBytes),
		c.cfg.BrokerMaxFiles,
	)
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
	var out BrokerListLeasesResponse
	if err := c.doJSON(ctx, http.MethodGet, requestPath, nil, &out, int64(c.cfg.BrokerMetadataMaxBytes)); err != nil {
		return nil, err
	}
	return append([]string(nil), out.LeaseIDs...), nil
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
	_, err = readBounded(resp.Body, int64(c.cfg.BrokerMetadataMaxBytes))
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
	resp, err := c.http.Do(req)
	if err != nil {
		return c.transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.decodeBrokerError(resp)
	}
	payload, err := readBounded(resp.Body, maxResponse)
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
	payload, err := readBounded(resp.Body, int64(c.cfg.BrokerMetadataMaxBytes))
	if err != nil {
		return err
	}
	var envelope BrokerErrorResponse
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		return fmt.Errorf("%w: HTTP %d", ErrBrokerProtocolViolation, resp.StatusCode)
	}
	sentinel := brokerErrorSentinel(envelope.Error.Code)
	return fmt.Errorf("%w: code=%s", sentinel, envelope.Error.Code)
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
		return ErrSandboxTimeout
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

func brokerOwnerFromLabels(labels []string, defaultOwner string, defaultBoot string) (string, string) {
	ownerID, ownerBootID := defaultOwner, defaultBoot
	for _, label := range labels {
		key, value, ok := strings.Cut(label, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		switch key {
		case SandboxContainerOwnerLabelKey:
			ownerID = value
		case SandboxContainerOwnerBootLabelKey:
			ownerBootID = value
		}
	}
	return ownerID, ownerBootID
}

func brokerBaseURL() string {
	// Host is intentionally synthetic; the custom Transport always dials the
	// configured Unix socket and never resolves this name.
	return "http://sandboxd"
}

func brokerLeasePath(leaseID string) string {
	return BrokerAPIPrefix + "/leases/" + url.PathEscape(leaseID)
}

func readBounded(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		return nil, ErrBrokerResponseTooLarge
	}
	payload, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read response", ErrBrokerProtocolViolation)
	}
	if int64(len(payload)) > max {
		return nil, ErrBrokerResponseTooLarge
	}
	return payload, nil
}

type maxBytesReader struct {
	reader io.Reader
	remain int64
	err    error
}

func (r *maxBytesReader) Read(p []byte) (int, error) {
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

func extractBrokerTar(r io.Reader, dest string, maxTotal int64, maxSingle int64, maxFiles int) error {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return fmt.Errorf("broker output mkdir: %w", err)
	}
	tr := tar.NewReader(io.LimitReader(r, maxTotal+(2<<20)+1))
	var total int64
	files := 0
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: invalid output tar", ErrBrokerProtocolViolation)
		}
		entries++
		if entries > maxFiles*2+4 {
			return ErrOutputTooLarge
		}
		cleanName, target, err := safeBrokerOutputPath(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("broker output mkdir %s: %w", cleanName, err)
			}
		case tar.TypeReg:
			files++
			if files > maxFiles || hdr.Size < 0 || hdr.Size > maxSingle || total+hdr.Size > maxTotal {
				return ErrOutputTooLarge
			}
			total += hdr.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf("broker output parent mkdir: %w", err)
			}
			if info, statErr := os.Lstat(target); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
				return ErrSandboxPolicyDenied
			} else if statErr != nil && !os.IsNotExist(statErr) {
				return fmt.Errorf("broker output lstat: %w", statErr)
			}
			f, openErr := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if openErr != nil {
				return fmt.Errorf("broker output create: %w", openErr)
			}
			_, copyErr := io.CopyN(f, tr, hdr.Size)
			closeErr := f.Close()
			if copyErr != nil {
				return fmt.Errorf("%w: truncated output file", ErrBrokerProtocolViolation)
			}
			if closeErr != nil {
				return fmt.Errorf("broker output close: %w", closeErr)
			}
		default:
			return ErrSandboxPolicyDenied
		}
	}
}

func safeBrokerOutputPath(dest string, name string) (string, string, error) {
	clean := path.Clean(strings.TrimSpace(name))
	if clean == "." || clean == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", ErrSandboxPolicyDenied
	}
	target := filepath.Join(dest, filepath.FromSlash(clean))
	rel, err := filepath.Rel(dest, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", ErrSandboxPolicyDenied
	}
	current := dest
	parts := strings.Split(filepath.FromSlash(clean), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", "", ErrSandboxPolicyDenied
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return "", "", fmt.Errorf("broker output lstat: %w", statErr)
		}
	}
	return clean, target, nil
}
