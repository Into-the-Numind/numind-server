package sandbox

import "time"

const (
	BrokerAPIPrefix = "/v1"

	DefaultBrokerSocket             = "/run/numind-sandbox/sandboxd.sock"
	DefaultBrokerMetadataMaxBytes   = 64 << 10
	DefaultBrokerExecOutputMaxBytes = 4 << 20
	DefaultBrokerCopyInMaxBytes     = 100 << 20
	DefaultBrokerCopyOutMaxBytes    = 200 << 20
	DefaultBrokerSingleFileMaxBytes = 50 << 20
	DefaultBrokerMaxFiles           = 10
	DefaultBrokerMaxConnections     = 32
)

type BrokerErrorCode string

const (
	BrokerErrorCapacity       BrokerErrorCode = "capacity"
	BrokerErrorUnavailable    BrokerErrorCode = "unavailable"
	BrokerErrorPolicyDenied   BrokerErrorCode = "policy_denied"
	BrokerErrorNotFound       BrokerErrorCode = "not_found"
	BrokerErrorTimeout        BrokerErrorCode = "timeout"
	BrokerErrorOOM            BrokerErrorCode = "oom"
	BrokerErrorInputTooLarge  BrokerErrorCode = "input_too_large"
	BrokerErrorOutputTooLarge BrokerErrorCode = "output_too_large"
	BrokerErrorProtocol       BrokerErrorCode = "protocol_error"
)

type BrokerErrorBody struct {
	Code      BrokerErrorCode `json:"code"`
	Message   string          `json:"message,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
}

type BrokerErrorResponse struct {
	Error BrokerErrorBody `json:"error"`
}

type BrokerCreateLeaseRequest struct {
	RequestID   string `json:"request_id"`
	OwnerID     string `json:"owner_id"`
	OwnerBootID string `json:"owner_boot_id"`
}

type BrokerCreateLeaseResponse struct {
	LeaseID   string    `json:"lease_id"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

type BrokerExecRequest struct {
	RequestID string   `json:"request_id"`
	Argv      []string `json:"argv"`
	Env       []string `json:"env,omitempty"`
}

type BrokerExecResponse struct {
	Stdout   string        `json:"stdout,omitempty"`
	Stderr   string        `json:"stderr,omitempty"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
}

type BrokerInspectResponse struct {
	Status      string `json:"status"`
	ExitCode    int    `json:"exit_code"`
	OOMKilled   bool   `json:"oom_killed"`
	OwnerID     string `json:"owner_id,omitempty"`
	OwnerBootID string `json:"owner_boot_id,omitempty"`
}

type BrokerMkdirRequest struct {
	RequestID string   `json:"request_id"`
	Dirs      []string `json:"dirs"`
}

type BrokerListLeasesResponse struct {
	LeaseIDs []string `json:"lease_ids"`
}
