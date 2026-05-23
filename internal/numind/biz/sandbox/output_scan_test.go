package sandbox

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// writeTestFile is a helper that writes data to a temp file and returns its path.
func writeTestFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writeTestFile: %v", err)
	}
	return path
}

// makeMinimalZip builds a valid, well-formed zip archive with one entry named
// `entryName` containing `entryData`. This is used to produce a "clean" zip
// for happy-path tests and a zip with vbaProject.bin for macro tests.
func makeMinimalZip(t *testing.T, entryName string, entryData []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create(entryName)
	if err != nil {
		t.Fatalf("makeMinimalZip Create: %v", err)
	}
	if _, err := f.Write(entryData); err != nil {
		t.Fatalf("makeMinimalZip Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("makeMinimalZip Close: %v", err)
	}
	return buf.Bytes()
}

// makeBigZip creates a zip archive whose single entry reports an uncompressed
// size > zipBombExpandedCeiling. We achieve this by manually crafting a local
// file header with an inflated UncompressedSize64 using a stored (no
// compression) entry so the sizes are accurate.
func makeBigZip(t *testing.T) []byte {
	t.Helper()
	// Write a zip where the entry's uncompressed size > 500 MB.
	// We use a Deflate-compressed store of tiny data but set a large header
	// value via the zip64 extension.  The easiest approach: write a zip64
	// archive with actual large data would be too slow in a unit test.
	// Instead, we rely on archive/zip's writer to record the real size;
	// we write a large byte slice to get the actual metadata right.
	//
	// Practical approach: write 501 MB of zeros in a StoredMethod zip.
	// This is fast (memfd) but allocates 501 MB of RAM in the test process,
	// which is unacceptable.
	//
	// Better: write two zip entries each of ~251 MB — still too large.
	//
	// Realistic approach for a unit test: check that our SUM logic works by
	// creating a zip with TWO entries that together exceed the threshold.
	// We'll use small entries but craft the check as a table test.
	//
	// Since writing 500 MB+ in a unit test is impractical, we instead test
	// the underlying logic by passing a path to a zip that Go's zip.OpenReader
	// will report as having a large UncompressedSize64 field.
	//
	// We do this by using the zip.Writer with Store method and 501 MB of
	// pre-computed zeros. We skip this in short mode.
	if testing.Short() {
		t.Skip("makeBigZip: skipping large-file test in -short mode")
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, err := w.CreateHeader(&zip.FileHeader{
		Name:   "large.bin",
		Method: zip.Deflate,
	})
	if err != nil {
		t.Fatalf("makeBigZip CreateHeader: %v", err)
	}
	chunk := make([]byte, 1024*1024) // 1 MB zeros
	for i := 0; i < 501; i++ {
		if _, err := fw.Write(chunk); err != nil {
			t.Fatalf("makeBigZip Write chunk %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("makeBigZip Close: %v", err)
	}
	return buf.Bytes()
}

// ─── Tests ──────────────────────────────────────────────────────────────────

func TestScanOutput_NormalXLSX_Pass(t *testing.T) {
	// A clean xlsx (zip archive with a workbook entry, no macro).
	data := makeMinimalZip(t, "xl/workbook.xml", []byte(`<?xml version="1.0"?><workbook/>`))
	path := writeTestFile(t, "report.xlsx", data)
	if err := ScanOutput(path, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"); err != nil {
		t.Errorf("ScanOutput normal xlsx = %v; want nil", err)
	}
}

func TestScanOutput_FileTooLarge_ErrOutputTooLarge(t *testing.T) {
	// Create a file exactly 1 byte over the 50 MB limit.
	size := maxOutputSizeBytes + 1
	// We don't actually need to write that many bytes — os.Truncate can
	// create a sparse file that stats as the right size. BUT os.Stat will
	// report the logical size, and our check uses info.Size(). Sparse files
	// work on most OS.
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(size)); err != nil {
		f.Close()
		t.Skip("OS does not support sparse files, skipping large-file test")
	}
	f.Close()

	if err := ScanOutput(path, ""); err == nil {
		t.Error("ScanOutput 50MB+1 = nil; want ErrOutputTooLarge")
	} else if err != ErrOutputTooLarge {
		t.Errorf("ScanOutput 50MB+1 = %v; want ErrOutputTooLarge", err)
	}
}

func TestScanOutput_ZipBomb_ErrZipBomb(t *testing.T) {
	data := makeBigZip(t) // skipped in -short
	path := writeTestFile(t, "bomb.zip", data)
	if err := ScanOutput(path, ""); err == nil {
		t.Error("ScanOutput zip-bomb = nil; want ErrZipBomb")
	} else if err != ErrZipBomb {
		t.Errorf("ScanOutput zip-bomb = %v; want ErrZipBomb", err)
	}
}

func TestScanOutput_MimeMismatch_ErrMimeMismatch(t *testing.T) {
	// Write PNG bytes but declare it as xlsx.
	// PNG magic bytes: 0x89 0x50 0x4E 0x47 0x0D 0x0A 0x1A 0x0A
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52}
	// Pad to 512 bytes so DetectContentType works.
	padded := make([]byte, 512)
	copy(padded, pngMagic)
	path := writeTestFile(t, "fake.xlsx", padded)
	err := ScanOutput(path, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	if err == nil {
		t.Error("ScanOutput MIME mismatch = nil; want ErrMimeMismatch")
	} else if err != ErrMimeMismatch {
		t.Errorf("ScanOutput MIME mismatch = %v; want ErrMimeMismatch", err)
	}
}

func TestScanOutput_NonZipFile_SkipsZipBombCheck(t *testing.T) {
	// A plain text CSV — not a zip, so zip-bomb check should be skipped.
	data := []byte("col1,col2\nval1,val2\n")
	path := writeTestFile(t, "data.csv", data)
	if err := ScanOutput(path, "text/csv"); err != nil {
		// text/csv vs detected text/plain — that's compatible.
		// Accept ErrMimeMismatch only if the detection truly differs.
		// In practice Go's DetectContentType returns text/plain for CSV.
		// Our mimeCompatible treats text/* variants as compatible.
		// If we get ErrMimeMismatch something is wrong in the compat logic.
		t.Errorf("ScanOutput non-zip = %v; want nil", err)
	}
}

func TestScanOutput_EmptyDeclaredMime_SkipsMimeCheck(t *testing.T) {
	// Empty declaredMime = skip MIME check.
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52}
	padded := make([]byte, 512)
	copy(padded, pngMagic)
	path := writeTestFile(t, "image.png", padded)
	if err := ScanOutput(path, ""); err != nil {
		t.Errorf("ScanOutput empty mime = %v; want nil", err)
	}
}
