package aiservice

import (
	"sort"

	"github.com/gin-gonic/gin"
)

// HealthzHandler handles GET /healthz/ai.
//
// It reports the status of the AI Gateway: which adapters are registered,
// whether the singleton has been initialised, and a simple "ok" status.
//
// This endpoint is intentionally lightweight — no upstream provider pings are
// performed.  A future iteration (post Task-8) can add per-provider error-rate
// metrics from a rolling window counter.
//
// Response shape:
//
//	{
//	  "status":          "ok" | "degraded",
//	  "gateway_ready":   true | false,
//	  "adapters_loaded": ["ali","baidu-ocr","bailian-file","dmxapi","funasr","volc"],
//	  "adapter_count":   6
//	}
func HealthzHandler(c *gin.Context) {
	p := defaultGateway.Load() // atomic load via atomic.Pointer[Gateway]
	if p == nil {
		c.JSON(503, gin.H{
			"status":          "degraded",
			"gateway_ready":   false,
			"adapters_loaded": []string{},
			"adapter_count":   0,
			"message":         "Gateway not initialised — aiservice.SetDefault() has not been called",
		})
		return
	}

	names := p.AdapterNames()
	sort.Strings(names)

	c.JSON(200, gin.H{
		"status":          "ok",
		"gateway_ready":   true,
		"adapters_loaded": names,
		"adapter_count":   len(names),
	})
}
