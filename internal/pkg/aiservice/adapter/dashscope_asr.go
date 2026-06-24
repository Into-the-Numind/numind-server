package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/util"
)

// Compile-time interface check.
var _ ASRAdapter = (*DashScopeASRAdapter)(nil)

// DashScopeASRAdapter 通过阿里云 DashScope「录音文件识别」(Paraformer) 做语音转文字。
// 流程：WAV bytes → 传 COS 拿临时公网 URL → 异步提交转写任务 → 轮询 → 拉结果 JSON。
// 与 FunASR 适配器实现同一 ASRProvider 接口，路由切换无需改业务代码（aiservice 唯一入口）。
type DashScopeASRAdapter struct {
	client *httpclient.Client
}

// NewDashScopeASRAdapter 创建基于共享 httpclient 连接池的 DashScope ASR 适配器。
func NewDashScopeASRAdapter() *DashScopeASRAdapter {
	cfg := httpclient.DefaultConfig()
	return &DashScopeASRAdapter{client: httpclient.NewClient(cfg)}
}

func (d *DashScopeASRAdapter) Name() string           { return "dashscope-asr" }
func (d *DashScopeASRAdapter) ProviderType() string   { return "dashscope" }
func (d *DashScopeASRAdapter) Capabilities() []string { return []string{"asr"} }

// ---- DashScope JSON 结构 ----

type dsSubmitResp struct {
	Output struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
	} `json:"output"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

type dsPollResp struct {
	Output struct {
		TaskStatus string `json:"task_status"`
		Results    []struct {
			TranscriptionURL string `json:"transcription_url"`
			SubtaskStatus    string `json:"subtask_status"`
		} `json:"results"`
	} `json:"output"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

type dsTranscription struct {
	Properties struct {
		OriginalDurationInMilliseconds int64 `json:"original_duration_in_milliseconds"`
	} `json:"properties"`
	Transcripts []struct {
		Text string `json:"text"`
	} `json:"transcripts"`
}

// ASR 实现 ASRProvider 接口。
func (d *DashScopeASRAdapter) ASR(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
	// 录音文件识别端点固定在 dashscope.aliyuncs.com（ali-dashscope provider 的 base_url 是
	// compatible-mode/v1，不适用于此 REST API），故此处不取 route base_url。
	baseURL := "https://dashscope.aliyuncs.com"
	model := route.ProviderModelID
	if model == "" {
		model = "paraformer-v2"
	}
	apiKey := route.Provider.APIKey
	if apiKey == "" {
		return nil, fmt.Errorf("dashscope.ASR: empty api key (registry ai_service.api_key 未配)")
	}

	// 1. 拿一个公网可访问的音频 URL：优先 req.AudioURL，否则把 bytes 传 COS 取签名 URL。
	audioURL := req.AudioURL
	if audioURL == "" {
		if len(req.AudioBytes) == 0 {
			return nil, fmt.Errorf("dashscope.ASR: 既无 AudioURL 也无 AudioBytes")
		}
		format := req.AudioFormat
		if format == "" {
			format = "wav"
		}
		objectKey := fmt.Sprintf("xhs-asr-tmp/%d.%s", time.Now().UnixNano(), format)
		if _, err := util.UploadBytesToCOS(ctx, objectKey, "audio/"+format, req.AudioBytes); err != nil {
			return nil, fmt.Errorf("dashscope.ASR: 上传音频到 COS: %w", err)
		}
		signed, err := util.GenerateSignedDownloadURL(ctx, objectKey, "", 3600)
		if err != nil {
			return nil, fmt.Errorf("dashscope.ASR: 生成音频签名 URL: %w", err)
		}
		audioURL = signed
	}

	// 2. 异步提交转写任务。
	taskID, err := d.submitTask(ctx, baseURL, apiKey, model, audioURL, req.Language)
	if err != nil {
		return nil, err
	}

	// 3. 轮询任务直到 SUCCEEDED / FAILED（受 ctx 截止时间约束）。
	transURL, err := d.pollTask(ctx, baseURL, apiKey, taskID)
	if err != nil {
		return nil, err
	}

	// 4. 拉结果 JSON，拼出全文 + 时长。
	text, durMs, err := d.fetchTranscription(ctx, transURL)
	if err != nil {
		return nil, err
	}

	durSec := float64(durMs) / 1000.0
	if durSec <= 0 {
		durSec = estimateWavSeconds(req.AudioBytes)
	}
	return &aiservice.ASRResponse{
		Text:            text,
		DurationSeconds: durSec,
		Provider:        d.Name(),
	}, nil
}

