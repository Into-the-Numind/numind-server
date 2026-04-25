package contextbudget

// FragmentRole classifies how a fragment participates in the context budget lifecycle.
type FragmentRole string

const (
	// RoleImmutable marks fragments that must always be present (e.g., system prompt).
	RoleImmutable FragmentRole = "immutable"
	// RoleRecent marks recent conversation turns to be preserved when possible.
	RoleRecent FragmentRole = "recent"
	// RoleDurable marks persistent context that can be summarized but not dropped.
	RoleDurable FragmentRole = "durable"
	// RoleEvidence marks retrieved knowledge base or document fragments.
	RoleEvidence FragmentRole = "evidence"
	// RoleWorking marks fragments for the current working session.
	RoleWorking FragmentRole = "working"
	// RoleDiscardable marks fragments that may be dropped under budget pressure.
	RoleDiscardable FragmentRole = "discardable"
)

// FragmentSource indicates where a fragment originated.
type FragmentSource string

const (
	// SourceSystem indicates a system-generated fragment (e.g., system prompt).
	SourceSystem FragmentSource = "system"
	// SourceUser indicates a user message.
	SourceUser FragmentSource = "user"
	// SourceAssistant indicates an assistant response.
	SourceAssistant FragmentSource = "assistant"
	// SourceTool indicates a tool call or tool result.
	SourceTool FragmentSource = "tool"
	// SourceFile indicates content loaded from a file.
	SourceFile FragmentSource = "file"
	// SourceKB indicates content retrieved from a knowledge base.
	SourceKB FragmentSource = "kb"
	// SourceDB indicates content retrieved from a database.
	SourceDB FragmentSource = "db"
	// SourceWeb indicates content retrieved from the web.
	SourceWeb FragmentSource = "web"
	// SourceInternal indicates internally generated content (e.g., summaries).
	SourceInternal FragmentSource = "internal"
)

// ContentType describes the semantic format of a fragment's content.
type ContentType string

const (
	// ContentText is plain or formatted text.
	ContentText ContentType = "text"
	// ContentAttachment is binary or non-text attachment metadata.
	ContentAttachment ContentType = "attachment"
	// ContentToolResult is the result returned by a tool invocation.
	ContentToolResult ContentType = "tool_result"
	// ContentReasoning is model reasoning / chain-of-thought output.
	ContentReasoning ContentType = "reasoning"
	// ContentSummary is a compressed summary replacing one or more original fragments.
	ContentSummary ContentType = "summary"
	// ContentStructuredData is structured data (JSON, tables, etc.).
	ContentStructuredData ContentType = "structured_data"
)

// Compressibility describes what compression operations are allowed on a fragment.
type Compressibility string

const (
	// CompressNone means the fragment must not be compressed or dropped.
	CompressNone Compressibility = "none"
	// CompressSummarize means the fragment may be replaced with an LLM-generated summary.
	CompressSummarize Compressibility = "summarize"
	// CompressReference means the fragment may be replaced by a lightweight reference pointer.
	CompressReference Compressibility = "reference"
	// CompressDrop means the fragment may be silently dropped.
	CompressDrop Compressibility = "drop"
)

// ContextFragment is the atomic unit managed by the context budget system.
//
// The Metadata field may carry arbitrary business keys (e.g., sop_run_id, node_id,
// chat_session_id) for caller tracing purposes. The planner and estimator MUST ignore
// Metadata keys entirely — they must not influence token estimation or ranking.
// Exception: the framework-level key "critical_reason" is read by isCritical() as
// authorized by spec §2.2 — it marks a fragment as critical regardless of its Role.
type ContextFragment struct {
	// ID uniquely identifies this fragment within a planning session.
	ID string `json:"id"`
	// Role classifies the fragment's lifecycle role.
	Role FragmentRole `json:"role"`
	// Source indicates the origin of the fragment.
	Source FragmentSource `json:"source"`
	// ContentType describes the semantic format of Content.
	ContentType ContentType `json:"content_type"`
	// Content holds the text body. May be empty only when Compressibility==CompressReference
	// and SourceReference is non-empty.
	Content string `json:"content"`
	// Importance is a caller-assigned priority [0, 10]; higher = more important.
	Importance int `json:"importance"`
	// Order is a monotonically increasing sequence number; lower = older.
	Order int `json:"order"`
	// Compressibility describes what compression operations are allowed.
	Compressibility Compressibility `json:"compressibility"`
	// Critical, when true, prevents the fragment from being dropped under any budget pressure.
	Critical bool `json:"critical"`
	// ParentID optionally links this fragment to a parent (e.g., a message group).
	ParentID string `json:"parent_id,omitempty"`
	// SourceReference is a URI or key pointing to the original source, used for reference compression.
	SourceReference string `json:"source_reference,omitempty"`
	// Metadata carries opaque business key-value pairs for caller tracing.
	// The planner and estimator MUST NOT read or act on this field.
	Metadata map[string]string `json:"metadata,omitempty"`
	// TokenEstimate is an optional pre-computed token count; zero means not yet estimated.
	TokenEstimate int `json:"token_estimate,omitempty"`
}

// isCritical returns true if the fragment is protected from being dropped.
//
// A fragment is critical if any of the following hold:
//   - Critical field is true
//   - Role is RoleImmutable
//   - Source is SourceSystem and Compressibility is CompressNone
//   - Metadata contains a non-empty "critical_reason" key (spec §2.2 framework exception)
func isCritical(f ContextFragment) bool {
	if f.Critical {
		return true
	}
	if f.Role == RoleImmutable {
		return true
	}
	if f.Source == SourceSystem && f.Compressibility == CompressNone {
		return true
	}
	if f.Metadata != nil && f.Metadata["critical_reason"] != "" {
		return true
	}
	return false
}
