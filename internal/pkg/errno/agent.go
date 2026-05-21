package errno

var (
	// ErrAgentRunNotFound is returned when an agent_run ID does not exist.
	ErrAgentRunNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.AgentRunNotFound", Message: "Agent run was not found."}

	// ErrAgentRunNotCancellable is returned when a cancel is attempted on an
	// already-terminal agent run (completed / failed / cancelled / error).
	ErrAgentRunNotCancellable = &Errno{HTTP: 409, Code: "FailedOperation.AgentRunNotCancellable", Message: "Agent run is already in a terminal state and cannot be cancelled."}
)
