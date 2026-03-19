package langfuse

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"numind-server/internal/pkg/log"
)

// promptCache 5 分钟内存缓存
var promptCache sync.Map

type cachedPrompt struct {
	prompt    *PromptResponse
	expiresAt time.Time
}

const promptCacheTTL = 5 * time.Minute

// FetchPrompt 获取 Langfuse 管理的 prompt，带缓存 + 硬编码 fallback
func FetchPrompt(name, fallback string) (string, int) {
	if C == nil || !C.enabled {
		return fallback, 0
	}

	// 检查缓存
	if cached, ok := promptCache.Load(name); ok {
		cp := cached.(*cachedPrompt)
		if time.Now().Before(cp.expiresAt) {
			return cp.prompt.Prompt, cp.prompt.Version
		}
		promptCache.Delete(name)
	}

	// 从 Langfuse 获取
	url := fmt.Sprintf("%s/api/public/v2/prompts/%s?label=production", C.baseURL, name)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		log.Warnw("langfuse: failed to create prompt request", "name", name, "error", err)
		return fallback, 0
	}
	req.SetBasicAuth(C.publicKey, C.secretKey)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Warnw("langfuse: prompt fetch failed, using fallback", "name", name, "error", err)
		return fallback, 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		log.Warnw("langfuse: prompt fetch returned error, using fallback", "name", name, "status", resp.StatusCode)
		return fallback, 0
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnw("langfuse: failed to read prompt response", "name", name, "error", err)
		return fallback, 0
	}

	var pr PromptResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		log.Warnw("langfuse: failed to parse prompt response", "name", name, "error", err)
		return fallback, 0
	}

	// 缓存
	promptCache.Store(name, &cachedPrompt{
		prompt:    &pr,
		expiresAt: time.Now().Add(promptCacheTTL),
	})

	return pr.Prompt, pr.Version
}

// Compile 编译 prompt 模板，替换 {{variable}} 占位符
func Compile(template string, vars map[string]string) string {
	result := template
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}
