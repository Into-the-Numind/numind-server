package errno

// agent-mode-permission-pipeline #6 占位 errno（v1 本 feature 不在 controller 抛出；
// 预留给 #10/#11 controller 层使用——规则 CRUD 端点 / 学员端 deny 友好提示）。

var (
	// ErrPermissionGateUnavailable — 权限网关启动失败 / 不可用（启动期错误）
	ErrPermissionGateUnavailable = &Errno{HTTP: 500, Code: "BizError.Permission.GateUnavailable", Message: "权限网关不可用"}

	// ErrPermissionDenied — 工具调用被权限拒绝（运行时错误；v1 在 hook 内短路，不抛 controller）
	ErrPermissionDenied = &Errno{HTTP: 403, Code: "BizError.Permission.Denied", Message: "操作被拒绝"}
)
