package errno

import (
	"errors"
	"fmt"
)

// Errno 定义了 miniblog 使用的错误类型.
type Errno struct {
	HTTP    int
	Code    string
	Message string
}

// Error 实现 error 接口中的 `Error` 方法.
func (err *Errno) Error() string {
	return err.Message
}

// SetMessage 设置 Errno 类型错误中的 Message 字段 (返回副本以避免篡改全局变量).
func (err *Errno) SetMessage(format string, args ...interface{}) *Errno {
	return &Errno{
		HTTP:    err.HTTP,
		Code:    err.Code,
		Message: fmt.Sprintf(format, args...),
	}
}

// Is enables errors.Is matching by comparing Code fields.
// This allows wrapped *Errno values (e.g. fmt.Errorf("...: %w", errno.ErrXxx))
// to be matched with errors.Is(err, errno.ErrXxx).
func (err *Errno) Is(target error) bool {
	t, ok := target.(*Errno)
	if !ok {
		return false
	}
	return err.Code == t.Code
}

// Decode 尝试从 err 中解析出业务错误码和错误信息.
//
// 通过 errors.As 解 wrap 链 — 业务层常用 fmt.Errorf("...: %w", errno.ErrXxx)
// 包装底层 cause；直接 type-switch 会漏匹配 *fmt.wrapError 让 HTTP code 退
// 回 500，掩盖真实 HTTP 语义（agent-mode-v2-skill-marketplace T7 修复）。
// 任意层级的 *Errno 都能被检出。
func Decode(err error) (int, string, string) {
	if err == nil {
		return OK.HTTP, OK.Code, OK.Message
	}

	var typed *Errno
	if errors.As(err, &typed) {
		return typed.HTTP, typed.Code, typed.Message
	}

	// 默认返回未知错误码和错误信息. 该错误代表服务端出错
	return InternalServerError.HTTP, InternalServerError.Code, err.Error()
}
