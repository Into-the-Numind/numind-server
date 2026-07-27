package agent

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	docxContentTypeNative = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	xlsxContentTypeNative = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	pptxContentTypeNative = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

type nativeDocxInput struct {
	Markdown string
	Images   map[string]nativeDocxImage
}

type nativeDocxImage struct {
	Name        string
	Data        []byte
	Ext         string
	ContentType string
	WidthPx     int
	HeightPx    int
}

func buildNativeDocx(in nativeDocxInput) ([]byte, error) {
	body, rels, contentTypeDefaults := nativeDocxBody(in)
	entries := map[string][]byte{
		"[Content_Types].xml": []byte(docxContentTypes(contentTypeDefaults)),
		"_rels/.rels": []byte(xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
			`</Relationships>`),
		"docProps/app.xml":  []byte(docPropsApp("Microsoft Word")),
		"docProps/core.xml": []byte(docPropsCore()),
		"word/document.xml": []byte(xmlHeader + `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
			`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
			`xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" ` +
			`xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
			`xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><w:body>` +
			body +
			`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="720" w:footer="720" w:gutter="0"/></w:sectPr>` +
			`</w:body></w:document>`),
		"word/styles.xml": []byte(xmlHeader + `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
			`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>` +
			`<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:sz w:val="32"/></w:rPr></w:style>` +
			`<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:b/><w:sz w:val="28"/></w:rPr></w:style>` +
			`<w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:b/><w:sz w:val="24"/></w:rPr></w:style>` +
			`</w:styles>`),
	}
	if rels == "" {
		rels = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"></Relationships>`
	} else {
		rels = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + rels + `</Relationships>`
	}
	entries["word/_rels/document.xml.rels"] = []byte(xmlHeader + rels)
	for _, img := range in.Images {
		entries["word/media/"+img.Name] = img.Data
	}
	return writeOOXMLZip(entries)
}

func nativeDocxBody(in nativeDocxInput) (body string, rels string, contentTypeDefaults map[string]string) {
	lines := strings.Split(in.Markdown, "\n")
	var b strings.Builder
	imageRels := make(map[string]string)
	contentTypeDefaults = make(map[string]string)
	imageID := 1

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if imageAlt, imageRef, ok := parseMarkdownImageLine(line); ok {
			if img, found := findNativeDocxImage(in.Images, imageRef); found {
				rid := imageRels[img.Name]
				if rid == "" {
					rid = fmt.Sprintf("rId%d", imageID)
					imageID++
					imageRels[img.Name] = rid
					rels += fmt.Sprintf(`<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/%s"/>`, rid, xmlAttr(img.Name))
					contentTypeDefaults[strings.TrimPrefix(img.Ext, ".")] = img.ContentType
				}
				b.WriteString(docxImageParagraph(rid, imageAlt, imageID, img.WidthPx, img.HeightPx))
				continue
			}
		}
		if level, text, ok := parseMarkdownHeading(line); ok {
			b.WriteString(docxParagraph(text, fmt.Sprintf("Heading%d", level), ""))
			continue
		}
		if text, ok := parseMarkdownBullet(line); ok {
			b.WriteString(docxParagraph("• "+text, "", "720"))
			continue
		}
		if text, ok := parseMarkdownNumbered(line); ok {
			b.WriteString(docxParagraph(text, "", "720"))
			continue
		}
		if isMarkdownTableStart(lines, i) {
			rows := make([][]string, 0)
			rows = append(rows, splitMarkdownTableRow(lines[i]))
			i += 2
			for i < len(lines) && strings.Contains(lines[i], "|") {
				rows = append(rows, splitMarkdownTableRow(lines[i]))
				i++
			}
			i--
			b.WriteString(docxTable(rows))
			continue
		}
		b.WriteString(docxParagraph(stripInlineMarkdown(line), "", ""))
	}
	if b.Len() == 0 {
		b.WriteString(docxParagraph(" ", "", ""))
	}
	return b.String(), rels, contentTypeDefaults
}

