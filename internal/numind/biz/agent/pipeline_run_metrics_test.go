package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/langfuse"
)

func TestPipelineRunMetrics_ParsesOnlyTheAgentSpecificWhitelist(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
		final     string
		want      map[string]string
	}{
		{
			name: "agent 1", agentName: pipelineAgent1Name,
			final: `完成\n<!-- numind-pipeline-report/v1 agent=agent-1 {"processed":12,"skipped":3,"remaining":4,"failed":1} -->`,
			want: map[string]string{
				"schema": pipelineReportSchema, "agent": "agent-1", "status": "ok",
				"processed": "12", "skipped": "3", "remaining": "4", "failed": "1",
			},
		},
		{
			name: "agent 2", agentName: pipelineAgent2Name,
			final: `完成\n<!-- numind-pipeline-report/v1 agent=agent-2 {"source_count":7,"output_mode":"update"} -->`,
			want: map[string]string{
				"schema": pipelineReportSchema, "agent": "agent-2", "status": "ok",
				"source_count": "7", "output_mode": "update",
			},
		},
		{
			name: "agent 3", agentName: pipelineAgent3Name,
			final: `完成\n<!-- numind-pipeline-report/v1 agent=agent-3 {"source_count":2,"output_mode":"replace-round"} -->`,
			want: map[string]string{
				"schema": pipelineReportSchema, "agent": "agent-3", "status": "ok",
				"source_count": "2", "output_mode": "replace-round",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, parsePipelineRunMetrics(tc.agentName, tc.final))
		})
	}
}

func TestPipelineRunMetrics_InvalidOrAmbiguousMarkerIsUnavailableAndNeverEchoed(t *testing.T) {
	secret := "customer-secret-body"
	tests := []struct {
		name      string
		agentName string
		final     string
	}{
		{"missing", pipelineAgent1Name, "ordinary final answer " + secret},
		{"negative", pipelineAgent1Name, `<!-- numind-pipeline-report/v1 agent=agent-1 {"processed":-1,"skipped":0,"remaining":0,"failed":0} --> ` + secret},
		{"unknown field", pipelineAgent1Name, `<!-- numind-pipeline-report/v1 agent=agent-1 {"processed":1,"skipped":0,"remaining":0,"failed":0,"title":"` + secret + `"} -->`},
		{"missing field", pipelineAgent1Name, `<!-- numind-pipeline-report/v1 agent=agent-1 {"processed":1,"skipped":0,"remaining":0} -->`},
		{"wrong agent", pipelineAgent2Name, `<!-- numind-pipeline-report/v1 agent=agent-3 {"source_count":1,"output_mode":"update"} -->`},
		{"bad agent 2 mode", pipelineAgent2Name, `<!-- numind-pipeline-report/v1 agent=agent-2 {"source_count":1,"output_mode":"append"} -->`},
		{"bad agent 3 mode", pipelineAgent3Name, `<!-- numind-pipeline-report/v1 agent=agent-3 {"source_count":1,"output_mode":"overwrite-everything"} -->`},
		{"duplicate marker", pipelineAgent1Name, `<!-- numind-pipeline-report/v1 agent=agent-1 {"processed":1,"skipped":0,"remaining":0,"failed":0} --><!-- numind-pipeline-report/v1 agent=agent-1 {"processed":1,"skipped":0,"remaining":0,"failed":0} -->`},
		{"malformed", pipelineAgent1Name, `<!-- numind-pipeline-report/v1 agent=agent-1 {` + secret + `} -->`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePipelineRunMetrics(tc.agentName, tc.final)
			assert.Equal(t, map[string]string{
				"schema": pipelineReportSchema,
				"agent":  pipelineAgentKey(tc.agentName),
				"status": "unavailable",
			}, got)
			encoded, err := json.Marshal(got)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), secret)
			assert.NotContains(t, string(encoded), "processed\":-1")
		})
	}
}

func TestPipelineRunMetrics_NonPipelineAgentProducesNoMetadata(t *testing.T) {
	assert.Nil(t, parsePipelineRunMetrics("普通 Agent", `<!-- numind-pipeline-report/v1 agent=agent-1 {"processed":1,"skipped":0,"remaining":0,"failed":0} -->`))
}

func TestPipelineRunMetrics_RecorderUsesTheSameSafeMapForLangfuse(t *testing.T) {
	previous := langfuse.C
	client := langfuse.NewTestClient()
	langfuse.C = client
	t.Cleanup(func() { langfuse.C = previous })
	var events []*langfuse.IngestionEvent
	client.InstallEventHook(func(event *langfuse.IngestionEvent) { events = append(events, event) })
	ctx := langfuse.WithTrace(context.Background(), "pipeline-trace")
	final := `sensitive prose must not be traced\n<!-- numind-pipeline-report/v1 agent=agent-1 {"processed":2,"skipped":1,"remaining":0,"failed":0} -->`

	got := recordPipelineRunMetrics(ctx, pipelineAgent1Name, final)

	require.Equal(t, "2", got["processed"])
	require.Len(t, events, 1)
	require.Equal(t, "trace-create", events[0].Type)
	body, ok := events[0].Body.(*langfuse.TraceBody)
	require.True(t, ok)
	assert.Equal(t, got, body.Metadata)
	encoded, err := json.Marshal(events[0])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "sensitive prose")
	assert.NotContains(t, string(encoded), "<!--")

	langfuse.C = nil
	withoutTrace := recordPipelineRunMetrics(context.Background(), pipelineAgent1Name, final)
	assert.Equal(t, got, withoutTrace, "Langfuse disabled must not change parser semantics")
	assert.False(t, strings.Contains(strings.Join(mapValues(withoutTrace), " "), "sensitive"))
}

func mapValues(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
