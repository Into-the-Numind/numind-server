package errno

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 飞书集成错误码 (feishu-integration §5)。
func TestFeishuErrnos(t *testing.T) {
	t.Run("ErrLarkNotConnected", func(t *testing.T) {
		require.NotNil(t, ErrLarkNotConnected)
		assert.Equal(t, 400, ErrLarkNotConnected.HTTP)
		assert.Equal(t, "Lark.NotConnected", ErrLarkNotConnected.Code)
		assert.Equal(t, "尚未连接飞书，请先在设置中连接飞书账号", ErrLarkNotConnected.Message)
	})

	t.Run("ErrLarkReauthRequired", func(t *testing.T) {
		require.NotNil(t, ErrLarkReauthRequired)
		assert.Equal(t, 401, ErrLarkReauthRequired.HTTP)
		assert.Equal(t, "Lark.ReauthRequired", ErrLarkReauthRequired.Code)
		assert.Equal(t, "飞书授权已过期，请重新授权", ErrLarkReauthRequired.Message)
	})

	t.Run("ErrLarkCallFailed", func(t *testing.T) {
		require.NotNil(t, ErrLarkCallFailed)
		assert.Equal(t, 502, ErrLarkCallFailed.HTTP)
		assert.Equal(t, "Lark.CallFailed", ErrLarkCallFailed.Code)
		assert.Equal(t, "飞书接口调用失败", ErrLarkCallFailed.Message)
	})
}

// SetMessage 必须返回副本（不污染全局），并保留 HTTP/Code 用于 callback 附细节。
func TestFeishuErrno_SetMessageReturnsCopy(t *testing.T) {
	custom := ErrLarkCallFailed.SetMessage("create doc failed: %s", "boom")
	assert.Equal(t, "create doc failed: boom", custom.Message)
	assert.Equal(t, ErrLarkCallFailed.HTTP, custom.HTTP)
	assert.Equal(t, ErrLarkCallFailed.Code, custom.Code)
	// 全局变量未被篡改。
	assert.Equal(t, "飞书接口调用失败", ErrLarkCallFailed.Message)
}

// 工具层常用 fmt.Errorf("...: %w", ErrLarkReauthRequired) 包装 sentinel，
// 须能被 errors.Is 检出（client.For → 工具 returnSoftError 分类依赖此）。
func TestFeishuErrno_WrappedIsMatch(t *testing.T) {
	wrapped := fmt.Errorf("client.For: %w", ErrLarkReauthRequired)
	assert.True(t, errors.Is(wrapped, ErrLarkReauthRequired))
	assert.False(t, errors.Is(wrapped, ErrLarkNotConnected))

	// Decode 解 wrap 链拿回 HTTP/Code，掩盖在 *fmt.wrapError 里也能命中。
	httpCode, code, _ := Decode(wrapped)
	assert.Equal(t, 401, httpCode)
	assert.Equal(t, "Lark.ReauthRequired", code)
}
