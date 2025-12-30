package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Config
const (
	baseURL         = "http://49.233.219.254:9200"
	loginAPI        = "/v1/wechat/login"
	createSopRunAPI = "/v1/sop/runs"
	testDataDir     = "test_data"
	logDir          = "data_injection_results"
)

// Flags
var (
	targetFile  = flag.String("file", "", "Specific file in test_data to upload (e.g. doc_large_500pg.pdf). If empty, assumes text injection.")
	targetText  = flag.String("prompt", "", "Specific prompt file in test_data (e.g. prompt_overflow_200k.txt).")
	concurrency = flag.Int("c", 10, "Concurrency level")
)

var (
	successCount int64
	failCount    int64
)

func main() {
	flag.Parse()

	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Printf("Failed to create log dir: %v\n", err)
		return
	}

	if *targetFile == "" && *targetText == "" {
		fmt.Println("Please specify either -file (for PDF upload test) or -prompt (for long text test)")
		fmt.Println("Available files should be generated in ./test_data/ first using 'go run generate_data.go'")
		return
	}

	// 1. Prepare Payload Content
	var fileContent []byte
	var textContent string
	var err error

	if *targetFile != "" {
		filePath := filepath.Join(testDataDir, *targetFile)
		fileContent, err = os.ReadFile(filePath)
		if err != nil {
			fmt.Printf("Failed to read file %s: %v\n", filePath, err)
			return
		}
		fmt.Printf("Loaded File: %s (%d bytes)\n", *targetFile, len(fileContent))
	}

	if *targetText != "" {
		filePath := filepath.Join(testDataDir, *targetText)
		b, err := os.ReadFile(filePath)
		if err != nil {
			fmt.Printf("Failed to read text file %s: %v\n", filePath, err)
			return
		}
		textContent = string(b)
		fmt.Printf("Loaded Text Prompt: %s (%d chars)\n", *targetText, len(textContent))
	}

	// 2. Login
	token, err := login()
	if err != nil {
		fmt.Printf("Login failed: %v\n", err)
		return
	}
	fmt.Println("Login successful.")

	// 3. Start Workers
	fmt.Printf("Starting Injection Test with %d workers...\n", *concurrency)
	var wg sync.WaitGroup
	startTime := time.Now()

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Each worker creates a fresh SOP run to test injection on
			localErr := runInjectionTest(id, token, *targetFile, fileContent, textContent)
			if localErr != nil {
				atomic.AddInt64(&failCount, 1)
				fmt.Printf("[Worker %d] Failed: %v\n", id, localErr)
				logError(id, localErr)
			} else {
				atomic.AddInt64(&successCount, 1)
				fmt.Printf("[Worker %d] Success\n", id)
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	fmt.Println("\n=== Injection Test Summary ===")
	fmt.Printf("Total Time: %v\n", duration)
	fmt.Printf("Success: %d\n", successCount)
	fmt.Printf("Failed:  %d\n", failCount)
}

func runInjectionTest(workerID int, token string, fileName string, fileData []byte, textData string) error {
	client := &http.Client{
		Timeout: 300 * time.Second, // Long timeout for large files
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// 1. Create Run
	// To test massive input, we can inject it at Creation time OR Node Execution time.
	// For "Prompt Overflow", let's inject at Creation.
	runInputText := "Start Injection Test"
	if textData != "" {
		runInputText = textData
	}

	// NOTE: If text is too huge, CreateRun might fail if it's sent as JSON string.
	// This is part of what we are testing.

	runID, err := createRun(client, token, runInputText)
	if err != nil {
		return fmt.Errorf("create run failed: %w", err)
	}

	// If we only have text, we are done testing CreateRun.
	// But usually we want to verify it works by executing at least one node.
	if fileName == "" {
		return nil
	}

	// 2. Determine Node to Execute (First Node)
	node, _, err := getNextNode(client, token, runID)
	if err != nil {
		return fmt.Errorf("get next node failed: %w", err)
	}
	if node == nil {
		return errors.New("no nodes found in new run")
	}

	// 3. Upload File to Node (Execute Stream)
	// This is the "Upload Large PDF" test
	err = executeNodeWithFile(client, token, runID, node.ID, fileName, fileData)
	if err != nil {
		return fmt.Errorf("file upload failed: %w", err)
	}

	return nil
}

func executeNodeWithFile(client *http.Client, token string, runID, nodeID uint, fileName string, fileData []byte) error {
	url := fmt.Sprintf("%s/v1/sop/runs/%d/nodes/%d/execute", baseURL, runID, nodeID)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file
	part, err := writer.CreateFormFile("files", fileName)
	if err != nil {
		return err
	}
	part.Write(fileData)

	writer.Close()

	req, _ := http.NewRequest("POST", url, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)

	// Do not expect stream for this specific stress test part, just look for 200 OK headers first
	// Because large file upload success is the primary metric here.

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}

	// Drain body
	io.Copy(io.Discard, resp.Body)
	return nil
}

// --- Helpers (Duplicated from flow test for standalone capability) ---

type LoginResponse struct {
	Token string `json:"token"`
}

func login() (string, error) {
	payload := map[string]string{"code": "98"}
	jsonData, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", baseURL+loginAPI, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var res LoginResponse
	json.NewDecoder(resp.Body).Decode(&res)
	return res.Token, nil
}

type CreateRunRequest struct {
	TemplateID int    `json:"template_id"`
	Text       string `json:"text"`
}

func createRun(client *http.Client, token string, text string) (uint, error) {
	// Template ID 1 default
	reqBody := CreateRunRequest{TemplateID: 1, Text: text}
	jsonData, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", baseURL+createSopRunAPI, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}

	type StandardResponse struct {
		Code int `json:"code"`
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
		Message string `json:"message"`
	}
	var stdRes StandardResponse
	json.NewDecoder(resp.Body).Decode(&stdRes)
	if stdRes.Code != 0 {
		return 0, fmt.Errorf(stdRes.Message)
	}
	return stdRes.Data.ID, nil
}

func getNextNode(client *http.Client, token string, runID uint) (*struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
	Sort int    `json:"sort"`
}, bool, error) {
	url := fmt.Sprintf("%s/v1/sop/runs/%d/next", baseURL, runID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, false, fmt.Errorf("status %d", resp.StatusCode)
	}

	type NextNodeData struct {
		Node *struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
			Sort int    `json:"sort"`
		} `json:"node"`
		HasNext bool `json:"has_next"`
	}
	type StandardResponse struct {
		Code int          `json:"code"`
		Data NextNodeData `json:"data"`
	}
	var stdRes StandardResponse
	json.NewDecoder(resp.Body).Decode(&stdRes)
	return stdRes.Data.Node, stdRes.Data.HasNext, nil
}

func logError(workerID int, err error) {
	fileName := filepath.Join(logDir, fmt.Sprintf("error_%d_%d.log", time.Now().Unix(), workerID))
	os.WriteFile(fileName, []byte(err.Error()), 0644)
}
