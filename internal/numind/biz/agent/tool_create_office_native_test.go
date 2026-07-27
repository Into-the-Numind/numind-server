package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateDocx_NativePackage_NoSandboxRequired(t *testing.T) {
	data, err := buildNativeDocx(nativeDocxInput{
		Markdown: "# 周报\n\n这是正文。\n\n- 事项 A\n- 事项 B\n\n| 指标 | 数值 |\n|---|---|\n| 成交 | 12 |",
	})
	require.NoError(t, err)
	entries := zipEntries(t, data)

	require.Contains(t, entries, "[Content_Types].xml")
	require.Contains(t, entries, "_rels/.rels")
	require.Contains(t, entries, "word/document.xml")
	assert.Contains(t, entries["word/document.xml"], "周报")
	assert.Contains(t, entries["word/document.xml"], "事项 A")
	assert.Contains(t, entries["word/document.xml"], "<w:tbl>")
}

func TestCreateXLSX_NativePackage_NoSandboxRequired(t *testing.T) {
	data, err := buildNativeXLSX(createXLSXInput{
		Sheets: []xlsxSheetInput{{
			Name:    "销售",
			Headers: []string{"姓名", "金额"},
			Rows: []any{
				[]any{"张三", 1200},
				map[string]any{"姓名": "李四", "金额": 3400},
			},
		}},
	})
	require.NoError(t, err)
	entries := zipEntries(t, data)

	require.Contains(t, entries, "[Content_Types].xml")
	require.Contains(t, entries, "xl/workbook.xml")
	require.Contains(t, entries, "xl/worksheets/sheet1.xml")
	assert.Contains(t, entries["xl/workbook.xml"], "销售")
	assert.Contains(t, entries["xl/worksheets/sheet1.xml"], "张三")
	assert.Contains(t, entries["xl/worksheets/sheet1.xml"], "3400")
}

func TestCreatePPTX_NativePackage_NoSandboxRequired(t *testing.T) {
	data, err := buildNativePPTX(createPPTXInput{
		Slides: []pptxSlideInput{{
			Title:    "增长复盘",
			Subtitle: "2026 Q3",
			Bullets:  []string{"线索增长", "转化提升"},
			Notes:    "内部讨论",
		}},
	})
	require.NoError(t, err)
	entries := zipEntries(t, data)

	require.Contains(t, entries, "[Content_Types].xml")
	require.Contains(t, entries, "ppt/presentation.xml")
	require.Contains(t, entries, "ppt/slides/slide1.xml")
	assert.Contains(t, entries["ppt/slides/slide1.xml"], "增长复盘")
	assert.Contains(t, entries["ppt/slides/slide1.xml"], "线索增长")
}

func TestCreateXLSX_ExecuteUploadsStandardWorkbook(t *testing.T) {
	tool := &createXLSXTool{}
	res, err := tool.Execute(context.Background(), ToolInput(`{"headers":["A","B"],"rows":[["x",1]],"filename":"report"}`))
	require.NoError(t, err)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(res, &out))
	assert.Equal(t, "xlsx", out.Format)
	assert.True(t, strings.HasSuffix(out.Filename, ".xlsx"))
	assert.NotEmpty(t, out.URL)
	assert.Greater(t, out.SizeBytes, int64(0))
}

func TestCreatePPTX_ExecuteUploadsStandardDeck(t *testing.T) {
	tool := &createPPTXTool{}
	res, err := tool.Execute(context.Background(), ToolInput(`{"slides":[{"title":"T","bullets":["A"]}],"filename":"deck"}`))
	require.NoError(t, err)

	var out fileCreateOutput
	require.NoError(t, json.Unmarshal(res, &out))
	assert.Equal(t, "pptx", out.Format)
	assert.True(t, strings.HasSuffix(out.Filename, ".pptx"))
	assert.NotEmpty(t, out.URL)
	assert.Greater(t, out.SizeBytes, int64(0))
}

func zipEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	entries := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		var buf bytes.Buffer
		_, err = buf.ReadFrom(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		entries[f.Name] = buf.String()
	}
	return entries
}
