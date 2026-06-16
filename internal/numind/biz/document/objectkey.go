// Package document 实现文档系统 v1：agent 生成产物的打开/解析/保存/导出（document-system feature）。
package document

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// editableExts 是 v1 支持在线编辑的文件扩展名（文本类）。
var editableExts = map[string]bool{
	".md": true, ".markdown": true, ".txt": true,
	".html": true, ".htm": true, ".docx": true,
}

// editableMimes 是 v1 支持在线编辑的 MIME（文本类）。
var editableMimes = map[string]bool{
	"text/markdown": true,
	"text/plain":    true,
	"text/html":     true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
}

// deriveObjectKey 从 COS 预签名 URL 解析出稳定的 object key（去 scheme/host/query）。
// 例：https://b.cos.r.myqcloud.com/agent-outputs/7/1-a.docx?sign=.. → agent-outputs/7/1-a.docx
func deriveObjectKey(sourceURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil {
		return "", fmt.Errorf("deriveObjectKey: parse url: %w", err)
	}
	key := strings.TrimLeft(u.Path, "/") // TrimLeft 而非 TrimPrefix：处理 //agent-outputs/.. 双斜杠
	key = path.Clean(key)
	if key == "" || key == "." {
		return "", fmt.Errorf("deriveObjectKey: empty object key")
	}
	return key, nil
}

// agentOutputsPrefix 返回某用户的 agent 产物 key 前缀：agent-outputs/{userID}/。
func agentOutputsPrefix(userID uint) string {
	return fmt.Sprintf("agent-outputs/%d/", userID)
}

// isOwnedAgentOutputKey 校验 key 必须严格属于调用者的 agent 产物目录（IDOR 防线）。
// COS key 格式 = agent-outputs/<userID>/<unixnano>-<filename>；仅校验 agent-outputs/ 前缀不足，
// 必须比对第二段 userID，否则用户 A 可传用户 B 的 key 打开 B 的文件。
func isOwnedAgentOutputKey(key string, userID uint) bool {
	return strings.HasPrefix(key, agentOutputsPrefix(userID))
}

// IsEditableMime 判定某文件是否支持 v1 在线编辑（mime 优先，扩展名兜底）。
func IsEditableMime(mime, filename string) bool {
	if mime != "" {
		// mime 可能带 "; charset=utf-8" 后缀，取分号前主体。
		base := strings.TrimSpace(strings.SplitN(mime, ";", 2)[0])
		if editableMimes[strings.ToLower(base)] {
			return true
		}
	}
	ext := strings.ToLower(path.Ext(filename))
	return editableExts[ext]
}

// titleFromFilename 取去扩展名的文件名作为文档标题；空则回退 "未命名文档"。
func titleFromFilename(filename string) string {
	base := path.Base(strings.TrimSpace(filename))
	if base == "" || base == "." || base == "/" {
		return "未命名文档"
	}
	if ext := path.Ext(base); ext != "" && ext != base { // ext==base 时是 .gitignore 类 dotfile，不剥
		base = strings.TrimSuffix(base, ext)
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return "未命名文档"
	}
	return base
}
