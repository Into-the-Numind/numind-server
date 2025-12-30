package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

// Chaos Modes
const (
	ModeNormal    = "normal"
	ModeTimeout   = "timeout"
	ModeError500  = "error500"
	ModeRateLimit = "ratelimit"
	ModeEmpty     = "empty"
	ModeGarbage   = "garbage"
)

var (
	port = flag.Int("port", 9999, "Port to run the mock server on")
	mode = flag.String("mode", ModeNormal, "Chaos mode: normal, timeout, error500, ratelimit, empty, garbage")
)

type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func main() {
	flag.Parse()

	http.HandleFunc("/", handleRequest)

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("👻 Chaos Mock Server running on %s\n", addr)
	fmt.Printf("Current Mode: %s\n", *mode)

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	log.Printf("[%s] Received request: %s %s", *mode, r.Method, r.URL.Path)

	// Simulate based on mode
	switch *mode {
	case ModeTimeout:
		// Simulate a timeout (e.g. 65s, assuming client timeout is 30-60s)
		time.Sleep(65 * time.Second)
		// Usually client disconnects before this, but if not:
		http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)

	case ModeError500:
		http.Error(w, "Internal Server Error (Chaos Injection)", http.StatusInternalServerError)

	case ModeRateLimit:
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)

	case ModeEmpty:
		// Return 200 OK but empty body (or empty JSON)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))

	case ModeGarbage:
		// Return valid JSON structure but malformed or unexpected content
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices": [{"message": {"content": null}}]}`)) // Null content test

	default: // ModeNormal
		// Simulate slight latency
		time.Sleep(500 * time.Millisecond)

		resp := ChatCompletionResponse{
			ID:      "chatcmpl-mock-normal",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   "mock-gpt-4",
		}
		// Construct a valid choice
		choice := struct {
			Index   int `json:"index"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			Index:        0,
			FinishReason: "stop",
		}
		choice.Message.Role = "assistant"
		choice.Message.Content = "This is a mocked normal response from the Chaos Server."

		resp.Choices = append(resp.Choices, choice)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
