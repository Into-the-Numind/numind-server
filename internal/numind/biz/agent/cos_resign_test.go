package agent

import (
	"context"
	"fmt"
	"net/url"
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

// TestResignCOSLinks_DecodesUTF8Key locks the readable-object-key change
// (document-editor-ux): a doc whose COS key carries a Chinese name appears
// percent-encoded in the persisted markdown URL (%E6%9C%AC…). On re-sign the key
// must be url.PathUnescape'd back to its raw form before handing to the COS SDK —
// otherwise the SDK re-encodes the already-encoded key (double-encode → 404).
// fakeSigner echoes the key/filename it receives, so we assert the DECODED key
// and name reached it.
func TestResignCOSLinks_DecodesUTF8Key(t *testing.T) {
	rawName := "本周工作小结.docx"
	rawKey := "agent-outputs/1/20260616-101010-" + rawName
	// Tail is percent-encoded in the URL text (as a real COS download URL would be).
	encKey := "agent-outputs/1/20260616-101010-" + url.PathEscape(rawName)
	md := "报告：[点击下载 Word 文档](https://" + testCOSHost + "/" + encKey +
		"?response-content-disposition=x&q-signature=OLD)"

	got := resignCOSLinksWithHost(context.Background(), md, testCOSHost, fakeSigner())

	// signDownload echoed the DECODED key → raw Chinese key present, not double-encoded.
	if !strings.Contains(got, "/"+rawKey+"?") {
		t.Fatalf("re-signed URL must carry the DECODED key %q, got: %s", rawKey, got)
	}
	if strings.Contains(got, "%25E6") {
		t.Fatalf("key was double-encoded (%%25E6…) — PathUnescape missing, got: %s", got)
	}
	// disposition filename derived from the decoded key tail.
	if !strings.Contains(got, "dl="+rawName) {
		t.Fatalf("download filename must be the decoded name %q, got: %s", rawName, got)
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
