package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
)

// Constants for data generation
const (
	OutputDir = "test_data"

	// Prompt scenarios
	NormalPromptLen   = 2000   // 2k tokens approx
	LongPromptLen     = 50000  // 50k tokens (Standard large)
	CriticalPromptLen = 120000 // 120k tokens (Close to DeepSeek 128k limit)
	OverflowPromptLen = 200000 // 200k tokens (Overflow test)

	// PDF Scenarios
	PDFNormalSizeKB = 500       // 500KB
	PDFLargeSizeKB  = 20 * 1024 // 20MB
	PDFHugeSizeKB   = 50 * 1024 // 50MB (Slow extraction test)
)

func main() {
	if err := os.MkdirAll(OutputDir, 0755); err != nil {
		fmt.Printf("Failed to create output dir: %v\n", err)
		return
	}

	fmt.Println("=== Starting Test Data Generation ===")

	// 1. Generate Prompts
	generateTextFile("prompt_normal_2k.txt", NormalPromptLen)
	generateTextFile("prompt_long_50k.txt", LongPromptLen)
	generateTextFile("prompt_critical_120k.txt", CriticalPromptLen)
	generateTextFile("prompt_overflow_200k.txt", OverflowPromptLen)

	// 2. Generate PDFs
	// Warning: Generating 50MB PDF might take a few seconds
	generatePDF("doc_normal.pdf", 10)       // ~10 pages
	generatePDF("doc_large_500pg.pdf", 500) // ~500 pages, text heavy
	// generatePDF("doc_huge_images.pdf", ...) // Requires image assets, skipping for simple text-based PDF test now.

	fmt.Println("=== Data Generation Complete ===")
	fmt.Printf("All files saved in ./%s/\n", OutputDir)
}

// generateTextFile creates a text file with approx 'charCount' characters (treating 1 char ~ 1 token for rough estimation, though actually 1 token ~ 4 chars in English or 1 char in Chinese)
// Let's assume input is "Target Character Count".
func generateTextFile(filename string, charCount int) {
	fmt.Printf("Generating %s (~%d chars)... ", filename, charCount)
	start := time.Now()

	f, err := os.Create(filepath.Join(OutputDir, filename))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer f.Close()

	// Use specific repetitive but varied content to avoid aggressive Zip compression by clever servers,
	// and to ensure meaningful processing load.
	baseText := "这是一个用于测试系统稳定性的长文本段落。This is a specific paragraph for testing system stability. "

	repeats := charCount / len(baseText)
	for i := 0; i < repeats; i++ {
		// Inject some randomness to prevent caching
		if i%100 == 0 {
			f.WriteString(fmt.Sprintf("\n[Block %d - %s]\n", i, uuid.New().String()))
		}
		f.WriteString(baseText)
	}

	fmt.Printf("Done (%v)\n", time.Since(start))
}

// generatePDF creates a text-heavy PDF
func generatePDF(filename string, pageCount int) {
	fmt.Printf("Generating %s (%d pages)... ", filename, pageCount)
	start := time.Now()

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Arial", "B", 16)
	pdf.Cell(40, 10, "Stress Test Document")
	pdf.Ln(12)

	pdf.SetFont("Arial", "", 12)

	lineHeight := 6.0
	// 50 lines per page approx
	for i := 0; i < pageCount; i++ {
		if i > 0 {
			pdf.AddPage()
		}
		pdf.Cell(0, 10, fmt.Sprintf("Page %d of %d", i+1, pageCount))
		pdf.Ln(10)

		for j := 0; j < 40; j++ {
			randStr := randomString(80)
			pdf.Cell(0, lineHeight, randStr)
			pdf.Ln(lineHeight)
		}
	}

	err := pdf.OutputFileAndClose(filepath.Join(OutputDir, filename))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Printf("Done (%v)\n", time.Since(start))
}

func randomString(n int) string {
	var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 ")
	s := make([]rune, n)
	for i := range s {
		s[i] = letters[rand.Intn(len(letters))]
	}
	return string(s)
}
