package validators

import (
	"context"
	"encoding/json"
	"strings"

	"numind-server/internal/numind/biz/permission"
)

type WorkingDir struct {
	allowedPrefix string
}

func NewWorkingDir(prefix string) permission.Validator {
	if prefix == "" {
		prefix = "/workdir/"
	}
	return &WorkingDir{allowedPrefix: prefix}
}

func (v *WorkingDir) ID() string { return "WorkingDir" }

func (v *WorkingDir) Validate(_ context.Context, req permission.PermissionRequest) permission.PermissionResult {
	if req.Tool == nil || !strings.HasPrefix(req.Tool.Name(), "file_") {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "not file_ tool")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(req.InputJSON), &m); err != nil {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "input not JSON")
	}
	path, _ := m["path"].(string)
	if path == "" {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no path field")
	}
	if !strings.HasPrefix(path, v.allowedPrefix) {
		return permission.Deny(v.ID(), permission.DecisionReasonWorkingDir,
			"文件路径必须在 "+v.allowedPrefix+" 下")
	}
	return permission.Passthrough(v.ID(), permission.DecisionReasonWorkingDir, "path in allowed dir")
}
