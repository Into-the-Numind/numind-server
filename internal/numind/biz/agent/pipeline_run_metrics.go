package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
)

const (
	pipelineReportSchema = "numind-pipeline-report/v1"
	pipelineAgent1Name   = "爆款素材加工打标"
	pipelineAgent2Name   = "客户核心信息与人群画像提炼"
	pipelineAgent3Name   = "选题规划"
)

var pipelineReportPattern = regexp.MustCompile(`<!-- numind-pipeline-report/v1 agent=(agent-[123]) (\{[^\r\n]*\}) -->`)

type pipelineAgent1Report struct {
	Processed *int `json:"processed"`
	Skipped   *int `json:"skipped"`
	Remaining *int `json:"remaining"`
	Failed    *int `json:"failed"`
}

type pipelineAgent23Report struct {
	SourceCount *int   `json:"source_count"`
	OutputMode  string `json:"output_mode"`
}

func pipelineAgentKey(agentName string) string {
	switch agentName {
	case pipelineAgent1Name:
		return "agent-1"
	case pipelineAgent2Name:
		return "agent-2"
	case pipelineAgent3Name:
		return "agent-3"
	default:
		return ""
	}
}

func unavailablePipelineRunMetrics(agentKey string) map[string]string {
	return map[string]string{
		"schema": pipelineReportSchema,
		"agent":  agentKey,
		"status": "unavailable",
	}
}

// parsePipelineRunMetrics accepts exactly one controlled marker and emits only
// the schema-specific scalar whitelist. It never returns the marker, final body,
// customer identity, filenames, links, or malformed values.
func parsePipelineRunMetrics(agentName, finalText string) map[string]string {
	agentKey := pipelineAgentKey(agentName)
	if agentKey == "" {
		return nil
	}
	unavailable := unavailablePipelineRunMetrics(agentKey)
	if strings.Count(finalText, "<!-- "+pipelineReportSchema) != 1 {
		return unavailable
	}
	match := pipelineReportPattern.FindStringSubmatch(finalText)
	if len(match) != 3 || match[1] != agentKey {
		return unavailable
	}

	decoder := json.NewDecoder(bytes.NewBufferString(match[2]))
	decoder.DisallowUnknownFields()
	base := map[string]string{
		"schema": pipelineReportSchema,
		"agent":  agentKey,
		"status": "ok",
	}
	if agentKey == "agent-1" {
		var report pipelineAgent1Report
		if err := decoder.Decode(&report); err != nil || ensureJSONEOF(decoder) != nil ||
			report.Processed == nil || report.Skipped == nil || report.Remaining == nil || report.Failed == nil ||
			*report.Processed < 0 || *report.Skipped < 0 || *report.Remaining < 0 || *report.Failed < 0 {
			return unavailable
		}
		base["processed"] = strconv.Itoa(*report.Processed)
		base["skipped"] = strconv.Itoa(*report.Skipped)
		base["remaining"] = strconv.Itoa(*report.Remaining)
		base["failed"] = strconv.Itoa(*report.Failed)
		return base
	}

	var report pipelineAgent23Report
	if err := decoder.Decode(&report); err != nil || ensureJSONEOF(decoder) != nil ||
		report.SourceCount == nil || *report.SourceCount < 0 || !validPipelineOutputMode(agentKey, report.OutputMode) {
		return unavailable
	}
	base["source_count"] = strconv.Itoa(*report.SourceCount)
	base["output_mode"] = report.OutputMode
	return base
}

func validPipelineOutputMode(agentKey, mode string) bool {
	switch agentKey {
	case "agent-2":
		return mode == "create" || mode == "update" || mode == "unavailable"
	case "agent-3":
		return mode == "create" || mode == "append" || mode == "replace-round" || mode == "unavailable"
	default:
		return false
	}
}

// recordPipelineRunMetrics feeds the same sanitized map to structured logs and
// Langfuse. Observability being disabled never changes parsing or run semantics.
func recordPipelineRunMetrics(ctx context.Context, agentName, finalText string) map[string]string {
	metadata := parsePipelineRunMetrics(agentName, finalText)
	if metadata == nil {
		return nil
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	logArgs := make([]any, 0, len(keys)*2)
	for _, key := range keys {
		logArgs = append(logArgs, key, metadata[key])
	}
	log.Infow("agent_pipeline_metrics", logArgs...)
	if trace := langfuse.FromContext(ctx); trace != nil {
		langfuse.UpdateTraceMetadata(trace.TraceID, metadata)
	}
	return metadata
}

func (r *agentRunner) recordPipelineRunMetricsForDefinition(ctx context.Context, agentDefinitionID uint64, finalText string) {
	if r == nil || r.skillStore == nil || agentDefinitionID == 0 {
		return
	}
	definition, err := r.skillStore.GetByIDIncludeInactive(ctx, agentDefinitionID)
	if err != nil || definition == nil {
		// This lookup is observability-only and must never change run semantics.
		return
	}
	recordPipelineRunMetrics(ctx, definition.Name, finalText)
}
