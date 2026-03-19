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
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		log.Warnw("langfuse: ingestion returned error", "status", resp.StatusCode, "count", len(batch))
	}
}
