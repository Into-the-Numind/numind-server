package sandbox

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeInputFile_PathTraversal_ErrUnsafeFilename(t *testing.T) {
	cases := []string{
		"../../../etc/passwd",
		"../../secret.txt",
		"foo/../../bar",
		"dir/../../../evil",
		"file with spaces.txt",
		"file;rm -rf /",
		"hello world",
		"foo<bar",
		"a:b",
	}
	for _, name := range cases {
		if err := SanitizeInputFile(name, []byte("data")); !errors.Is(err, ErrUnsafeFilename) {
			t.Errorf("SanitizeInputFile(%q) = %v; want ErrUnsafeFilename", name, err)
		}
	}
}

func TestSanitizeInputFile_TooLarge_ErrInputTooLarge(t *testing.T) {
	big := make([]byte, MaxInputFileSizeBytes+1)
	if err := SanitizeInputFile("large.bin", big); !errors.Is(err, ErrInputTooLarge) {
		t.Errorf("SanitizeInputFile too-large = %v; want ErrInputTooLarge", err)
	}
}

func TestSanitizeInputFile_WithMacro_ErrMacroDetected(t *testing.T) {
	// Build a zip archive with a vbaProject.bin entry.
	data := makeMinimalZip(t, "vbaProject.bin", []byte("VBA content"))
	if err := SanitizeInputFile("workbook.xlsx", data); !errors.Is(err, ErrMacroDetected) {
		t.Errorf("SanitizeInputFile macro xlsx = %v; want ErrMacroDetected", err)
	}
}

func TestSanitizeInputFile_MacroInSubdir_ErrMacroDetected(t *testing.T) {
	// vbaProject.bin nested under xl/ subdirectory.
	data := makeMinimalZip(t, "xl/vbaProject.bin", []byte("VBA content"))
	if err := SanitizeInputFile("book.xlsx", data); !errors.Is(err, ErrMacroDetected) {
		t.Errorf("SanitizeInputFile macro nested = %v; want ErrMacroDetected", err)
	}
}

func TestSanitizeInputFile_NormalDocx_Pass(t *testing.T) {
	// A docx (zip) with no vbaProject.bin.
	data := makeMinimalZip(t, "word/document.xml", []byte(`<?xml version="1.0"?><document/>`))
	if err := SanitizeInputFile("report.docx", data); err != nil {
		t.Errorf("SanitizeInputFile clean docx = %v; want nil", err)
	}
}

func TestSanitizeInputFile_ValidNames_Pass(t *testing.T) {
	cases := []string{
		"hello.txt",
		"data-2024.csv",
		"report_v2.xlsx",
		"image.PNG",
		"My-File_123.pdf",
	}
	for _, name := range cases {
		if err := SanitizeInputFile(name, []byte("x")); err != nil {
			t.Errorf("SanitizeInputFile(%q) = %v; want nil", name, err)
		}
	}
}

func TestSanitizeInputFile_ExactSizeLimit_Pass(t *testing.T) {
	// Exactly at the limit should pass.
	data := make([]byte, MaxInputFileSizeBytes)
	if err := SanitizeInputFile("exact.bin", data); err != nil {
		t.Errorf("SanitizeInputFile exactly at limit = %v; want nil", err)
	}
}

func TestSanitizeInputFile_FilenameTooLong_ErrUnsafeFilename(t *testing.T) {
	long := strings.Repeat("a", MaxInputFilenameLen+1) + ".txt"
	if err := SanitizeInputFile(long, []byte("x")); !errors.Is(err, ErrUnsafeFilename) {
		t.Errorf("SanitizeInputFile long filename = %v; want ErrUnsafeFilename", err)
	}
}
