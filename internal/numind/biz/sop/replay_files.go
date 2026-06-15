package sop

import (
	"context"
	"strings"

	"numind-server/internal/pkg/model"
)

// replayFileURLExpirySeconds 回看文件签名 URL 有效期（2h）。
// 远长于单次回看浏览会话，又不至于让链接长期可分享。
const replayFileURLExpirySeconds = 7200

// CompletedNodeFileInfo 回看态单个上传文件（biz 层）。URL 已在装配时实时签名。
type CompletedNodeFileInfo struct {
	ID       uint   `json:"id"`
	FileName string `json:"file_name"`
	FileURL  string `json:"file_url"`
	FileType string `json:"file_type"`
	FileSize int64  `json:"file_size"`
	FileExt  string `json:"file_ext,omitempty"`
	Content  string `json:"content,omitempty"`
}

// replayImageExts 用于在 MIME 不可靠时回退判断是否图片（inline 渲染 vs 下载）。
var replayImageExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
	".bmp":  true,
	".svg":  true,
}

// isImageExt 报告扩展名是否为内联图片类型。容忍带或不带前导点、大小写、空白。
func isImageExt(ext string) bool {
	e := strings.ToLower(strings.TrimSpace(ext))
	if e == "" {
		return false
	}
	if !strings.HasPrefix(e, ".") {
		e = "." + e
	}
	return replayImageExts[e]
}

// attachNodeFiles 把 run 的上传文件按节点分组、签名后回填到对应 CompletedNodeInfo.Files。
//
// 私有桶裸链（sop_file.file_url）匿名 GET 会 403，必须读取时实时签名：图片用 signImage
// 生成 inline GET 签名（可 <img> 渲染），非图片用 signDownload 生成 attachment 下载签名
// （避免浏览器跨站下载告警）。签名失败 / objectKey 为空 / signer 返回空 → 回退原 base url（不阻断）。
//
// 签名函数通过参数注入，便于单测不依赖真实 COS。原地修改 nodes。
func attachNodeFiles(
	ctx context.Context,
	nodes []CompletedNodeInfo,
	files []model.SopFile,
	signImage func(ctx context.Context, objectKey string) (string, error),
	signDownload func(ctx context.Context, objectKey, fileName string) (string, error),
) {
	if len(nodes) == 0 || len(files) == 0 {
		return
	}

	byNode := make(map[uint][]model.SopFile, len(files))
	for _, f := range files {
		if f.NodeID == nil {
			continue
		}
		byNode[*f.NodeID] = append(byNode[*f.NodeID], f)
	}

	for i := range nodes {
		nodeFiles := byNode[nodes[i].NodeID]
		if len(nodeFiles) == 0 {
			continue
		}
		infos := make([]CompletedNodeFileInfo, 0, len(nodeFiles))
		for _, f := range nodeFiles {
			url := f.FileURL // 回退：持久化的 base 链接
			if f.ObjectKey != "" {
				var (
					signed string
					err    error
				)
				if strings.HasPrefix(f.FileType, "image/") || isImageExt(f.FileExt) {
					signed, err = signImage(ctx, f.ObjectKey)
				} else {
					signed, err = signDownload(ctx, f.ObjectKey, f.FileName)
				}
				if err == nil && signed != "" {
					url = signed
				}
			}
			infos = append(infos, CompletedNodeFileInfo{
				ID:       f.ID,
				FileName: f.FileName,
				FileURL:  url,
				FileType: f.FileType,
				FileSize: f.FileSize,
				FileExt:  f.FileExt,
				Content:  f.Content,
			})
		}
		nodes[i].Files = infos
	}
}
