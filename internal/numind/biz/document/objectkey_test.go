package document

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeriveObjectKey(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{"带签名 query 剥离", "https://b.cos.ap-chengdu.myqcloud.com/agent-outputs/7/1-a.docx?sign=xyz&t=1", "agent-outputs/7/1-a.docx", false},
		{"无 query", "https://b.cos.r.myqcloud.com/agent-outputs/7/2-b.md", "agent-outputs/7/2-b.md", false},
		{"双斜杠规范化", "https://b.cos.r.myqcloud.com//agent-outputs/7/3-c.md", "agent-outputs/7/3-c.md", false},
		{"空 path", "https://b.cos.r.myqcloud.com/", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := deriveObjectKey(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsOwnedAgentOutputKey_IDOR(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		userID uint
		want   bool
	}{
		{"自己的产物", "agent-outputs/7/1-a.docx", 7, true},
		{"别人的产物(IDOR 拦截)", "agent-outputs/8/1-a.docx", 7, false},
		{"userID 前缀粘连不误判", "agent-outputs/77/1-a.docx", 7, false},
		{"非 agent-outputs 来源", "agent-attachments/7/1-a.docx", 7, false},
		{"任意 COS 对象", "avatars/7/x.png", 7, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isOwnedAgentOutputKey(tt.key, tt.userID))
		})
	}
}

func TestIsEditableMime(t *testing.T) {
	docxMime := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	tests := []struct {
		name string
		mime string
		file string
		want bool
	}{
		{"docx by mime", docxMime, "a.docx", true},
		{"md by ext (mime 空)", "", "a.md", true},
		{"txt mime 带 charset", "text/plain; charset=utf-8", "a.txt", true},
		{"html", "text/html", "a.html", true},
		{"png 不可编", "image/png", "chart.png", false},
		{"xlsx 不可编", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "a.xlsx", false},
		{"csv 不可编", "text/csv", "a.csv", false},
		{"pptx 不可编", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "a.pptx", false},
		{"pdf 不可编", "application/pdf", "a.pdf", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsEditableMime(tt.mime, tt.file))
		})
	}
}

func TestTitleFromFilename(t *testing.T) {
	assert.Equal(t, "report", titleFromFilename("report.docx"))
	assert.Equal(t, "a.b", titleFromFilename("a.b.md"))
	assert.Equal(t, "季度报告", titleFromFilename("季度报告.docx"))
	assert.Equal(t, "未命名文档", titleFromFilename(""))
	assert.Equal(t, "noext", titleFromFilename("noext"))
}
