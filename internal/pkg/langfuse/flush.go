package langfuse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"numind-server/internal/pkg/log"
)

// ingestionResponse mirrors the per-event shape returned by Langfuse's
// /api/public/ingestion endpoint. On partial failure the batch returns
// HTTP 207 (or 200 with a non-empty errors array), so we must inspect the
// body — a naive StatusCode >= 400 check hides these rejections.
type ingestionResponse struct {
	Successes []struct {
		ID     string `json:"id"`
		Status int    `json:"status"`
	} `json:"successes"`
	Errors []struct {
		ID      string `json:"id"`
		Status  int    `json:"status"`
		Message string `json:"message"`
		Error   string `json:"error"`
	} `json:"errors"`
}

// flush 将一批事件 POST 到 Langfuse ingestion API
func (c *Client) flush(batch []*IngestionEvent) {
	if len(batch) == 0 {
		return
	}

	body := &IngestionBatch{Batch: batch}
	data, err := json.Marshal(body)
	if err != nil {
		log.Errorw("langfuse: failed to marshal batch", "error", err, "count", len(batch))
		return
	}

	url := fmt.Sprintf("%s/api/public/ingestion", c.baseURL)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		log.Errorw("langfuse: failed to create request", "error", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.publicKey, c.secretKey)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Warnw("langfuse: ingestion request failed", "error", err, "count", len(batch))
		return
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		log.Warnw("langfuse: ingestion returned error",
			"status", resp.StatusCode, "count", len(batch),
			"body", truncateForLog(respBytes, 500))
		return
	}

	var parsed ingestionResponse
	if unmarshalErr := json.Unmarshal(respBytes, &parsed); unmarshalErr == nil && len(parsed.Errors) > 0 {
		first := parsed.Errors[0]
		log.Warnw("langfuse: per-event ingestion errors",
			"error_count", len(parsed.Errors),
			"batch_size", len(batch),
			"first_event_id", first.ID,
			"first_status", first.Status,
			"first_message", first.Message,
			"first_error", truncateForLog([]byte(first.Error), 300))
	}
}

func truncateForLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…(truncated)"
}
