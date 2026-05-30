package util

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/viper"
)

// TestGenerateSignedDownloadURL_SetsContentDispositionAttachment reproduces the
// customer-reported "下载提示不安全" bug: artifact presigned URLs were generated
// without response-content-disposition, so Tencent COS served files inline with
// no Content-Disposition header, triggering Chrome's cross-site-download safety
// warning ("无法验证此文件来源").
//
// The fix introduces GenerateSignedDownloadURL which appends
// response-content-disposition=attachment;filename*=UTF-8”<encoded> to the
// signed URL's query string. COS reflects this back as a real
// Content-Disposition response header, and the browser treats the response as
// a proper download — no warning.
//
// This test must remain in the repo as a regression guard (NDF rule 11).
func TestGenerateSignedDownloadURL_SetsContentDispositionAttachment(t *testing.T) {
	resetCOSSingleton(t)

	viper.Set("cos.enabled", true)
	viper.Set("cos.secret_id", "test-ak")
	viper.Set("cos.secret_key", "test-sk")
	viper.Set("cos.bucket", "fake-bucket-1234567890")
	viper.Set("cos.region", "ap-beijing")

	signed, err := GenerateSignedDownloadURL(
		context.Background(),
		"agent-output/1/2026-05-30/report.docx",
		"report.docx",
		3600,
	)
	if err != nil {
		t.Fatalf("GenerateSignedDownloadURL returned error: %v", err)
	}

	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed URL: %v (raw=%s)", err, signed)
	}

	rcd := u.Query().Get("response-content-disposition")
	if rcd == "" {
		t.Fatalf("signed URL missing response-content-disposition; raw=%s", signed)
	}
	if !strings.HasPrefix(rcd, "attachment") {
		t.Fatalf("response-content-disposition must start with 'attachment'; got %q", rcd)
	}
}

// TestGenerateSignedDownloadURL_EncodesUnicodeFilename ensures non-ASCII
// filenames (Chinese, spaces) use RFC 5987 filename*=UTF-8” form so the
// browser correctly preserves the original filename on save.
func TestGenerateSignedDownloadURL_EncodesUnicodeFilename(t *testing.T) {
	resetCOSSingleton(t)

	viper.Set("cos.enabled", true)
	viper.Set("cos.secret_id", "test-ak")
	viper.Set("cos.secret_key", "test-sk")
	viper.Set("cos.bucket", "fake-bucket-1234567890")
	viper.Set("cos.region", "ap-beijing")

	const filename = "销售分析报告 2026-05.xlsx"

	signed, err := GenerateSignedDownloadURL(
		context.Background(),
		"agent-output/1/2026-05-30/analytics.xlsx",
		filename,
		3600,
	)
	if err != nil {
		t.Fatalf("GenerateSignedDownloadURL: %v", err)
	}

	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed URL: %v (raw=%s)", err, signed)
	}

	rcd := u.Query().Get("response-content-disposition")
	if rcd == "" {
		t.Fatalf("signed URL missing response-content-disposition; raw=%s", signed)
	}
	if !strings.Contains(rcd, "filename*=UTF-8''") {
		t.Fatalf("expected RFC 5987 filename* form for non-ASCII name; got %q", rcd)
	}

	// The encoded filename must round-trip back to the original after percent-decoding.
	idx := strings.Index(rcd, "filename*=UTF-8''")
	if idx < 0 {
		t.Fatalf("missing filename* segment in %q", rcd)
	}
	encoded := rcd[idx+len("filename*=UTF-8''"):]
	if semi := strings.Index(encoded, ";"); semi >= 0 {
		encoded = encoded[:semi]
	}
	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		t.Fatalf("PathUnescape(%q): %v", encoded, err)
	}
	if decoded != filename {
		t.Fatalf("decoded filename mismatch: got %q want %q", decoded, filename)
	}
}

// TestGenerateSignedDownloadURL_ASCIIFilename locks in that pure-ASCII
// filenames still go through the filename* form (uniform serialization, no
// branching). Catches a regression where someone might add an early-return
// "fast path" that emits filename="..." only for ASCII.
func TestGenerateSignedDownloadURL_ASCIIFilename(t *testing.T) {
	resetCOSSingleton(t)

	viper.Set("cos.enabled", true)
	viper.Set("cos.secret_id", "test-ak")
	viper.Set("cos.secret_key", "test-sk")
	viper.Set("cos.bucket", "fake-bucket-1234567890")
	viper.Set("cos.region", "ap-beijing")

	signed, err := GenerateSignedDownloadURL(
		context.Background(),
		"agent-output/1/2026-05-30/report.docx",
		"report.docx",
		3600,
	)
	if err != nil {
		t.Fatalf("GenerateSignedDownloadURL: %v", err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rcd := u.Query().Get("response-content-disposition")
	if !strings.Contains(rcd, "filename*=UTF-8''report.docx") {
		t.Fatalf("expected filename*=UTF-8''report.docx in %q", rcd)
	}
}

// TestGenerateSignedDownloadURL_StripsCRLF locks in the defense-in-depth strip
// of CR/LF from caller-supplied filenames so a malicious caller cannot smuggle
// header-like bytes into the reflected Content-Disposition response header.
func TestGenerateSignedDownloadURL_StripsCRLF(t *testing.T) {
	resetCOSSingleton(t)

	viper.Set("cos.enabled", true)
	viper.Set("cos.secret_id", "test-ak")
	viper.Set("cos.secret_key", "test-sk")
	viper.Set("cos.bucket", "fake-bucket-1234567890")
	viper.Set("cos.region", "ap-beijing")

	signed, err := GenerateSignedDownloadURL(
		context.Background(),
		"agent-output/1/2026-05-30/evil.bin",
		"evil\r\nX-Injected: 1\r\n.bin",
		3600,
	)
	if err != nil {
		t.Fatalf("GenerateSignedDownloadURL: %v", err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rcd := u.Query().Get("response-content-disposition")
	if strings.ContainsAny(rcd, "\r\n") {
		t.Fatalf("response-content-disposition must not contain raw CR/LF; got %q", rcd)
	}
	// Encoded forms %0D / %0A must also be absent — they would decode to CRLF
	// once COS reflects the header back, leaving the filename in the response
	// header containing line-break bytes that some downstream parsers might
	// mishandle.
	if strings.Contains(rcd, "%0D") || strings.Contains(rcd, "%0A") ||
		strings.Contains(rcd, "%0d") || strings.Contains(rcd, "%0a") {
		t.Fatalf("response-content-disposition must not encode CR/LF; got %q", rcd)
	}
}

// resetCOSSingleton zeroes the lazily-initialized COS client so each test can
// re-run client construction with a fresh viper config. Safe to call from
// tests; the singleton is only used by this package.
func resetCOSSingleton(t *testing.T) {
	t.Helper()
	globalCOS = COSClient{once: sync.Once{}}
	t.Cleanup(func() {
		globalCOS = COSClient{once: sync.Once{}}
		viper.Set("cos.enabled", false)
		viper.Set("cos.secret_id", "")
		viper.Set("cos.secret_key", "")
		viper.Set("cos.bucket", "")
		viper.Set("cos.region", "")
	})
}
