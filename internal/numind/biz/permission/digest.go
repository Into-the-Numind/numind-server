package permission

import (
	"crypto/sha256"
	"encoding/hex"
)

// Digest 返回 SHA-256 完整 64 hex 字符（对账匹配；防 PII 副作用）。
func Digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
