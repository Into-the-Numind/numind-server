package validators

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/permission"
)

func TestWorkingDir_NonFileTool_Passthrough(t *testing.T) {
	v := NewWorkingDir("/workdir/")
	req := permission.PermissionRequest{
		Tool:      newFakeTool("bash_exec"),
		InputJSON: mustJSON(map[string]any{"path": "/etc/passwd"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for non-file_ tool, got %q", got.Behavior)
	}
}

func TestWorkingDir_NilTool_Passthrough(t *testing.T) {
	v := NewWorkingDir("/workdir/")
	req := permission.PermissionRequest{
		Tool:      nil,
		InputJSON: mustJSON(map[string]any{"path": "/etc/passwd"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for nil tool, got %q", got.Behavior)
	}
}

func TestWorkingDir_AllowedPath_Passthrough(t *testing.T) {
	v := NewWorkingDir("/workdir/")
	req := permission.PermissionRequest{
		Tool:      newFakeTool("file_read"),
		InputJSON: mustJSON(map[string]any{"path": "/workdir/output.txt"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for allowed path, got %q", got.Behavior)
	}
}

func TestWorkingDir_DisallowedPath_Deny(t *testing.T) {
	v := NewWorkingDir("/workdir/")
	req := permission.PermissionRequest{
		Tool:      newFakeTool("file_read"),
		InputJSON: mustJSON(map[string]any{"path": "/etc/passwd"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorDeny {
		t.Errorf("want deny for /etc/passwd, got %q", got.Behavior)
	}
	if got.DecisionReason != permission.DecisionReasonWorkingDir {
		t.Errorf("want reason=workingDir, got %q", got.DecisionReason)
	}
}

func TestWorkingDir_NoPathField_Passthrough(t *testing.T) {
	v := NewWorkingDir("/workdir/")
	req := permission.PermissionRequest{
		Tool:      newFakeTool("file_write"),
		InputJSON: mustJSON(map[string]any{"content": "hello"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for no path field, got %q", got.Behavior)
	}
}

func TestWorkingDir_DefaultPrefix(t *testing.T) {
	// Empty prefix should default to /workdir/
	v := NewWorkingDir("")
	req := permission.PermissionRequest{
		Tool:      newFakeTool("file_read"),
		InputJSON: mustJSON(map[string]any{"path": "/workdir/data.txt"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough with default prefix, got %q", got.Behavior)
	}
}
