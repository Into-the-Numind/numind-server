package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/langfuse"
)

func capturePipelineLangfuseEvents(t *testing.T) *[]*langfuse.IngestionEvent {
	t.Helper()
	previous := langfuse.C
	client := langfuse.NewTestClient()
	langfuse.C = client
	events := make([]*langfuse.IngestionEvent, 0, 4)
	client.InstallEventHook(func(event *langfuse.IngestionEvent) { events = append(events, event) })
	t.Cleanup(func() { langfuse.C = previous })
	return &events
}

func findPipelineSpanEvent(t *testing.T, events []*langfuse.IngestionEvent, eventType, name string) *langfuse.SpanBody {
	t.Helper()
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		body, ok := event.Body.(*langfuse.SpanBody)
		if !ok {
			continue
		}
		if eventType == "span-create" && body.Name != name {
			continue
		}
		return body
	}
	require.FailNow(t, "span event not found", "%s %s", eventType, name)
	return nil
}

func findPipelineSpanUpdate(t *testing.T, events []*langfuse.IngestionEvent, spanID string) *langfuse.SpanBody {
	t.Helper()
	for _, event := range events {
		body, ok := event.Body.(*langfuse.SpanBody)
		if event.Type == "span-update" && ok && body.ID == spanID {
			return body
		}
	}
	require.FailNow(t, "span update not found", "%s", spanID)
	return nil
}

func pipelineEventsJSON(t *testing.T, events []*langfuse.IngestionEvent) string {
	t.Helper()
	body, err := json.Marshal(events)
	require.NoError(t, err)
	return string(body)
}

func findPipelineTraceMetadata(t *testing.T, events []*langfuse.IngestionEvent) map[string]string {
	t.Helper()
	for _, event := range events {
		body, ok := event.Body.(*langfuse.TraceBody)
		if event.Type == "trace-create" && ok && body.Metadata["schema"] == pipelineReportSchema {
			return body.Metadata
		}
	}
	require.FailNow(t, "pipeline trace metadata not found")
	return nil
}
