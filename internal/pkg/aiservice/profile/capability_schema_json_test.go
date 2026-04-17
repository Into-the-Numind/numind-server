package profile

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCapabilitySchemaJSON locks the wire format of CapabilitySchema /
// CapabilityField to snake_case. Admin UI relies on these exact JSON keys;
// renaming a tag silently breaks the schema-driven form rendering.
func TestCapabilitySchemaJSON(t *testing.T) {
	s := CapabilitySchema{
		ServiceType: "llm",
		Fields: []CapabilityField{
			{
				Name:        "input_modalities",
				Type:        "modalities",
				Required:    true,
				EnumValues:  []string{"text", "image"},
				Description: "demo",
			},
			{
				Name:        "realtime",
				Type:        "bool",
				Required:    false,
				Description: "with omitempty EnumValues",
			},
		},
	}

	b, err := json.Marshal(s)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))

	assert.Equal(t, "llm", out["service_type"])
	fields, ok := out["fields"].([]any)
	require.True(t, ok)
	require.Len(t, fields, 2)

	f0 := fields[0].(map[string]any)
	assert.Equal(t, "input_modalities", f0["name"])
	assert.Equal(t, "modalities", f0["type"])
	assert.Equal(t, true, f0["required"])
	assert.Equal(t, []any{"text", "image"}, f0["enum_values"])
	assert.Equal(t, "demo", f0["description"])

	// Second field: EnumValues omitted when empty.
	f1 := fields[1].(map[string]any)
	_, hasEnum := f1["enum_values"]
	assert.False(t, hasEnum, "enum_values should be omitted when empty")
	assert.Equal(t, false, f1["required"], "required=false must serialize explicitly")
}
