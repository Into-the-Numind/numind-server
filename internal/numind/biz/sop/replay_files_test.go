package sop

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func uptr(v uint) *uint { return &v }

func sopFile(id, nodeID uint, name, mime, ext, objectKey, baseURL, content string) model.SopFile {
	f := model.SopFile{
		NodeID:    uptr(nodeID),
		FileName:  name,
		FileType:  mime,
		FileExt:   ext,
		ObjectKey: objectKey,
		FileURL:   baseURL,
		Content:   content,
		FileSize:  123,
	}
	f.ID = id
	return f
}

// recordingSigners returns signer funcs that record calls and emit distinguishable URLs.
func recordingSigners() (
	signImage func(context.Context, string) (string, error),
	signDownload func(context.Context, string, string) (string, error),
	imageKeys *[]string,
	downloadKeys *[]string,
) {
	imgs := &[]string{}
	dls := &[]string{}
	si := func(_ context.Context, key string) (string, error) {
		*imgs = append(*imgs, key)
		return "signed-image://" + key, nil
	}
	sd := func(_ context.Context, key, name string) (string, error) {
		*dls = append(*dls, key)
		return "signed-dl://" + key + "?name=" + name, nil
	}
	return si, sd, imgs, dls
}

func TestAttachNodeFiles_GroupsByNodeAndRoutesSigning(t *testing.T) {
	nodes := []CompletedNodeInfo{
		{NodeID: 10},
		{NodeID: 20},
	}
	files := []model.SopFile{
		sopFile(1, 10, "shot.png", "image/png", ".png", "sop/u/r/shot.png", "https://cos/base/shot.png", "ocr text"),
		sopFile(2, 10, "report.pdf", "application/pdf", ".pdf", "sop/u/r/report.pdf", "https://cos/base/report.pdf", "pdf text"),
		sopFile(3, 20, "data.xlsx", "application/octet-stream", ".xlsx", "sop/u/r/data.xlsx", "https://cos/base/data.xlsx", ""),
	}

	si, sd, imgKeys, dlKeys := recordingSigners()
	attachNodeFiles(context.Background(), nodes, files, si, sd)

	// node 10: image (signImage) + pdf (signDownload)
	require.Len(t, nodes[0].Files, 2)
	assert.Equal(t, "signed-image://sop/u/r/shot.png", nodes[0].Files[0].FileURL)
	assert.Equal(t, "image/png", nodes[0].Files[0].FileType)
	assert.Equal(t, "ocr text", nodes[0].Files[0].Content)
	assert.Equal(t, "signed-dl://sop/u/r/report.pdf?name=report.pdf", nodes[0].Files[1].FileURL)

	// node 20: xlsx (signDownload)
	require.Len(t, nodes[1].Files, 1)
	assert.Equal(t, "signed-dl://sop/u/r/data.xlsx?name=data.xlsx", nodes[1].Files[0].FileURL)

	assert.Equal(t, []string{"sop/u/r/shot.png"}, *imgKeys, "only the png should route to signImage")
	assert.ElementsMatch(t, []string{"sop/u/r/report.pdf", "sop/u/r/data.xlsx"}, *dlKeys)
}

func TestAttachNodeFiles_ImageDetectedByExtWhenMimeMissing(t *testing.T) {
	nodes := []CompletedNodeInfo{{NodeID: 1}}
	files := []model.SopFile{
		sopFile(1, 1, "pic.JPG", "", ".JPG", "k1", "base://pic", ""),
	}
	si, sd, imgKeys, dlKeys := recordingSigners()
	attachNodeFiles(context.Background(), nodes, files, si, sd)

	require.Len(t, nodes[0].Files, 1)
	assert.Equal(t, "signed-image://k1", nodes[0].Files[0].FileURL)
	assert.Equal(t, []string{"k1"}, *imgKeys)
	assert.Empty(t, *dlKeys)
}

func TestAttachNodeFiles_EmptyObjectKeyKeepsBaseURL(t *testing.T) {
	nodes := []CompletedNodeInfo{{NodeID: 1}}
	files := []model.SopFile{
		sopFile(1, 1, "x.png", "image/png", ".png", "", "https://cos/base/x.png", ""),
	}
	called := false
	si := func(context.Context, string) (string, error) { called = true; return "should-not-be-used", nil }
	sd := func(context.Context, string, string) (string, error) { called = true; return "should-not-be-used", nil }
	attachNodeFiles(context.Background(), nodes, files, si, sd)

	require.Len(t, nodes[0].Files, 1)
	assert.Equal(t, "https://cos/base/x.png", nodes[0].Files[0].FileURL)
	assert.False(t, called, "no signer should be called when object_key is empty")
}

func TestAttachNodeFiles_SignerErrorOrEmptyFallsBackToBaseURL(t *testing.T) {
	nodes := []CompletedNodeInfo{{NodeID: 1}}
	files := []model.SopFile{
		sopFile(1, 1, "a.png", "image/png", ".png", "k-err", "base://a", ""),
		sopFile(2, 1, "b.pdf", "application/pdf", ".pdf", "k-empty", "base://b", ""),
	}
	si := func(context.Context, string) (string, error) { return "", errors.New("cos down") }
	sd := func(context.Context, string, string) (string, error) { return "", nil } // empty, no error
	attachNodeFiles(context.Background(), nodes, files, si, sd)

	require.Len(t, nodes[0].Files, 2)
	assert.Equal(t, "base://a", nodes[0].Files[0].FileURL, "signer error → base url")
	assert.Equal(t, "base://b", nodes[0].Files[1].FileURL, "empty signed url → base url")
}

func TestAttachNodeFiles_NilNodeIDSkippedAndNoopGuards(t *testing.T) {
	nodes := []CompletedNodeInfo{{NodeID: 1}}
	orphan := sopFile(1, 0, "orphan.png", "image/png", ".png", "k", "base://o", "")
	orphan.NodeID = nil
	si, sd, _, _ := recordingSigners()
	attachNodeFiles(context.Background(), nodes, []model.SopFile{orphan}, si, sd)
	assert.Empty(t, nodes[0].Files, "file with nil NodeID is skipped")

	// no-op guards: empty nodes / empty files must not panic
	attachNodeFiles(context.Background(), nil, []model.SopFile{orphan}, si, sd)
	attachNodeFiles(context.Background(), nodes, nil, si, sd)
}

func TestIsImageExt(t *testing.T) {
	cases := []struct {
		ext  string
		want bool
	}{
		{".jpg", true}, {".jpeg", true}, {".png", true}, {".gif", true},
		{".webp", true}, {".bmp", true}, {".svg", true},
		{".JPG", true}, {".PNG", true}, {".JPEG", true}, // uppercase (mobile uploads)
		{"png", true}, {"PNG", true}, // missing leading dot
		{" .png ", true}, // surrounding whitespace
		{".pdf", false}, {".docx", false}, {".xlsx", false}, {".txt", false},
		{"", false}, {".PDF", false},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, isImageExt(c.ext), "isImageExt(%q)", c.ext)
	}
}
