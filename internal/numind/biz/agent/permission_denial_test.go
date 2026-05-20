package agent

import (
	"strings"
	"testing"
)

func TestPermissionDenialDetail_String_NilSafe(t *testing.T) {
	var d *PermissionDenialDetail
	out := d.String()
	if out != "<nil PermissionDenialDetail>" {
		t.Errorf("nil String() = %q", out)
	}
}

func TestPermissionDenialDetail_String_Filled(t *testing.T) {
	d := &PermissionDenialDetail{
		ToolName:       "bash_exec",
		Behavior:       "deny",
		DecisionReason: "rule",
		ValidatorID:    "PlatformHardRule:ControlChar",
		Message:        "命令含控制字符",
	}
	out := d.String()
	if !strings.Contains(out, "bash_exec") {
		t.Errorf("String() missing tool_name: %q", out)
	}
	if !strings.Contains(out, "PlatformHardRule:ControlChar") {
		t.Errorf("String() missing validator_id: %q", out)
	}
}
