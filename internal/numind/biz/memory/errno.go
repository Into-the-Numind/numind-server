package memory

import "numind-server/internal/pkg/errno"

// Memory subsystem error codes (agent-mode-memory-system #7/14).
var (
	// ErrMemoryKindInvalid is returned when the requested MemoryKind is not in
	// the allowed enum (see MemoryKind.Valid).
	ErrMemoryKindInvalid = &errno.Errno{HTTP: 400, Code: "MemoryError.KindInvalid", Message: "memory kind 不在合法枚举内"}

	// ErrMemoryKeyTooLong is returned when the Notepad key exceeds 100 characters.
	ErrMemoryKeyTooLong = &errno.Errno{HTTP: 400, Code: "MemoryError.KeyTooLong", Message: "memory key 超过 100 字符上限"}

	// ErrMemoryValueTooLong is returned when the Notepad value exceeds 1024 characters.
	ErrMemoryValueTooLong = &errno.Errno{HTTP: 400, Code: "MemoryError.ValueTooLong", Message: "memory value 超过 1024 字符上限"}

	// ErrMemoryUserRequired is returned when userID == 0 (unauthenticated context).
	ErrMemoryUserRequired = &errno.Errno{HTTP: 400, Code: "MemoryError.UserRequired", Message: "userID 必填"}

	// ErrMemoryNotFound is returned when a requested memory entry does not exist.
	ErrMemoryNotFound = &errno.Errno{HTTP: 404, Code: "MemoryError.NotFound", Message: "memory 条目不存在"}
)