func buildNativeXLSX(in createXLSXInput) ([]byte, error) {
	sheets := normalizeXLSXSheets(in)
	if len(sheets) == 0 {
		return nil, fmt.Errorf("at least one sheet or row is required")
	}
	entries := map[string][]byte{
		"[Content_Types].xml": []byte(xlsxContentTypes(len(sheets))),
		"_rels/.rels": []byte(xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
			`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>` +
			`</Relationships>`),
		"docProps/app.xml":  []byte(docPropsApp("Microsoft Excel")),
		"docProps/core.xml": []byte(docPropsCore()),
	}
	var wb strings.Builder
	var wbRels strings.Builder
	wb.WriteString(xmlHeader + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	for i, sheet := range sheets {
		sheetID := i + 1
		name := sanitizeSheetName(sheet.Name, sheetID)
		wb.WriteString(fmt.Sprintf(`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlAttr(name), sheetID, sheetID))
		wbRels.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, sheetID, sheetID))
		entries[fmt.Sprintf("xl/worksheets/sheet%d.xml", sheetID)] = []byte(xlsxWorksheetXML(sheet))
	}
	wb.WriteString(`</sheets></workbook>`)
	entries["xl/workbook.xml"] = []byte(wb.String())
	entries["xl/_rels/workbook.xml.rels"] = []byte(xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + wbRels.String() + `</Relationships>`)
	return writeOOXMLZip(entries)
}

func buildNativePPTX(in createPPTXInput) ([]byte, error) {
	if len(in.Slides) == 0 {
		return nil, fmt.Errorf("slides is required")
	}
	entries := map[string][]byte{
		"[Content_Types].xml": []byte(pptxContentTypes(len(in.Slides))),
		"_rels/.rels": []byte(xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
			`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>` +
			`</Relationships>`),
		"docProps/app.xml":                             []byte(docPropsApp("Microsoft PowerPoint")),
		"docProps/core.xml":                            []byte(docPropsCore()),
		"ppt/theme/theme1.xml":                         []byte(pptxThemeXML()),
		"ppt/slideMasters/slideMaster1.xml":            []byte(pptxSlideMasterXML()),
		"ppt/slideMasters/_rels/slideMaster1.xml.rels": []byte(pptxSlideMasterRelsXML()),
		"ppt/slideLayouts/slideLayout1.xml":            []byte(pptxSlideLayoutXML()),
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": []byte(pptxSlideLayoutRelsXML()),
	}
	var sldIDs strings.Builder
	var presRels strings.Builder
	presRels.WriteString(`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>`)
	for i, slide := range in.Slides {
		id := i + 1
		relID := id + 1
		sldIDs.WriteString(fmt.Sprintf(`<p:sldId id="%d" r:id="rId%d"/>`, 255+id, relID))
		presRels.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide%d.xml"/>`, relID, id))
		entries[fmt.Sprintf("ppt/slides/slide%d.xml", id)] = []byte(pptxSlideXML(slide, id))
		entries[fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", id)] = []byte(pptxSlideRelsXML())
	}
	entries["ppt/presentation.xml"] = []byte(xmlHeader + `<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
		`xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldMasterIdLst>` +
		`<p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst><p:sldIdLst>` +
		sldIDs.String() +
		`</p:sldIdLst><p:sldSz cx="12192000" cy="6858000" type="wide"/><p:notesSz cx="6858000" cy="9144000"/></p:presentation>`)
	entries["ppt/_rels/presentation.xml.rels"] = []byte(xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + presRels.String() + `</Relationships>`)
	return writeOOXMLZip(entries)
}

func writeOOXMLZip(entries map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Unix(0, 0).UTC()}
		w, err := zw.CreateHeader(h)
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		if _, err := w.Write(entries[name]); err != nil {
			_ = zw.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`

func docxContentTypes(imageDefaults map[string]string) string {
	var defaults strings.Builder
	for ext, ctype := range imageDefaults {
		if ext == "" || ctype == "" {
			continue
		}
		defaults.WriteString(fmt.Sprintf(`<Default Extension="%s" ContentType="%s"/>`, xmlAttr(ext), xmlAttr(ctype)))
	}
	return xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		defaults.String() +
		`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>` +
		`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
		`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
		`<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>` +
		`</Types>`
}

func xlsxContentTypes(sheetCount int) string {
	var sheets strings.Builder
	for i := 1; i <= sheetCount; i++ {
		sheets.WriteString(fmt.Sprintf(`<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i))
	}
	return xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>` +
		`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
		sheets.String() +
		`</Types>`
}

func pptxContentTypes(slideCount int) string {
	var slides strings.Builder
	for i := 1; i <= slideCount; i++ {
		slides.WriteString(fmt.Sprintf(`<Override PartName="/ppt/slides/slide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>`, i))
	}
	return xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>` +
		`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
		`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>` +
		`<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>` +
		`<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>` +
		`<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>` +
		slides.String() +
		`</Types>`
}

func docPropsApp(app string) string {
	return xmlHeader + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><Application>` + xmlText(app) + `</Application></Properties>`
}

func docPropsCore() string {
	now := time.Now().UTC().Format(time.RFC3339)
	return xmlHeader + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
		`<dc:creator>Numind</dc:creator><cp:lastModifiedBy>Numind</cp:lastModifiedBy>` +
		`<dcterms:created xsi:type="dcterms:W3CDTF">` + now + `</dcterms:created>` +
		`<dcterms:modified xsi:type="dcterms:W3CDTF">` + now + `</dcterms:modified>` +
		`</cp:coreProperties>`
}

func docxParagraph(text, style, indent string) string {
	var ppr strings.Builder
	if style != "" || indent != "" {
		ppr.WriteString(`<w:pPr>`)
		if style != "" {
			ppr.WriteString(`<w:pStyle w:val="` + xmlAttr(style) + `"/>`)
		}
		if indent != "" {
			ppr.WriteString(`<w:ind w:left="` + xmlAttr(indent) + `"/>`)
		}
		ppr.WriteString(`</w:pPr>`)
	}
	return `<w:p>` + ppr.String() + `<w:r><w:t xml:space="preserve">` + xmlText(text) + `</w:t></w:r></w:p>`
}

func docxTable(rows [][]string) string {
	var b strings.Builder
	b.WriteString(`<w:tbl><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:left w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:bottom w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:right w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:insideH w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:insideV w:val="single" w:sz="4" w:space="0" w:color="auto"/></w:tblBorders></w:tblPr>`)
	for _, row := range rows {
		b.WriteString(`<w:tr>`)
		for _, cell := range row {
			b.WriteString(`<w:tc><w:p><w:r><w:t xml:space="preserve">` + xmlText(stripInlineMarkdown(cell)) + `</w:t></w:r></w:p></w:tc>`)
		}
		b.WriteString(`</w:tr>`)
	}
	b.WriteString(`</w:tbl>`)
	return b.String()
}

func docxImageParagraph(rid, alt string, id, widthPx, heightPx int) string {
	if widthPx <= 0 {
		widthPx = 640
	}
	if heightPx <= 0 {
		heightPx = 360
	}
	const emuPerPixel = 9525
	cx := widthPx * emuPerPixel
	cy := heightPx * emuPerPixel
	maxCx := 6 * 914400
	if cx > maxCx {
		cy = cy * maxCx / cx
		cx = maxCx
	}
	return fmt.Sprintf(`<w:p><w:r><w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0">`+
		`<wp:extent cx="%d" cy="%d"/><wp:docPr id="%d" name="Picture %d" descr="%s"/>`+
		`<a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture">`+
		`<pic:pic><pic:nvPicPr><pic:cNvPr id="%d" name="%s"/><pic:cNvPicPr/></pic:nvPicPr>`+
		`<pic:blipFill><a:blip r:embed="%s"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill>`+
		`<pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr>`+
		`</pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>`,
		cx, cy, id, id, xmlAttr(alt), id, xmlAttr(alt), rid, cx, cy)
}

func xlsxWorksheetXML(sheet xlsxSheetInput) string {
	headers := normalizedXLSXHeaders(sheet)
	rows := normalizedXLSXRows(headers, sheet.Rows)
	var b strings.Builder
	b.WriteString(xmlHeader + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	rowNum := 1
	if len(headers) > 0 {
		b.WriteString(xlsxRowXML(rowNum, headers))
		rowNum++
	}
	for _, row := range rows {
		b.WriteString(xlsxRowXML(rowNum, row))
		rowNum++
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

func xlsxRowXML(rowNum int, values []string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<row r="%d">`, rowNum))
	for i, value := range values {
		ref := columnName(i+1) + strconv.Itoa(rowNum)
		b.WriteString(`<c r="` + ref + `" t="inlineStr"><is><t xml:space="preserve">` + xmlText(value) + `</t></is></c>`)
	}
	b.WriteString(`</row>`)
	return b.String()
}

func normalizeXLSXSheets(in createXLSXInput) []xlsxSheetInput {
	if len(in.Sheets) > 0 {
		return in.Sheets
	}
	if len(in.Headers) == 0 && len(in.Rows) == 0 {
		return nil
	}
	return []xlsxSheetInput{{Name: "Sheet1", Headers: in.Headers, Rows: in.Rows}}
}

func normalizedXLSXHeaders(sheet xlsxSheetInput) []string {
	if len(sheet.Headers) > 0 {
		return sheet.Headers
	}
	for _, row := range sheet.Rows {
		if m, ok := row.(map[string]any); ok {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return keys
		}
	}
	return nil
}

func normalizedXLSXRows(headers []string, rows []any) [][]string {
	out := make([][]string, 0, len(rows))
	for _, row := range rows {
		switch v := row.(type) {
		case []any:
			out = append(out, stringifyAnySlice(v))
		case []string:
			out = append(out, append([]string(nil), v...))
		case map[string]any:
			if len(headers) == 0 {
				headers = sortedMapKeys(v)
			}
			values := make([]string, len(headers))
			for i, h := range headers {
				values[i] = officeCellString(v[h])
			}
			out = append(out, values)
		default:
			out = append(out, []string{officeCellString(v)})
		}
	}
	return out
}

func stringifyAnySlice(values []any) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = officeCellString(value)
	}
	return out
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func officeCellString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	default:
		return fmt.Sprint(v)
	}
}

func pptxSlideXML(slide pptxSlideInput, slideID int) string {
	title := strings.TrimSpace(slide.Title)
	if title == "" {
		title = fmt.Sprintf("Slide %d", slideID)
	}
	var shapes strings.Builder
	shapes.WriteString(pptxTextShape(2, "Title", 686000, 450000, 10800000, 900000, 3600, []pptxParagraph{{Text: title}}))
	if strings.TrimSpace(slide.Subtitle) != "" {
		shapes.WriteString(pptxTextShape(3, "Subtitle", 900000, 1300000, 10400000, 650000, 2200, []pptxParagraph{{Text: slide.Subtitle}}))
	}
	if len(slide.Bullets) > 0 {
		paragraphs := make([]pptxParagraph, 0, len(slide.Bullets))
		for _, bullet := range slide.Bullets {
			if strings.TrimSpace(bullet) != "" {
				paragraphs = append(paragraphs, pptxParagraph{Text: bullet, Bullet: true})
			}
		}
		shapes.WriteString(pptxTextShape(4, "Bullets", 900000, 2150000, 10400000, 3000000, 2000, paragraphs))
	}
	if strings.TrimSpace(slide.Notes) != "" {
		shapes.WriteString(pptxTextShape(5, "Notes", 900000, 5650000, 10400000, 600000, 1400, []pptxParagraph{{Text: "Notes: " + slide.Notes}}))
	}
	return xmlHeader + `<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
		`<p:cSld><p:spTree>` + pptxEmptyGroupShape() +
		shapes.String() +
		`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`
}

func pptxSlideRelsXML() string {
	return xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>` +
		`</Relationships>`
}

func pptxSlideMasterRelsXML() string {
	return xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>` +
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/>` +
		`</Relationships>`
}

func pptxSlideLayoutRelsXML() string {
	return xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/>` +
		`</Relationships>`
}

func pptxSlideMasterXML() string {
	return xmlHeader + `<p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
		`<p:cSld><p:spTree>` + pptxEmptyGroupShape() + `</p:spTree></p:cSld>` +
		`<p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/>` +
		`<p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/></p:sldLayoutIdLst>` +
		`<p:txStyles><p:titleStyle/><p:bodyStyle/><p:otherStyle/></p:txStyles></p:sldMaster>`
}

func pptxSlideLayoutXML() string {
	return xmlHeader + `<p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" type="blank" preserve="1">` +
		`<p:cSld name="Blank"><p:spTree>` + pptxEmptyGroupShape() + `</p:spTree></p:cSld>` +
		`<p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`
}

func pptxThemeXML() string {
	return xmlHeader + `<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="Numind">` +
		`<a:themeElements><a:clrScheme name="Numind">` +
		`<a:dk1><a:sysClr val="windowText" lastClr="000000"/></a:dk1><a:lt1><a:sysClr val="window" lastClr="FFFFFF"/></a:lt1>` +
		`<a:dk2><a:srgbClr val="1F2937"/></a:dk2><a:lt2><a:srgbClr val="F8FAFC"/></a:lt2>` +
		`<a:accent1><a:srgbClr val="2563EB"/></a:accent1><a:accent2><a:srgbClr val="16A34A"/></a:accent2>` +
		`<a:accent3><a:srgbClr val="F59E0B"/></a:accent3><a:accent4><a:srgbClr val="DC2626"/></a:accent4>` +
		`<a:accent5><a:srgbClr val="7C3AED"/></a:accent5><a:accent6><a:srgbClr val="0891B2"/></a:accent6>` +
		`<a:hlink><a:srgbClr val="2563EB"/></a:hlink><a:folHlink><a:srgbClr val="7C3AED"/></a:folHlink></a:clrScheme>` +
		`<a:fontScheme name="Numind"><a:majorFont><a:latin typeface="Aptos Display"/><a:ea typeface="Microsoft YaHei"/><a:cs typeface=""/></a:majorFont>` +
		`<a:minorFont><a:latin typeface="Aptos"/><a:ea typeface="Microsoft YaHei"/><a:cs typeface=""/></a:minorFont></a:fontScheme>` +
		`<a:fmtScheme name="Numind"><a:fillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:fillStyleLst>` +
		`<a:lnStyleLst><a:ln w="9525"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln></a:lnStyleLst>` +
		`<a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst>` +
		`<a:bgFillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:bgFillStyleLst></a:fmtScheme>` +
		`</a:themeElements><a:objectDefaults/><a:extraClrSchemeLst/></a:theme>`
}

func pptxEmptyGroupShape() string {
	return `<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>`
}

type pptxParagraph struct {
	Text   string
	Bullet bool
}

func pptxTextShape(id int, name string, x, y, cx, cy, fontSize int, paragraphs []pptxParagraph) string {
	var body strings.Builder
	if len(paragraphs) == 0 {
		paragraphs = []pptxParagraph{{Text: ""}}
	}
	for _, p := range paragraphs {
		bullet := ""
		if p.Bullet {
			bullet = `<a:pPr><a:buChar char="•"/></a:pPr>`
		}
		body.WriteString(`<a:p>` + bullet + `<a:r><a:rPr lang="zh-CN" sz="` + strconv.Itoa(fontSize) + `"/><a:t>` + xmlText(p.Text) + `</a:t></a:r></a:p>`)
	}
	return fmt.Sprintf(`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:noFill/><a:ln><a:noFill/></a:ln></p:spPr><p:txBody><a:bodyPr wrap="square"/><a:lstStyle/>%s</p:txBody></p:sp>`,
		id, xmlAttr(name), x, y, cx, cy, body.String())
}

func parseMarkdownHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 3 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	return level, stripInlineMarkdown(strings.TrimSpace(line[level:])), true
}

func parseMarkdownBullet(line string) (string, bool) {
	if len(line) < 3 {
		return "", false
	}
	if (strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ")) && strings.TrimSpace(line[2:]) != "" {
		return stripInlineMarkdown(strings.TrimSpace(line[2:])), true
	}
	return "", false
}

func parseMarkdownNumbered(line string) (string, bool) {
	dot := strings.Index(line, ". ")
	if dot <= 0 {
		return "", false
	}
	for _, r := range line[:dot] {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return stripInlineMarkdown(strings.TrimSpace(line)), true
}

func isMarkdownTableStart(lines []string, i int) bool {
	if i+1 >= len(lines) || !strings.Contains(lines[i], "|") {
		return false
	}
	sep := strings.TrimSpace(lines[i+1])
	if !strings.Contains(sep, "|") {
		return false
	}
	for _, r := range strings.ReplaceAll(sep, "|", "") {
		if r != '-' && r != ':' && r != ' ' {
			return false
		}
	}
	return true
}

func splitMarkdownTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func parseMarkdownImageLine(line string) (alt, ref string, ok bool) {
	if !strings.HasPrefix(line, "![") {
		return "", "", false
	}
	endAlt := strings.Index(line, "](")
	if endAlt < 2 || !strings.HasSuffix(line, ")") {
		return "", "", false
	}
	return line[2:endAlt], strings.TrimSpace(line[endAlt+2 : len(line)-1]), true
}

func findNativeDocxImage(images map[string]nativeDocxImage, ref string) (nativeDocxImage, bool) {
	if len(images) == 0 {
		return nativeDocxImage{}, false
	}
	candidates := []string{ref, filepath.Base(ref), sanitizeOutputFilename(ref), sanitizeOutputFilename(filepath.Base(ref))}
	for _, candidate := range candidates {
		if img, ok := images[strings.ToLower(candidate)]; ok {
			return img, true
		}
	}
	return nativeDocxImage{}, false
}

func decodeNativeDocxImage(name string, data []byte) (nativeDocxImage, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nativeDocxImage{}, fmt.Errorf("unsupported image: %w", err)
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".jpeg" {
		ext = ".jpg"
	}
	if ext == "" {
		ext = "." + format
	}
	ctype := mime.TypeByExtension(ext)
	if ctype == "" {
		ctype = http.DetectContentType(data)
	}
	if !strings.HasPrefix(ctype, "image/") {
		return nativeDocxImage{}, fmt.Errorf("unsupported image content type: %s", ctype)
	}
	safeName := strings.TrimSuffix(sanitizeOutputFilename(name), filepath.Ext(name)) + ext
	return nativeDocxImage{
		Name:        safeName,
		Data:        data,
		Ext:         ext,
		ContentType: ctype,
		WidthPx:     cfg.Width,
		HeightPx:    cfg.Height,
	}, nil
}

func stripInlineMarkdown(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "`", "")
	return s
}

func columnName(n int) string {
	if n <= 0 {
		return "A"
	}
	var out []byte
	for n > 0 {
		n--
		out = append([]byte{byte('A' + n%26)}, out...)
		n /= 26
	}
	return string(out)
}

func sanitizeSheetName(name string, index int) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("Sheet%d", index)
	}
	replacer := strings.NewReplacer("[", "_", "]", "_", ":", "_", "*", "_", "?", "_", "/", "_", "\\", "_")
	name = replacer.Replace(name)
	if len([]rune(name)) > 31 {
		name = string([]rune(name)[:31])
	}
	return name
}

func resolveOfficeFilename(filename, fallbackPrefix, ext string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = fallbackPrefix + "_" + time.Now().Format("20060102_150405") + ext
	}
	if !strings.HasSuffix(strings.ToLower(filename), ext) {
		filename += ext
	}
	return filename
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func xmlAttr(s string) string {
	return xmlText(s)
}
