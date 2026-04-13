package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// 简单的独立可运行测试，验证阿里百炼 enable_thinking 流式返回。
// 直接使用 HTTP 调用兼容模式，无需额外依赖。
func main() {
	const apiKey = "sk-634923fe0bba4cb199c3f17eb9e7e749"

	baseURL := os.Getenv("DASHSCOPE_BASE_URL")
	if baseURL == "" {
		baseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	}

	// 默认用 qwen-max，更容易返回 reasoning_content；支持通过环境变量 LLM_MODEL 覆盖。
	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = "qwen-plus" // 推荐优先试 qwen-plus，亦可覆盖为 deepseek-r1
	}

	// 选一个推理型提示，便于模型输出 reasoning_content
	prompts := []string{
		"你是推理助手。必须先输出 reasoning_content（详细推理步骤，以文本逐条输出），然后输出最终答案 content。若未输出 reasoning_content，则视为未完成，需要补充。题目：一步步推导 3 位朋友平分 17 块蛋糕（可切分），如何分？",
		"你是推理助手。必须先输出 reasoning_content（详细推理步骤，以文本逐条输出），然后输出最终答案 content。若未输出 reasoning_content，则视为未完成，需要补充。题目：火车长 200 米，速度 20 米/秒，通过 300 米隧道需要多久？给出逐步推导。",
		"你是推理助手。必须先输出 reasoning_content（详细推理步骤，以文本逐条输出），然后输出最终答案 content。若未输出 reasoning_content，则视为未完成，需要补充。题目：给出 2025-12-26 是星期几的推算过程，用逐步演算。",
	}
	prompt := prompts[0]
	// 打开以观察原始 chunk，便于确认服务是否返回 reasoning_content。
	logRawChunks := true
	if v := os.Getenv("LOG_RAW_CHUNKS"); v != "" {
		lv := strings.ToLower(v)
		logRawChunks = lv == "1" || lv == "true" || lv == "yes" || lv == "y"
	}

	log.Printf("[INFO] using model=%s prompt=%s base_url=%s log_raw_chunks=%v", model, prompt, baseURL, logRawChunks)

	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": "你是一个严谨的推理助手，会先写出 reasoning_content，再写出最终答案。"},
			{"role": "user", "content": prompt},
		},
		"stream": true,
		"extra_body": map[string]interface{}{
			"enable_thinking": true,
		},
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}

	reqData, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", baseURL, bytes.NewBuffer(reqData))
	if err != nil {
		log.Fatalf("new request error: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "numind-sop-stream-test/1.0")

	client := &http.Client{
		Timeout: 120 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("http call error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("unexpected status: %d", resp.StatusCode)
	}

	fmt.Println()
	fmt.Println("==================== 思考过程 ====================")
	fmt.Println()

	var (
		thinkingBuf   strings.Builder
		answerBuf     strings.Builder
		isAnswering   bool
		hasThinking   bool
		lastRawChunks int
	)

	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		raw := strings.TrimPrefix(line, "data: ")
		if raw == "[DONE]" {
			break
		}

		if logRawChunks {
			lastRawChunks++
			log.Printf("[DEBUG] raw chunk #%d: %s", lastRawChunks, raw)
		}

		// data 行可能包含 usage，也可能包含 delta
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			log.Printf("[WARN] parse chunk error: %v, raw: %s", err, raw)
			continue
		}

		// 记录 usage（如果在流中返回）
		if usage, ok := chunk["usage"]; ok {
			usageJSON, _ := json.Marshal(usage)
			log.Printf("[INFO] usage: %s", string(usageJSON))
		}

		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]interface{})
		delta, _ := choice["delta"].(map[string]interface{})
		if delta == nil {
			continue
		}

		if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
			hasThinking = true
			thinkingBuf.WriteString(rc)
			log.Printf("[INFO] thinking chunk len=%d", len(rc))
			if !isAnswering {
				fmt.Print(rc)
			}
		}

		if content, ok := delta["content"].(string); ok && content != "" {
			if !isAnswering {
				fmt.Println()
				fmt.Println("==================== 完整回复 ====================")
				fmt.Println()
				isAnswering = true
			}
			answerBuf.WriteString(content)
			log.Printf("[INFO] message chunk len=%d", len(content))
			fmt.Print(content)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("stream read error: %v", err)
	}

	log.Printf("[INFO] stream finished, thinking_len=%d, answer_len=%d", thinkingBuf.Len(), answerBuf.Len())
	if !hasThinking {
		log.Printf("[WARN] no reasoning_content received. Try a harder prompt or model=%s", model)
	}
}
