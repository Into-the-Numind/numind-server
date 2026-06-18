package agent

import "testing"

// TestFilenameFromURL_StripsNanoPrefix reproduces 问题二: attachment chips derived
// from the object-key URL show the upload's nanosecond ID prefix
// (agent-attachments/<userID>/<unixnano>-<filename>). The derived filename must
// strip the ≥13-digit nanosecond prefix, while leaving short numeric prefixes
// like "2024-" untouched.
func TestFilenameFromURL_StripsNanoPrefix(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "nanosecond prefix stripped",
			url:  "https://x.cos/agent-attachments/9/1781779536452527550-和皎皎的对话.docx?sig=a",
			want: "和皎皎的对话.docx",
		},
		{
			name: "short numeric prefix not stripped",
			url:  "https://x.cos/agent-attachments/9/2024-plan.docx",
			want: "2024-plan.docx",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filenameFromURL(tc.url)
			if got != tc.want {
				t.Fatalf("filenameFromURL(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}
