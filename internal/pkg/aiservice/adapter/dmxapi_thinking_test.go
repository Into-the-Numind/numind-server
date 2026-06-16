package adapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
)

// mockThinkingRoute builds a ResolvedRoute with the DMXAPI-relevant thinking
// flags populated. All cases in the matrix below target the DMXAPIAdapter.
func mockThinkingRoute(serverURL, modelID string, supportsThinking, thinkingOnly bool, thinkingStyle string) *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		ProviderModelID:  modelID,
		SupportsThinking: supportsThinking,
		ThinkingOnly:     thinkingOnly,
		ThinkingStyle:    thinkingStyle,
		Provider: registry.ProviderInfo{
			BaseURL: serverURL,
			APIKey:  "test-key",
		},
	}
}

// TestDMXAPI_Thinking_Matrix exercises the eight decision-table rows from the
// plan's §3.2 "AiHubMix thinking gating" table. Each case spins up a tiny
// httptest server that captures the outbound POST body, and asserts both the
// wire-level oaiChatRequest fields AND the TraceMetadata surfaced back on the
// ChatResponse.
//
// Why fake server (not a mock HTTP client): the build path
// marshal → doPost → httpclient → upstream is exercised end-to-end, so the
// marshalling of newly-added fields (reasoning_effort, max_completion_tokens)
// is verified exactly as production code would send them.
func TestDMXAPI_Thinking_Matrix(t *testing.T) {
	type expectation struct {
		reasoningEffort     string
		maxTokens           int
		maxCompletionTokens int
		temperature         float64
		temperatureSent     bool // omitempty: 0.0 is omitted
		enableThinkingKwarg bool // chat_template_kwargs.enable_thinking==true expected on the wire
		// disableThinkingKwarg: chat_template_kwargs.enable_thinking==false expected on the
		// wire (explicit thinking-OFF for hybrid models that default thinking ON, e.g.
		// deepseek-v4-flash for session titles). Mutually exclusive with enableThinkingKwarg.
		disableThinkingKwarg bool
	}
	type metaExpect struct {
		reasoningEffort string
		modelFamily     string
		tempOverridden  bool
	}

	cases := []struct {
		name       string
		modelID    string
		supports   bool
		thinkOnly  bool
		thinkStyle string
		req        aiservice.ChatRequest
		expectBody expectation
		expectMeta metaExpect
	}{
		// case 1: Claude base + Thinking=true — inject medium, force temp=1.
		{
			name:      "claude_base_thinking_true_forces_temp1",
			modelID:   "claude-sonnet-4-6",
			supports:  true,
			thinkOnly: false,
			req: aiservice.ChatRequest{
				Messages:    sampleMessages(),
				Thinking:    true,
				Temperature: 0.5,
				MaxTokens:   500,
			},
			expectBody: expectation{
				reasoningEffort:     "medium",
				maxTokens:           500,
				maxCompletionTokens: 0,
				temperature:         1,
				temperatureSent:     true,
			},
			expectMeta: metaExpect{
				reasoningEffort: "medium",
				modelFamily:     string(ModelFamilyClaude),
				tempOverridden:  true,
			},
		},
		// case 2: Claude base + Thinking=false — no injection, preserve temp.
		{
			name:      "claude_base_thinking_false_preserves_temp",
			modelID:   "claude-sonnet-4-6",
			supports:  true,
			thinkOnly: false,
			req: aiservice.ChatRequest{
				Messages:    sampleMessages(),
				Thinking:    false,
				Temperature: 0.7,
				MaxTokens:   500,
			},
			expectBody: expectation{
				reasoningEffort:     "",
				maxTokens:           500,
				maxCompletionTokens: 0,
				temperature:         0.7,
				temperatureSent:     true,
			},
			expectMeta: metaExpect{
				reasoningEffort: "",
				modelFamily:     string(ModelFamilyClaude),
				tempOverridden:  false,
			},
		},
		// case 3: GPT 5.4 + Thinking=true — inject medium, max_completion_tokens.
		{
			name:      "gpt54_thinking_true_uses_max_completion",
			modelID:   "gpt-5.4",
			supports:  true,
			thinkOnly: false,
			req: aiservice.ChatRequest{
				Messages:  sampleMessages(),
				Thinking:  true,
				MaxTokens: 500,
			},
			expectBody: expectation{
				reasoningEffort:     "medium",
				maxTokens:           0,
				maxCompletionTokens: 500,
				temperature:         0,
				temperatureSent:     false,
			},
			expectMeta: metaExpect{
				reasoningEffort: "medium",
				modelFamily:     string(ModelFamilyOpenAIReasoning),
				tempOverridden:  false,
			},
		},
		// case 4: GPT 5.4 + Thinking=false — still max_completion_tokens (P1-1 regression protection).
		{
			name:      "gpt54_thinking_false_still_max_completion",
			modelID:   "gpt-5.4",
			supports:  true,
			thinkOnly: false,
			req: aiservice.ChatRequest{
				Messages:  sampleMessages(),
				Thinking:  false,
				MaxTokens: 500,
			},
			expectBody: expectation{
				reasoningEffort:     "",
				maxTokens:           0,
				maxCompletionTokens: 500,
				temperature:         0,
				temperatureSent:     false,
			},
			expectMeta: metaExpect{
				reasoningEffort: "",
				modelFamily:     string(ModelFamilyOpenAIReasoning),
				tempOverridden:  false,
			},
		},
		// case 5: Gemini intrinsic (ThinkingOnly=true) — no wire field, meta "intrinsic".
		{
			name:      "gemini_intrinsic_no_wire_field_meta_intrinsic",
			modelID:   "gemini-3.1-pro-preview",
			supports:  true,
			thinkOnly: true,
			req: aiservice.ChatRequest{
				Messages:  sampleMessages(),
				Thinking:  true,
				MaxTokens: 500,
			},
			expectBody: expectation{
				reasoningEffort:     "",
				maxTokens:           500,
				maxCompletionTokens: 0,
				temperature:         0,
				temperatureSent:     false,
			},
			expectMeta: metaExpect{
				reasoningEffort: "intrinsic",
				modelFamily:     string(ModelFamilyGemini),
				tempOverridden:  false,
			},
		},
		// case 6: qwen-turbo non-thinking (SupportsThinking=false) — no injection.
		{
			name:      "qwen_turbo_non_thinking_skip",
			modelID:   "qwen-turbo",
			supports:  false,
			thinkOnly: false,
			req: aiservice.ChatRequest{
				Messages:  sampleMessages(),
				Thinking:  true, // asked for but model does not support
				MaxTokens: 500,
			},
			expectBody: expectation{
				reasoningEffort:     "",
				maxTokens:           500,
				maxCompletionTokens: 0,
				temperature:         0,
				temperatureSent:     false,
			},
			expectMeta: metaExpect{
				reasoningEffort: "",
				modelFamily:     string(ModelFamilyGeneric),
				tempOverridden:  false,
			},
		},
		// case 7: Claude -think variant — no reasoning_effort, temp NOT overridden.
		// AiHubMix forces temp=1 server-side for -think slug so our adapter does not.
		{
			name:      "claude_think_suffix_no_temp_override",
			modelID:   "claude-sonnet-4-6-think",
			supports:  true,
			thinkOnly: true,
			req: aiservice.ChatRequest{
				Messages:    sampleMessages(),
				Thinking:    true,
				Temperature: 0.5,
				MaxTokens:   500,
			},
			expectBody: expectation{
				reasoningEffort:     "",
				maxTokens:           500,
				maxCompletionTokens: 0,
				temperature:         0.5,
				temperatureSent:     true,
			},
			expectMeta: metaExpect{
				reasoningEffort: "intrinsic",
				modelFamily:     string(ModelFamilyClaudeThinkingSlug),
				tempOverridden:  false,
			},
		},
		// case 8: DeepSeek base + Thinking=true — inject medium, preserve temp.
		{
			name:      "deepseek_thinking_true_preserves_temp",
			modelID:   "deepseek-v3.2",
			supports:  true,
			thinkOnly: false,
			req: aiservice.ChatRequest{
				Messages:    sampleMessages(),
				Thinking:    true,
				Temperature: 0.7,
				MaxTokens:   500,
			},
			expectBody: expectation{
				reasoningEffort:     "medium",
				maxTokens:           500,
				maxCompletionTokens: 0,
				temperature:         0.7,
				temperatureSent:     true,
			},
			expectMeta: metaExpect{
				reasoningEffort: "medium",
				modelFamily:     string(ModelFamilyDeepSeek),
				tempOverridden:  false,
			},
		},
		// case 9 (REPRO, bug-from-customer): agnes-2.0-flash activates thinking via
		// chat_template_kwargs.enable_thinking, NOT reasoning_effort. Before the
		// thinking_style switch lands, the adapter sends neither → agnes never thinks.
		// thinking_style="enable_thinking_kwarg" must emit chat_template_kwargs and
		// must NOT emit reasoning_effort.
		{
			name:       "agnes_enable_thinking_kwarg_emits_chat_template_kwargs",
			modelID:    "agnes-2.0-flash",
			supports:   true,
			thinkOnly:  false,
			thinkStyle: "enable_thinking_kwarg",
			req: aiservice.ChatRequest{
				Messages:  sampleMessages(),
				Thinking:  true,
				MaxTokens: 500,
			},
			expectBody: expectation{
				reasoningEffort:     "",
				maxTokens:           500,
				maxCompletionTokens: 0,
				temperature:         0,
				temperatureSent:     false,
				enableThinkingKwarg: true,
			},
			expectMeta: metaExpect{
				reasoningEffort: "enable_thinking_kwarg",
				modelFamily:     string(ModelFamilyGeneric),
				tempOverridden:  false,
			},
		},
		// case 10 (AC2): explicit thinking_style="reasoning_effort" → reasoning_effort, no kwarg.
		{
			name:       "explicit_reasoning_effort_style",
			modelID:    "some-oai-model",
			supports:   true,
			thinkOnly:  false,
			thinkStyle: "reasoning_effort",
			req: aiservice.ChatRequest{
				Messages:  sampleMessages(),
				Thinking:  true,
				MaxTokens: 500,
			},
			expectBody: expectation{
				reasoningEffort: "medium",
				maxTokens:       500,
			},
			expectMeta: metaExpect{
				reasoningEffort: "medium",
				modelFamily:     string(ModelFamilyGeneric),
				tempOverridden:  false,
			},
		},
		// case 11 (AC3): thinking_style="none" → neither reasoning_effort nor kwarg.
		{
			name:       "none_style_injects_nothing",
			modelID:    "some-oai-model",
			supports:   true,
			thinkOnly:  false,
			thinkStyle: "none",
			req: aiservice.ChatRequest{
				Messages:  sampleMessages(),
				Thinking:  true,
				MaxTokens: 500,
			},
			expectBody: expectation{
				reasoningEffort: "",
				maxTokens:       500,
			},
			expectMeta: metaExpect{
				reasoningEffort: "none",
				modelFamily:     string(ModelFamilyGeneric),
				tempOverridden:  false,
			},
		},
		// case 12 (title-nothinking): enable_thinking_kwarg style + Thinking=false on an
		// optional-thinking model now emits the DEACTIVATION field enable_thinking=false.
		// Hybrid models like deepseek-v4-flash default thinking ON at DMXAPI; the bare
		// request would think unless we explicitly opt out. (Was previously "no injection".)
		{
			name:       "kwarg_style_thinking_false_emits_disable",
			modelID:    "agnes-2.0-flash",
			supports:   true,
			thinkOnly:  false,
			thinkStyle: "enable_thinking_kwarg",
			req: aiservice.ChatRequest{
				Messages:  sampleMessages(),
				Thinking:  false,
				MaxTokens: 500,
			},
			expectBody: expectation{
				reasoningEffort:      "",
				maxTokens:            500,
				disableThinkingKwarg: true,
			},
			expectMeta: metaExpect{
				reasoningEffort: "enable_thinking_kwarg_off",
				modelFamily:     string(ModelFamilyGeneric),
				tempOverridden:  false,
			},
		},
		// case 14 (title-nothinking, real scenario): deepseek-v4-flash is the session.title
		// model. The title call never opts into thinking → must send enable_thinking=false
		// so the provider does NOT return a chain-of-thought (faster, cheaper, no 8s timeout).
		{
			name:       "deepseek_v4_flash_title_thinking_false_emits_disable",
			modelID:    "deepseek-v4-flash",
			supports:   true,
			thinkOnly:  false,
			thinkStyle: "enable_thinking_kwarg",
			req: aiservice.ChatRequest{
				Messages:  sampleMessages(),
				Thinking:  false,
				MaxTokens: 256,
			},
			expectBody: expectation{
				reasoningEffort:      "",
				maxTokens:            256,
				disableThinkingKwarg: true,
			},
			expectMeta: metaExpect{
				reasoningEffort: "enable_thinking_kwarg_off",
				modelFamily:     string(ModelFamilyDeepSeek),
				tempOverridden:  false,
			},
		},
		// case 15 (guard): thinking_only model + Thinking=false → NO disable kwarg.
		// Intrinsic-thinking models (e.g. deepseek-v4-pro for agent runs) always think;
		// the !ThinkingOnly gate must keep the deactivation branch from firing on them.
		{
			name:       "thinking_only_thinking_false_no_disable",
			modelID:    "deepseek-v4-pro",
			supports:   true,
			thinkOnly:  true,
			thinkStyle: "enable_thinking_kwarg",
			req: aiservice.ChatRequest{
				Messages:  sampleMessages(),
				Thinking:  false,
				MaxTokens: 500,
			},
			expectBody: expectation{
				reasoningEffort: "",
				maxTokens:       500,
			},
			expectMeta: metaExpect{
				reasoningEffort: "",
				modelFamily:     string(ModelFamilyDeepSeek),
				tempOverridden:  false,
			},
		},
		// case 13 (AC5 guard): enable_thinking_kwarg style but supports_thinking=false → no kwarg.
		{
			name:       "kwarg_style_supports_false_no_injection",
			modelID:    "agnes-2.0-flash",
			supports:   false,
			thinkOnly:  false,
			thinkStyle: "enable_thinking_kwarg",
			req: aiservice.ChatRequest{
				Messages:  sampleMessages(),
				Thinking:  true,
				MaxTokens: 500,
			},
			expectBody: expectation{
				reasoningEffort:     "",
				maxTokens:           500,
				enableThinkingKwarg: false,
			},
			expectMeta: metaExpect{
				reasoningEffort: "",
				modelFamily:     string(ModelFamilyGeneric),
				tempOverridden:  false,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var capturedBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				capturedBody = b
				writeChatJSON(w, "ok", tc.modelID, 1, 1)
			}))
			defer srv.Close()

			d := NewDMXAPIAdapter()
			route := mockThinkingRoute(srv.URL, tc.modelID, tc.supports, tc.thinkOnly, tc.thinkStyle)

			resp, err := d.Chat(context.Background(), route, tc.req)
			if err != nil {
				t.Fatalf("Chat: unexpected error: %v", err)
			}

			var sent oaiChatRequest
			if err := json.Unmarshal(capturedBody, &sent); err != nil {
				t.Fatalf("unmarshal captured body: %v (body=%s)", err, string(capturedBody))
			}

			// Wire assertions.
			if sent.ReasoningEffort != tc.expectBody.reasoningEffort {
				t.Errorf("reasoning_effort: got %q, want %q", sent.ReasoningEffort, tc.expectBody.reasoningEffort)
			}
			if sent.MaxTokens != tc.expectBody.maxTokens {
				t.Errorf("max_tokens: got %d, want %d", sent.MaxTokens, tc.expectBody.maxTokens)
			}
			if sent.MaxCompletionTokens != tc.expectBody.maxCompletionTokens {
				t.Errorf("max_completion_tokens: got %d, want %d", sent.MaxCompletionTokens, tc.expectBody.maxCompletionTokens)
			}

			// chat_template_kwargs.enable_thinking (Qwen/vLLM-style thinking activation/deactivation).
			if tc.expectBody.enableThinkingKwarg && tc.expectBody.disableThinkingKwarg {
				t.Fatalf("invalid test case: enableThinkingKwarg and disableThinkingKwarg are mutually exclusive")
			}
			switch {
			case tc.expectBody.enableThinkingKwarg:
				if sent.ChatTemplateKwargs == nil {
					t.Errorf("chat_template_kwargs: expected present with enable_thinking=true, got nil")
				} else if v, ok := sent.ChatTemplateKwargs["enable_thinking"].(bool); !ok || !v {
					t.Errorf("chat_template_kwargs.enable_thinking: got %v, want true", sent.ChatTemplateKwargs["enable_thinking"])
				}
			case tc.expectBody.disableThinkingKwarg:
				if sent.ChatTemplateKwargs == nil {
					t.Errorf("chat_template_kwargs: expected present with enable_thinking=false, got nil")
				} else if v, ok := sent.ChatTemplateKwargs["enable_thinking"].(bool); !ok || v {
					t.Errorf("chat_template_kwargs.enable_thinking: got %v, want false", sent.ChatTemplateKwargs["enable_thinking"])
				}
			default:
				if sent.ChatTemplateKwargs != nil {
					t.Errorf("chat_template_kwargs: expected absent (omitempty), got %v", sent.ChatTemplateKwargs)
				}
			}
			if tc.expectBody.temperatureSent {
				if sent.Temperature != tc.expectBody.temperature {
					t.Errorf("temperature: got %v, want %v", sent.Temperature, tc.expectBody.temperature)
				}
			} else {
				// omitempty: 0.0 when temperature was not set.
				if sent.Temperature != 0 {
					t.Errorf("temperature: expected 0 (omitempty), got %v", sent.Temperature)
				}
			}

			// Non-stream adapter path always sets stream=false.
			if sent.Stream {
				t.Errorf("stream: expected false for non-streaming Chat, got true")
			}

			// TraceMetadata assertions.
			if resp.TraceMetadata == nil {
				t.Fatal("resp.TraceMetadata is nil; adapter must always populate it")
			}
			if resp.TraceMetadata.ResolvedReasoningEffort != tc.expectMeta.reasoningEffort {
				t.Errorf("TraceMetadata.ResolvedReasoningEffort: got %q, want %q",
					resp.TraceMetadata.ResolvedReasoningEffort, tc.expectMeta.reasoningEffort)
			}
			if resp.TraceMetadata.ResolvedModelFamily != tc.expectMeta.modelFamily {
				t.Errorf("TraceMetadata.ResolvedModelFamily: got %q, want %q",
					resp.TraceMetadata.ResolvedModelFamily, tc.expectMeta.modelFamily)
			}
			if resp.TraceMetadata.TempOverridden != tc.expectMeta.tempOverridden {
				t.Errorf("TraceMetadata.TempOverridden: got %v, want %v",
					resp.TraceMetadata.TempOverridden, tc.expectMeta.tempOverridden)
			}
		})
	}
}
