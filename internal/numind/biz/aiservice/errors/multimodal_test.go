package errors_test

import (
	"errors"
	"testing"

	aierrors "numind-server/internal/numind/biz/aiservice/errors"
)

// mockStatusCodeErr is a test double that satisfies the statusCoder and
// bodyProvider interfaces, simulating a structured provider error.
type mockStatusCodeErr struct {
	code int
	body string
	msg  string
}

func (e *mockStatusCodeErr) Error() string   { return e.msg }
func (e *mockStatusCodeErr) StatusCode() int { return e.code }
func (e *mockStatusCodeErr) Body() string    { return e.body }

// wrappedErr wraps an error so we can test errors.As chain traversal.
type wrappedErr struct {
	inner error
}

func (e *wrappedErr) Error() string { return "wrapped: " + e.inner.Error() }
func (e *wrappedErr) Unwrap() error { return e.inner }

func TestIsMultimodalNotSupportedError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantYes bool
	}{
		// --- 8 pattern cases (one per provider / category) ---
		{
			name:    "pattern0_openai_invalid_value_image_url",
			err:     errors.New("Invalid value: 'image_url' is not supported for this model"),
			wantYes: true,
		},
		{
			name:    "pattern1_ali_dashscope_model_does_not_support_image",
			err:     errors.New("model does not support image input"),
			wantYes: true,
		},
		{
			name:    "pattern2_volc_ark_unsupported_modality_image",
			err:     errors.New("unsupported modality: image"),
			wantYes: true,
		},
		{
			name:    "pattern3_dmxapi_multimodal_not_supported",
			err:     errors.New("multimodal feature not supported"),
			wantYes: true,
		},
		{
			name:    "pattern4_generic_does_not_support_vision",
			err:     errors.New("this model does not support vision"),
			wantYes: true,
		},
		{
			name:    "pattern5_generic_image_input_not_supported",
			err:     errors.New("image input is not supported by this endpoint"),
			wantYes: true,
		},
		{
			name:    "pattern6_openai_422_image_url_not_allowed",
			err:     errors.New("image_url is not allowed for this model"),
			wantYes: true,
		},
		{
			name:    "pattern7_generic_vision_not_enabled",
			err:     errors.New("vision is not enabled for this model"),
			wantYes: true,
		},

		// --- Case-insensitive variants ---
		{
			name:    "case_insensitive_uppercase",
			err:     errors.New("INVALID VALUE: 'IMAGE_URL' IS NOT SUPPORTED"),
			wantYes: true,
		},
		{
			name:    "case_insensitive_mixed",
			err:     errors.New("Model Does Not Support Image Input"),
			wantYes: true,
		},

		// --- HTTP status code + body matching ---
		{
			name: "http_422_body_image_url_not_allowed",
			err: &mockStatusCodeErr{
				code: 422,
				body: "image_url is not allowed for glm-4",
				msg:  "provider error: 422",
			},
			wantYes: true,
		},
		{
			name: "http_400_body_multimodal_not_support",
			err: &mockStatusCodeErr{
				code: 400,
				body: "multimodal not support",
				msg:  "provider error: 400",
			},
			wantYes: true,
		},
		{
			name: "http_400_body_benign_still_matches_msg_pattern",
			err: &mockStatusCodeErr{
				code: 400,
				body: "missing field: model",
				msg:  "Invalid value: 'image_url' is not accepted",
			},
			wantYes: true,
		},
		{
			name: "http_422_body_benign_and_msg_benign",
			err: &mockStatusCodeErr{
				code: 422,
				body: "unknown field 'foo'",
				msg:  "provider error: 422 unprocessable",
			},
			wantYes: false,
		},
		{
			name: "http_500_not_checked_for_body",
			err: &mockStatusCodeErr{
				code: 500,
				body: "image_url is not allowed",
				msg:  "internal server error",
			},
			wantYes: false,
		},

		// --- Wrapped errors (errors.As chain) ---
		{
			name: "wrapped_provider_error_with_matching_status",
			err: &wrappedErr{
				inner: &mockStatusCodeErr{
					code: 400,
					body: "image_url not allowed",
					msg:  "provider error",
				},
			},
			wantYes: true,
		},

		// --- Negative cases ---
		{
			name:    "nil_error",
			err:     nil,
			wantYes: false,
		},
		{
			name:    "generic_500_internal_server_error",
			err:     errors.New("internal server error"),
			wantYes: false,
		},
		{
			name:    "timeout_deadline_exceeded",
			err:     errors.New("context deadline exceeded"),
			wantYes: false,
		},
		{
			name:    "rate_limit_exceeded",
			err:     errors.New("rate limit exceeded, please slow down"),
			wantYes: false,
		},
		{
			name:    "auth_error",
			err:     errors.New("401 unauthorized: invalid api key"),
			wantYes: false,
		},
		{
			name:    "network_error",
			err:     errors.New("connection refused"),
			wantYes: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := aierrors.IsMultimodalNotSupportedError(tc.err)
			if got != tc.wantYes {
				t.Errorf("IsMultimodalNotSupportedError(%v) = %v; want %v", tc.err, got, tc.wantYes)
			}
		})
	}
}
