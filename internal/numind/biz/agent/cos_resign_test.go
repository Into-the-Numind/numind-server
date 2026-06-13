package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

const testCOSHost = "numind-dev-1334169463.cos.ap-chengdu.myqcloud.com"

// fakeSigner returns deterministic signed URLs so tests assert routing
// (image vs download) and the freshly-attached signature without a live COS.
func fakeSigner() cosSigner {
	return cosSigner{
		signImage: func(_ context.Context, key string, _ int64) (string, error) {
			return "https://" + testCOSHost + "/" + key + "?inline=1&q-signature=IMG", nil
		},
		signDownload: func(_ context.Context, key, filename string, _ int64) (string, error) {
			return "https://" + testCOSHost + "/" + key +
				"?response-content-disposition=attachment&dl=" + filename + "&q-signature=DL", nil
		},
	}
}

// TestResignCOSLinks_HealsTruncatedDownloadURL reproduces dev run 150
// (2026-06-13): the model transcribed the ~600-char signed DOCX URL into its
// final answer but truncated it, keeping the path + response-content-disposition
// yet dropping every q-sign-* param. The anonymous GET with a disposition header
// is rejected by COS ("InvalidRequest"). The read path must re-sign from the
// object key so the link works again.
//
// Permanent regression protection (NDF Rule 11). RED against the T0 stub.
func TestResignCOSLinks_HealsTruncatedDownloadURL(t *testing.T) {
	key := "agent-outputs/1/20260613-001014-py-report.docx"
	// The exact failure shape: path + disposition, NO signature.
	truncated := "https://" + testCOSHost + "/" + key + "?response-content-disposition=attachment%3B+filename"
	md := "报告已生成：[点击下载完整报告](" + truncated + ")"

	got := resignCOSLinksWithHost(context.Background(), md, testCOSHost, fakeSigner())

	if strings.Contains(got, "?response-content-disposition=attachment%3B+filename)") {
		t.Fatalf("truncated unsigned URL survived the read path: %s", got)
	}
	if !strings.Contains(got, "q-signature=DL") {
		t.Fatalf("download link must be re-signed with a fresh signature, got: %s", got)
	}
	if !strings.Contains(got, "/"+key+"?") {
		t.Fatalf("object key must be preserved across re-signing, got: %s", got)
	}
}

func TestResignCOSLinks_ImagesSignInline(t *testing.T) {
	key := "agent-outputs/1/20260613-000337-bar_chart.png"
	md := "![柱状图](https://" + testCOSHost + "/" + key + "?q-signature=OLD)"
	got := resignCOSLinksWithHost(context.Background(), md, testCOSHost, fakeSigner())
	if !strings.Contains(got, "q-signature=IMG") {
		t.Fatalf("image must use the inline signer, got: %s", got)
	}
	if strings.Contains(got, "attachment") {
		t.Fatalf("image must NOT get an attachment disposition (breaks <img>), got: %s", got)
	}
}

func TestResignCOSLinks_MultipleLinksAllReplaced(t *testing.T) {
	docKey := "agent-outputs/1/a-report.docx"
	imgKey := "agent-outputs/1/b-chart.png"
	md := fmt.Sprintf("[下载](https://%s/%s?x) 和 ![图](https://%s/%s?y)",
		testCOSHost, docKey, testCOSHost, imgKey)
	got := resignCOSLinksWithHost(context.Background(), md, testCOSHost, fakeSigner())
	if !strings.Contains(got, "q-signature=DL") || !strings.Contains(got, "q-signature=IMG") {
		t.Fatalf("both links must be re-signed, got: %s", got)
	}
}

func TestResignCOSLinks_LeavesForeignAndEmptyUntouched(t *testing.T) {
	md := "see [docs](https://example.com/a.docx) and plain text"
	if got := resignCOSLinksWithHost(context.Background(), md, testCOSHost, fakeSigner()); got != md {
		t.Fatalf("non-COS links must be untouched, got: %s", got)
	}
	if got := resignCOSLinksWithHost(context.Background(), "any", "", fakeSigner()); got != "any" {
		t.Fatalf("empty host must be a no-op, got: %s", got)
	}
}

func TestResignCOSLinks_SignerErrorKeepsOriginal(t *testing.T) {
	key := "agent-outputs/1/a-report.docx"
	orig := "https://" + testCOSHost + "/" + key + "?stale"
	md := "[下载](" + orig + ")"
	errSigner := cosSigner{
		signImage:    func(_ context.Context, _ string, _ int64) (string, error) { return "", fmt.Errorf("boom") },
		signDownload: func(_ context.Context, _, _ string, _ int64) (string, error) { return "", fmt.Errorf("boom") },
	}
	got := resignCOSLinksWithHost(context.Background(), md, testCOSHost, errSigner)
	if !strings.Contains(got, orig) {
		t.Fatalf("on signer error the original URL must be preserved, got: %s", got)
	}
}
