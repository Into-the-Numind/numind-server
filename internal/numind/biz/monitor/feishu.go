package monitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// feishuCardPayload 飞书卡片消息结构
type feishuCardPayload struct {
	MsgType string          `json:"msg_type"`
	Card    feishuCardInner `json:"card"`
}

type feishuCardInner struct {
	Header   feishuCardHeader    `json:"header"`
	Elements []feishuCardElement `json:"elements"`
}

type feishuCardHeader struct {
	Title feishuCardText `json:"title"`
}

type feishuCardText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type feishuCardElement struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

// SendWebhookMessage 发送飞书 webhook 消息（卡片格式）
// 如果 webhookURL 为空，静默返回 nil（优雅降级）。
// 发送失败只记录日志，不返回 error 以避免影响主流程。
func SendWebhookMessage(webhookURL, title, content string) error {
	if webhookURL == "" {
		return nil
	}

	payload := feishuCardPayload{
		MsgType: "interactive",
		Card: feishuCardInner{
			Header: feishuCardHeader{
				Title: feishuCardText{
					Tag:     "plain_text",
					Content: title,
				},
			},
			Elements: []feishuCardElement{
				{
					Tag:     "markdown",
					Content: content,
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		log.Errorw("SendWebhookMessage: marshal payload failed", "error", err)
		return nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Errorw("SendWebhookMessage: post failed", "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Errorw("SendWebhookMessage: non-200 response", "status", resp.StatusCode)
	}

	return nil
}

// SendBriefing 发送简报到飞书
func SendBriefing(webhookURL string, briefing *model.MonitorBriefing) {
	if webhookURL == "" || briefing == nil {
		return
	}

	title := fmt.Sprintf("竞品监控%s", briefing.Title)

	// 截取内容预览（飞书卡片内容长度有限）
	content := briefing.Content
	if len(content) > 2000 {
		content = content[:2000] + "\n\n... (内容已截断，请在工作台查看完整简报)"
	}

	_ = SendWebhookMessage(webhookURL, title, content)
}

// SendNewNotesNotification 发送新笔记更新通知到飞书
func SendNewNotesNotification(webhookURL string, bloggerName string, noteCount int) {
	if webhookURL == "" || noteCount == 0 {
		return
	}

	title := "竞品监控 - 新内容提醒"
	content := fmt.Sprintf("**%s** 发布了 **%d** 条新笔记，请前往工作台查看详情。", bloggerName, noteCount)

	_ = SendWebhookMessage(webhookURL, title, content)
}
