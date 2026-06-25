package xhs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// COS 未启用（本地/测试默认）时，镜像与重签都应安全降级：原样返回，不报错、不阻塞。
func TestMedia_CosDisabledPassthrough(t *testing.T) {
	ctx := context.Background()
	imgs := []string{"https://sns-img.xhscdn.com/a.jpg", "https://sns-img.xhscdn.com/b.jpg"}
	out := mirrorImagesToCOS(ctx, 1, 100, imgs)
	assert.Equal(t, imgs, out, "COS 未启用 → 图片列表原样返回")

	// 非 COS URL 重签应原样返回。
	u := "https://sns-img.xhscdn.com/a.jpg"
	assert.Equal(t, u, resignCOSMediaURL(ctx, u), "非 COS URL 不动")
	assert.Equal(t, "", resignCOSMediaURL(ctx, ""), "空串不动")
}

func TestMedia_ResignNoteMediaLeavesNonCOS(t *testing.T) {
	item := &NoteItem{
		CoverURL: "https://sns-img.xhscdn.com/cover.jpg",
		VideoURL: "https://sns-video.xhscdn.com/v.mp4",
		Images:   []string{"https://sns-img.xhscdn.com/1.jpg"},
	}
	resignNoteMedia(context.Background(), item)
	assert.Equal(t, "https://sns-img.xhscdn.com/cover.jpg", item.CoverURL)
	assert.Equal(t, "https://sns-video.xhscdn.com/v.mp4", item.VideoURL)
	assert.Equal(t, "https://sns-img.xhscdn.com/1.jpg", item.Images[0])
}