func (d *DashScopeASRAdapter) submitTask(ctx context.Context, baseURL, apiKey, model, audioURL, lang string) (string, error) {
	if lang == "" {
		lang = "zh"
	}
	body := map[string]any{
		"model": model,
		"input": map[string]any{"file_urls": []string{audioURL}},
		"parameters": map[string]any{
			"language_hints": []string{lang},
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := d.client.Do(&httpclient.Request{
		Method:  "POST",
		URL:     baseURL + "/api/v1/services/audio/asr/transcription",
		Body:    strings.NewReader(string(raw)),
		Context: ctx,
		Headers: map[string]string{
			"Authorization":     "Bearer " + apiKey,
			"Content-Type":      "application/json",
			"X-DashScope-Async": "enable",
		},
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 1},
	})
	if err != nil {
		return "", wrapHTTPClientErr("dashscope.ASR submit", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", wrapHTTPStatusErr("dashscope.ASR submit", resp.StatusCode, data)
	}
	var sr dsSubmitResp
	if err := json.Unmarshal(data, &sr); err != nil {
		return "", fmt.Errorf("dashscope.ASR submit: decode: %w", err)
	}
	if sr.Output.TaskID == "" {
		return "", fmt.Errorf("dashscope.ASR submit: 无 task_id (code=%s msg=%s)", sr.Code, sr.Message)
	}
	return sr.Output.TaskID, nil
}

func (d *DashScopeASRAdapter) pollTask(ctx context.Context, baseURL, apiKey, taskID string) (string, error) {
	url := baseURL + "/api/v1/tasks/" + taskID
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("dashscope.ASR poll: %w", ctx.Err())
		default:
		}
		resp, err := d.client.Do(&httpclient.Request{
			Method:      "GET",
			URL:         url,
			Context:     ctx,
			Headers:     map[string]string{"Authorization": "Bearer " + apiKey},
			RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 2},
		})
		if err != nil {
			return "", wrapHTTPClientErr("dashscope.ASR poll", err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", wrapHTTPStatusErr("dashscope.ASR poll", resp.StatusCode, data)
		}
		var pr dsPollResp
		if err := json.Unmarshal(data, &pr); err != nil {
			return "", fmt.Errorf("dashscope.ASR poll: decode: %w", err)
		}
		switch pr.Output.TaskStatus {
		case "SUCCEEDED":
			for _, r := range pr.Output.Results {
				if r.TranscriptionURL != "" {
					return r.TranscriptionURL, nil
				}
			}
			return "", fmt.Errorf("dashscope.ASR poll: SUCCEEDED 但无 transcription_url")
		case "FAILED", "CANCELED":
			return "", fmt.Errorf("dashscope.ASR poll: 任务 %s (code=%s msg=%s)", pr.Output.TaskStatus, pr.Code, pr.Message)
		default: // PENDING / RUNNING
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("dashscope.ASR poll: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

func (d *DashScopeASRAdapter) fetchTranscription(ctx context.Context, transURL string) (string, int64, error) {
	resp, err := d.client.Do(&httpclient.Request{
		Method:      "GET",
		URL:         transURL,
		Context:     ctx,
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 2},
	})
	if err != nil {
		return "", 0, wrapHTTPClientErr("dashscope.ASR fetch", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", 0, wrapHTTPStatusErr("dashscope.ASR fetch", resp.StatusCode, data)
	}
	var t dsTranscription
	if err := json.Unmarshal(data, &t); err != nil {
		return "", 0, fmt.Errorf("dashscope.ASR fetch: decode: %w", err)
	}
	parts := make([]string, 0, len(t.Transcripts))
	for _, tr := range t.Transcripts {
		if s := strings.TrimSpace(tr.Text); s != "" {
			parts = append(parts, s)
		}
	}
	text := strings.Join(parts, "\n")
	if text == "" {
		log.C(ctx).Warnw("dashscope.ASR: 转写结果为空", "url", transURL)
	}
	return text, t.Properties.OriginalDurationInMilliseconds, nil
}

// estimateWavSeconds 按 16kHz 单声道 16-bit 估算时长（DashScope 未返回时长时兜底）。
func estimateWavSeconds(wav []byte) float64 {
	const bytesPerSec = 16000 * 2 // 16kHz * 2 bytes
	n := len(wav)
	if n <= 44 {
		return 0
	}
	return float64(n-44) / float64(bytesPerSec)
}
