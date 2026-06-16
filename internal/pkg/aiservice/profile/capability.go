package profile

import "fmt"

// Requirements represents the task-side capability requirements stored in
// task_profile.requirements JSON. Fields use pointer semantics via omitempty JSON tags;
// a zero value means "not required" (not checked).
type Requirements struct {
	// LLM fields
	// InputModalities lists the input modalities the task will use (e.g. ["text", "image"]).
	InputModalities []string `json:"input_modalities,omitempty"`
	// MinContext is the minimum context window (tokens) the task requires.
	MinContext int `json:"min_context,omitempty"`
	// Features lists feature-flag names that must be enabled on the service
	// (e.g. ["tool_use", "streaming", "json_mode"]). Note: image/vision is NOT a
	// feature — it is expressed via InputModalities containing "image"
	// (vision-capability-unify single source of truth).
	Features []string `json:"features,omitempty"`
	// Capability specifies the required capability class (e.g. "embedding", "rerank").
	Capability string `json:"capability,omitempty"`
	// Dimension is the required embedding vector dimension. Only checked when
	// Capability == "embedding". Matching is exact (not ≥), because the vector store
	// index dimension must match precisely.
	Dimension int `json:"dimension,omitempty"`

	// OCR fields
	// ImageFormats lists the image formats the task will submit (subset of service support).
	ImageFormats []string `json:"image_formats,omitempty"`
	// MaxResolution is the maximum image dimension (px) the task may submit.
	MaxResolution int `json:"max_resolution,omitempty"`
	// MaxFileSizeMB is the maximum file size (MB) the task may submit.
	MaxFileSizeMB int `json:"max_file_size_mb,omitempty"`

	// ASR fields
	// AudioFormats lists the audio formats the task will submit.
	AudioFormats []string `json:"audio_formats,omitempty"`
	// MaxDurationSec is the maximum audio duration (seconds) the task may submit.
	MaxDurationSec int `json:"max_duration_sec,omitempty"`
	// Languages lists the language codes the task requires the service to support.
	Languages []string `json:"languages,omitempty"`
}

// ServiceCapability represents the service-side capability document stored in
// ai_service.capability_json. Fields are populated according to the service_type.
type ServiceCapability struct {
	// ServiceType mirrors ai_service.service_type (llm | ocr | asr).
	// When non-empty, Match uses it to detect caller/service type mismatches.
	ServiceType string `json:"service_type,omitempty"`

	// LLM fields
	InputModalities  []string        `json:"input_modalities,omitempty"`
	OutputModalities []string        `json:"output_modalities,omitempty"`
	ContextWindow    int             `json:"context_window,omitempty"`
	MaxOutputTokens  int             `json:"max_output_tokens,omitempty"`
	Capabilities     []string        `json:"capabilities,omitempty"`
	Dimension        int             `json:"dimension,omitempty"`
	Features         map[string]bool `json:"features,omitempty"`

	// OCR fields
	ImageFormats  []string `json:"image_formats,omitempty"`
	MaxResolution int      `json:"max_resolution,omitempty"`
	MaxFileSizeMB int      `json:"max_file_size_mb,omitempty"`

	// ASR fields
	AudioFormats   []string `json:"audio_formats,omitempty"`
	MaxDurationSec int      `json:"max_duration_sec,omitempty"`
	Languages      []string `json:"languages,omitempty"`
	Realtime       bool     `json:"realtime,omitempty"`
}

// MatchResult is the output of a Capability Matching check.
type MatchResult struct {
	// Compatible is true when the service satisfies all task requirements.
	Compatible bool
	// Reasons lists human-readable explanations for each incompatibility.
	// Empty when Compatible is true.
	Reasons []string
}

// Match checks whether a service with the given capability satisfies the task's
// requirements for the specified serviceType.
//
// Semantics (spec §5.2):
//   - Capacity fields (context_window, max_resolution, max_duration_sec, dimension):
//     requirement ≤ service value (service must meet or exceed what the task needs).
//   - Boolean/enumeration fields (modalities, features, capabilities, formats, languages):
//     requirement ⊆ service set (service must support every item the task requests).
//   - Embedding dimension is an exact match (not ≥) because the vector store index
//     dimension must be identical.
func Match(serviceType string, req Requirements, svc ServiceCapability) MatchResult {
	var reasons []string

	// Guard: if the service document carries its own service_type, verify it matches
	// the caller's intent. A mismatch means the wrong service was selected (e.g. the
	// router handed an OCR service to an LLM task), and all further field checks would
	// be meaningless — return early with a single diagnostic reason.
	if svc.ServiceType != "" && svc.ServiceType != serviceType {
		return MatchResult{
			Compatible: false,
			Reasons: []string{fmt.Sprintf(
				"service_type 不一致（期望 %s，服务为 %s）", serviceType, svc.ServiceType,
			)},
		}
	}

	switch serviceType {
	case "llm":
		reasons = append(reasons, matchLLM(req, svc)...)
	case "ocr":
		reasons = append(reasons, matchOCR(req, svc)...)
	case "asr":
		reasons = append(reasons, matchASR(req, svc)...)
	default:
		reasons = append(reasons, fmt.Sprintf("未知 service_type: %s", serviceType))
	}

	return MatchResult{
		Compatible: len(reasons) == 0,
		Reasons:    reasons,
	}
}

