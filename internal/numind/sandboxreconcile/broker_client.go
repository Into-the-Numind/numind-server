package sandboxreconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	brokerAPIPrefix          = "/v1"
	brokerMetadataMaxBytes   = 64 << 10
	defaultBrokerHTTPTimeout = 10 * time.Second
)

var safeBrokerTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type BrokerUnixClient struct {
	socket string
	http   *http.Client
}

func NewBrokerUnixClient(socket string) (*BrokerUnixClient, error) {
	if strings.TrimSpace(socket) == "" || !filepath.IsAbs(socket) {
		return nil, ErrInvalidConfig
	}
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	return &BrokerUnixClient{
		socket: socket,
		http: &http.Client{
			Transport: transport,
			Timeout:   defaultBrokerHTTPTimeout,
		},
	}, nil
}

func (c *BrokerUnixClient) ListRecoveryPendingLeases(
	ctx context.Context,
	limit int,
) ([]LeaseRef, error) {
	if c == nil || limit <= 0 || limit > MaxLimit {
		return nil, ErrInvalidConfig
	}
	requestPath := brokerAPIPrefix + "/recovery-pending?limit=" + strconv.Itoa(limit)
	var out struct {
		Leases []struct {
			LeaseID           string `json:"lease_id"`
			AgentRunID        uint64 `json:"agent_run_id"`
			SandboxSessionID  uint64 `json:"sandbox_session_id"`
			State             string `json:"state"`
			TerminationReason string `json:"termination_reason"`
		} `json:"leases"`
	}
	if err := c.doJSON(ctx, http.MethodGet, requestPath, nil, &out); err != nil {
		return nil, err
	}
	leases := make([]LeaseRef, 0, len(out.Leases))
	seen := make(map[string]struct{}, len(out.Leases))
	for _, lease := range out.Leases {
		if !safeBrokerToken(lease.LeaseID) {
			return nil, ErrInvalidConfig
		}
		if _, ok := seen[lease.LeaseID]; ok {
			return nil, ErrInvalidConfig
		}
		seen[lease.LeaseID] = struct{}{}
		leases = append(leases, LeaseRef{
			LeaseID:           lease.LeaseID,
			AgentRunID:        lease.AgentRunID,
			SandboxSessionID:  lease.SandboxSessionID,
			State:             lease.State,
			TerminationReason: lease.TerminationReason,
		})
	}
	return leases, nil
}

func (c *BrokerUnixClient) MarkLeaseReconciled(
	ctx context.Context,
	leaseID string,
) error {
	if c == nil || !safeBrokerToken(leaseID) {
		return ErrInvalidConfig
	}
	requestPath := brokerAPIPrefix + "/recovery-pending/" +
		url.PathEscape(leaseID) + "/reconciled"
	return c.doJSON(ctx, http.MethodPost, requestPath, struct {
		RequestID string `json:"request_id"`
	}{RequestID: uuid.NewString()}, nil)
}

func (c *BrokerUnixClient) doJSON(
	ctx context.Context,
	method string,
	requestPath string,
	input any,
	output any,
) error {
	if c == nil {
		return ErrInvalidConfig
	}
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return ErrInvalidConfig
		}
		if len(payload) > brokerMetadataMaxBytes {
			return ErrInvalidConfig
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(
		ctx,
		method,
		"http://sandboxd.local"+requestPath,
		body,
	)
	if err != nil {
		return ErrInvalidConfig
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: broker request failed", ErrRunFailed)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, brokerMetadataMaxBytes))
		return fmt.Errorf("%w: broker status %d", ErrRunFailed, resp.StatusCode)
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, brokerMetadataMaxBytes))
		return nil
	}
	limited := io.LimitReader(resp.Body, brokerMetadataMaxBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("%w: broker response read failed", ErrRunFailed)
	}
	if len(payload) > brokerMetadataMaxBytes {
		return fmt.Errorf("%w: broker response too large", ErrRunFailed)
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return fmt.Errorf("%w: broker response invalid", ErrRunFailed)
	}
	return nil
}

func safeBrokerToken(value string) bool {
	return safeBrokerTokenPattern.MatchString(value)
}
