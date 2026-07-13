package agent

import "testing"

// TestFrontendStatus covers the backend agent_run.status + state_reason →
// frontend AgentRunStatus mapping. The waiting_for_user_choice case is the
// T2 regression target (agent-stream-interactivity): a run paused for an
// ask_user_question answer must surface as active "running", not the default
// "failed" — otherwise the chat header shows a misleading failure badge while
// the question is pending (affects both streaming and polling paths).
func TestFrontendStatus(t *testing.T) {
	cases := []struct {
		name        string
		status      string
		stateReason string
		want        string
	}{
		{"running", "running", "", "running"},
		{"pending", "pending", "", "pending"},
		{"completed", "terminated", "completed", "completed"},
		{"budget_exhausted", "terminated", "error_max_budget", "budget_exhausted"},
		{"timeout", "terminated", "max_turns", "timeout"},
		{"cancelled", "terminated", "cancelled", "cancelled"},
		{"aborted_streaming_to_cancelled", "terminated", "aborted_streaming", "cancelled"},
		{"aborted_tools_to_cancelled", "terminated", "aborted_tools", "cancelled"},
		{"waiting_for_user_choice_to_running", "terminated", "waiting_for_user_choice", "running"},
		{"external_resume_ready_to_running", "terminated", "external_resume_ready", "running"},
		{"external_resume_starting_to_running", "running", "ext_resume:0123456789abcdef0123456789abcdef", "running"},
		{"unknown_to_failed", "terminated", "model_error", "failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := frontendStatus(c.status, c.stateReason); got != c.want {
				t.Fatalf("frontendStatus(%q, %q) = %q, want %q", c.status, c.stateReason, got, c.want)
			}
		})
	}
}
