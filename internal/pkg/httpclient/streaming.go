package httpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"numind-server/internal/pkg/log"
)

// StreamProcessor 流式处理器
type StreamProcessor struct {
	client *Client
}

// NewStreamProcessor 创建流式处理器
func NewStreamProcessor(client *Client) *StreamProcessor {
	return &StreamProcessor{
		client: client,
	}
}

// SSEProcessor SSE (Server-Sent Events) 处理器
type SSEProcessor struct {
	*StreamProcessor
}

// NewSSEProcessor 创建SSE处理器
func NewSSEProcessor(client *Client) *SSEProcessor {
	return &SSEProcessor{
		StreamProcessor: NewStreamProcessor(client),
	}
}

// SSEEvent SSE事件结构
type SSEEvent struct {
	Event string          `json:"event,omitempty"`
	ID    string          `json:"id,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	Retry int             `json:"retry,omitempty"`
}

// ProcessSSE 处理SSE流
func (sse *SSEProcessor) ProcessSSE(req *Request, eventHandler func(*SSEEvent) error) error {
	resp, err := sse.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	var currentEvent SSEEvent
	var currentData strings.Builder

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" {
			if currentData.Len() > 0 {
				currentEvent.Data = json.RawMessage(currentData.String())
				if err := eventHandler(&currentEvent); err != nil {
					log.Warnw("Error handling SSE event", "error", err)
					continue
				}
			}
			currentEvent = SSEEvent{}
			currentData.Reset()
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "id:") {
			currentEvent.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		} else if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				return nil
			}
			currentData.WriteString(data)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	return nil
}

// JSONStreamProcessor JSON流处理器
type JSONStreamProcessor struct {
	*StreamProcessor
}

// NewJSONStreamProcessor 创建JSON流处理器
func NewJSONStreamProcessor(client *Client) *JSONStreamProcessor {
	return &JSONStreamProcessor{
		StreamProcessor: NewStreamProcessor(client),
	}
}

// ProcessJSONStream 处理JSON流
func (jsp *JSONStreamProcessor) ProcessJSONStream(req *Request, jsonHandler func(json.RawMessage) error) error {
	resp, err := jsp.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return nil
			}

			var jsonData json.RawMessage
			if err := json.Unmarshal([]byte(data), &jsonData); err != nil {
				log.Warnw("Failed to parse JSON data", "error", err, "data", data)
				continue
			}

			if err := jsonHandler(jsonData); err != nil {
				log.Warnw("Error handling JSON data", "error", err)
				continue
			}
		} else {
			var jsonData json.RawMessage
			if err := json.Unmarshal([]byte(line), &jsonData); err != nil {
				log.Warnw("Failed to parse JSON line", "error", err, "line", line)
				continue
			}

			if err := jsonHandler(jsonData); err != nil {
				log.Warnw("Error handling JSON line", "error", err)
				continue
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	return nil
}

// StreamWithTimeout 带超时的流式处理
func (sp *StreamProcessor) StreamWithTimeout(ctx context.Context, timeout time.Duration, processor func() error) error {
	ctxWithTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- processor()
	}()

	select {
	case err := <-done:
		return err
	case <-ctxWithTimeout.Done():
		return fmt.Errorf("stream processing timeout after %v", timeout)
	}
}