// matchLLM performs LLM-specific compatibility checks.
func matchLLM(req Requirements, svc ServiceCapability) []string {
	var reasons []string

	// 1. Input modalities: task's required modalities must all be in service's supported list.
	svcInputSet := toSet(svc.InputModalities)
	for _, m := range req.InputModalities {
		if !svcInputSet[m] {
			reasons = append(reasons, "缺少输入模态: "+m)
		}
	}

	// 2. Context window: service must meet or exceed the minimum required.
	if req.MinContext > 0 && svc.ContextWindow < req.MinContext {
		reasons = append(reasons, fmt.Sprintf("上下文窗口不足（需要 ≥%d，服务提供 %d）", req.MinContext, svc.ContextWindow))
	}

	// 3. Features: every required feature must be enabled on the service.
	for _, f := range req.Features {
		if !svc.Features[f] {
			reasons = append(reasons, "缺少特性: "+f)
		}
	}

	// 4. Capability class: e.g. "embedding", "rerank".
	if req.Capability != "" {
		svcCapSet := toSet(svc.Capabilities)
		if !svcCapSet[req.Capability] {
			reasons = append(reasons, "不支持能力: "+req.Capability)
		}

		// 5. Embedding dimension: must be an exact match (vector index constraint).
		if req.Capability == "embedding" && req.Dimension > 0 {
			if svc.Dimension != req.Dimension {
				reasons = append(reasons, fmt.Sprintf(
					"embedding 维度不匹配（需要 %d，服务提供 %d）", req.Dimension, svc.Dimension,
				))
			}
		}
	}

	return reasons
}

// matchOCR performs OCR-specific compatibility checks.
func matchOCR(req Requirements, svc ServiceCapability) []string {
	var reasons []string

	// 1. Image formats: every format the task may submit must be supported.
	svcFmtSet := toSet(svc.ImageFormats)
	for _, imgFmt := range req.ImageFormats {
		if !svcFmtSet[imgFmt] {
			reasons = append(reasons, "不支持图像格式: "+imgFmt)
		}
	}

	// 2. Resolution: service must meet or exceed the task's maximum.
	if req.MaxResolution > 0 && svc.MaxResolution < req.MaxResolution {
		reasons = append(reasons, fmt.Sprintf(
			"服务分辨率上限不足（需要 ≥%d，服务上限 %d）", req.MaxResolution, svc.MaxResolution,
		))
	}

	// 3. File size: service must meet or exceed the task's maximum.
	if req.MaxFileSizeMB > 0 && svc.MaxFileSizeMB < req.MaxFileSizeMB {
		reasons = append(reasons, fmt.Sprintf(
			"服务文件大小上限不足（需要 ≥%d MB，服务为 %d MB）", req.MaxFileSizeMB, svc.MaxFileSizeMB,
		))
	}

	return reasons
}

// matchASR performs ASR-specific compatibility checks.
func matchASR(req Requirements, svc ServiceCapability) []string {
	var reasons []string

	// 1. Audio formats: every format the task may submit must be supported.
	svcFmtSet := toSet(svc.AudioFormats)
	for _, audioFmt := range req.AudioFormats {
		if !svcFmtSet[audioFmt] {
			reasons = append(reasons, "不支持音频格式: "+audioFmt)
		}
	}

	// 2. Duration: service must meet or exceed the task's maximum.
	if req.MaxDurationSec > 0 && svc.MaxDurationSec < req.MaxDurationSec {
		reasons = append(reasons, "服务音频时长上限不足")
	}

	// 3. Languages: every required language must be supported.
	svcLangSet := toSet(svc.Languages)
	for _, lang := range req.Languages {
		if !svcLangSet[lang] {
			reasons = append(reasons, "不支持语言: "+lang)
		}
	}

	return reasons
}

// toSet converts a string slice to a boolean membership map for O(1) lookup.
func toSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}
