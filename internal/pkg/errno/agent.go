package errno

var (
	// ErrAgentRunNotFound is returned when an agent_run ID does not exist.
	ErrAgentRunNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.AgentRunNotFound", Message: "Agent run was not found."}

	// ErrAgentRunNotCancellable is returned when a cancel is attempted on an
	// already-terminal agent run (completed / failed / cancelled / error).
	ErrAgentRunNotCancellable = &Errno{HTTP: 409, Code: "FailedOperation.AgentRunNotCancellable", Message: "Agent run is already in a terminal state and cannot be cancelled."}

	// ErrInvalidInput is returned when a tool receives invalid or unsafe input
	// (e.g. malformed URL, SSRF-blocked address, disallowed file path).
	ErrInvalidInput = &Errno{HTTP: 400, Code: "InvalidParameter.ToolInput", Message: "invalid tool input"}

	// ErrExternalAPI is returned when an outbound HTTP call to an external
	// service fails (network error, 4xx, 5xx, etc.).
	ErrExternalAPI = &Errno{HTTP: 502, Code: "FailedOperation.ExternalAPI", Message: "external API call failed"}

	// ErrTimeout is returned when a tool operation exceeds its deadline
	// (e.g. web_fetch 30s, web_search 5s).
	ErrTimeout = &Errno{HTTP: 504, Code: "FailedOperation.ToolTimeout", Message: "tool operation timed out"}
)
