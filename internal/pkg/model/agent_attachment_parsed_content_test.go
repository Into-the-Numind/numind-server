package model

import (
	"reflect"
	"testing"
)

// Regression: uploaded attachments were parsed by the fallback worker but the
// canonical result had nowhere to live, so file_read downloaded and parsed the
// same source again for every page.
func TestAgentAttachmentHasCanonicalParsedContentCache(t *testing.T) {
	typ := reflect.TypeOf(AgentAttachment{})
	for _, field := range []string{
		"ParsedContent",
		"ParsedContentSHA256",
		"ParsedContentByteSize",
		"ParsedPageCount",
		"ParsedAt",
	} {
		if _, ok := typ.FieldByName(field); !ok {
			t.Errorf("AgentAttachment is missing canonical cache field %s", field)
		}
	}
}
