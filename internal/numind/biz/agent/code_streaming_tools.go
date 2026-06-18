package agent

// codeStreamingTools is the allowlist of tools whose function arguments ARE the
// generated code / document content the user wants to watch stream in (BE-1).
// For these, the runner forwards EventToolCallArgsDelta so the frontend can show
// a live, collapsible "writing code" box. Every other tool (web_search,
// kb_search, ask_user_question, …) is excluded — their arguments are control
// parameters, not content, and streaming them would be noise.
//
// Kept as a set (not a length/size heuristic) on purpose: the gate is
// deterministic and predictable, avoiding flicker from chunk-size guessing.
var codeStreamingTools = map[string]struct{}{
	"run_python":       {},
	"create_html":      {},
	"create_docx":      {},
	"create_csv":       {},
	"create_json":      {},
	"create_text":      {},
	"create_png_chart": {},
}

// isCodeStreamingTool reports whether tool-call argument deltas for the named
// tool should be streamed to the client as a live "writing code" view.
func isCodeStreamingTool(name string) bool {
	_, ok := codeStreamingTools[name]
	return ok
}
