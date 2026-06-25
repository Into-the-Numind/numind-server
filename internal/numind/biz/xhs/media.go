package xhs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/util"
)

// 媒体镜像到 COS：小红书图片/视频直链会过期，采集时下载并存到我们 COS，
// 展示时从 COS 读（私有桶 → 读时重签 inline 签名 URL，与 agent cos_resign 同模式）。
// 跳转原帖/作者主页仍用小红书 URL（note_url/author_link，不镜像）。
const (
	cosMediaSignExpiry = int64(6 * 3600) // 读时 inline 签名有效期（6h；每次读都重签，等于长期有效）
	mediaDownloadCap   = int64(80) << 20 // 单个媒体下载上限 80MB
	mediaDownloadTO    = 60 * time.Second
)

// downloadMedia 下载小红书 CDN 媒体（带 Referer/UA 绕防盗链）。
func downloadMedia(ctx context.Context, mediaURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Referer", "https://www.xiaohongshu.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	resp, err := (&http.Client{Timeout: mediaDownloadTO}).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, "", fmt.Errorf("downloadMedia: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, mediaDownloadCap))
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// isCOSURL 判断一个 URL 是否指向我们的 COS 桶。
func isCOSURL(u string) bool {
	host := util.COSBucketHost()
	return host != "" && strings.Contains(u, host)
}

func imageExt(contentType, srcURL string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "gif"):
		return ".gif"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return ".jpg"
	}
	low := strings.ToLower(srcURL)
	for _, e := range []string{".png", ".webp", ".gif", ".jpeg", ".jpg"} {
		if strings.Contains(low, e) {
			if e == ".jpeg" {
				return ".jpg"
			}
			return e
		}
	}
	return ".jpg"
}

// mirrorImagesToCOS 把小红书图片下载并镜像到 COS，返回 COS base URL 列表。
// 单张失败保留原 URL（降级，不阻塞）；COS 未启用直接返回原列表。
func mirrorImagesToCOS(ctx context.Context, userID uint, noteID uint64, imgs []string) []string {
	if len(imgs) == 0 || !util.IsCOSEnabled() {
		return imgs
	}
	out := make([]string, len(imgs))
	for i, u := range imgs {
		out[i] = u
		if u == "" || isCOSURL(u) {
			continue
		}
		data, ct, err := downloadMedia(ctx, u)
		if err != nil || len(data) == 0 {
			log.C(ctx).Warnw("xhs mirror image failed, keep original", "note_id", noteID, "idx", i, "error", err)
			continue
		}
		if ct == "" {
			ct = "image/jpeg"
		}
		key := fmt.Sprintf("xhs-media/%d/%d/img_%d%s", userID, noteID, i, imageExt(ct, u))
		cosURL, upErr := util.UploadBytesToCOS(ctx, key, ct, data)
		if upErr != nil || cosURL == "" {
			log.C(ctx).Warnw("xhs mirror image upload failed, keep original", "note_id", noteID, "idx", i, "error", upErr)
			continue
		}
		out[i] = cosURL
	}
	return out
}

// mirrorVideoBytesToCOS 把已下载的视频字节上传 COS，返回 COS base URL；失败返回 ""（调用方保留原值）。
func mirrorVideoBytesToCOS(ctx context.Context, userID uint, noteID uint64, data []byte) string {
	if len(data) == 0 || !util.IsCOSEnabled() {
		return ""
	}
	key := fmt.Sprintf("xhs-media/%d/%d/video.mp4", userID, noteID)
	cosURL, err := util.UploadBytesToCOS(ctx, key, "video/mp4", data)
	if err != nil {
		log.C(ctx).Warnw("xhs mirror video upload failed", "note_id", noteID, "error", err)
		return ""
	}
	return cosURL
}

// resignCOSMediaURL 把 COS base URL 重签成新鲜 inline 签名 URL；非 COS URL 原样返回。
func resignCOSMediaURL(ctx context.Context, rawURL string) string {
	if rawURL == "" || !isCOSURL(rawURL) {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	key := strings.TrimPrefix(u.Path, "/")
	if dec, decErr := url.PathUnescape(key); decErr == nil {
		key = dec
	}
	signed, err := util.GenerateSignedURL(ctx, key, cosMediaSignExpiry)
	if err != nil || signed == "" {
		return rawURL
	}
	return signed
}

// resignNoteMedia 对一条笔记 DTO 的封面/图片/视频 COS URL 读时重签（inline 展示）。
// 非 COS URL（如尚未镜像的小红书直链）原样保留。
func resignNoteMedia(ctx context.Context, item *NoteItem) {
	if item == nil {
		return
	}
	item.CoverURL = resignCOSMediaURL(ctx, item.CoverURL)
	item.VideoURL = resignCOSMediaURL(ctx, item.VideoURL)
	for i := range item.Images {
		item.Images[i] = resignCOSMediaURL(ctx, item.Images[i])
	}
}
