package httpclient

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"numind-server/internal/pkg/log"

	"github.com/spf13/viper"
)

// Config HTTP客户端配置
type Config struct {
	Timeout               time.Duration `yaml:"timeout"`
	ConnectTimeout        time.Duration `yaml:"connect_timeout"`
	ResponseHeaderTimeout time.Duration `yaml:"response_header_timeout"`
	TLSHandshakeTimeout   time.Duration `yaml:"tls_handshake_timeout"`
	IdleConnTimeout       time.Duration `yaml:"idle_conn_timeout"`
	MaxIdleConns          int           `yaml:"max_idle_conns"`
	MaxIdleConnsPerHost   int           `yaml:"max_idle_conns_per_host"`
	MaxRetries            int           `yaml:"max_retries"`
	RetryDelay            time.Duration `yaml:"retry_delay"`
	RetryBackoff          float64       `yaml:"retry_backoff"`
	EnableCompression     bool          `yaml:"enable_compression"`
	UserAgent             string        `yaml:"user_agent"`
}

// DefaultConfig 默认配置
func DefaultConfig() *Config {
	return &Config{
		Timeout:               120 * time.Second,
		ConnectTimeout:        30 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		MaxRetries:            3,
		RetryDelay:            1 * time.Second,
		RetryBackoff:          2.0,
		EnableCompression:     true,
		UserAgent:             "numind-server/1.0",
	}
}

// Client 优化的HTTP客户端
type Client struct {
	config *Config
	client *http.Client
}

// NewClient 创建新的HTTP客户端
func NewClient(config *Config) *Client {
	if config == nil {
		config = DefaultConfig()
	}

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   config.ConnectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          config.MaxIdleConns,
		MaxIdleConnsPerHost:   config.MaxIdleConnsPerHost,
		IdleConnTimeout:       config.IdleConnTimeout,
		TLSHandshakeTimeout:   config.TLSHandshakeTimeout,
		ResponseHeaderTimeout: config.ResponseHeaderTimeout,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
		},
		DisableCompression: !config.EnableCompression,
	}

	client := &http.Client{
		Timeout:   config.Timeout,
		Transport: transport,
	}

	return &Client{
		config: config,
		client: client,
	}
}

// NewClientFromConfig 从配置文件创建客户端
func NewClientFromConfig(configKey string) *Client {
	config := DefaultConfig()

	if viper.IsSet(configKey + ".timeout") {
		config.Timeout = viper.GetDuration(configKey + ".timeout")
	}
	if viper.IsSet(configKey + ".max_retries") {
		config.MaxRetries = viper.GetInt(configKey + ".max_retries")
	}

	return NewClient(config)
}

// Request HTTP请求结构
type Request struct {
	Method      string
	URL         string
	Headers     map[string]string
	Body        io.Reader
	Context     context.Context
	RetryPolicy *RetryPolicy
}

// RetryPolicy 重试策略
type RetryPolicy struct {
	MaxRetries   int
	RetryDelay   time.Duration
	RetryBackoff float64
}

// DefaultRetryPolicy 默认重试策略
func DefaultRetryPolicy() *RetryPolicy {
	return &RetryPolicy{
		MaxRetries:   3,
		RetryDelay:   1 * time.Second,
		RetryBackoff: 2.0,
	}
}

// Do 执行HTTP请求
func (c *Client) Do(req *Request) (*http.Response, error) {
	if req.Context == nil {
		req.Context = context.Background()
	}

	if req.RetryPolicy == nil {
		req.RetryPolicy = DefaultRetryPolicy()
	}

	var lastErr error
	delay := req.RetryPolicy.RetryDelay

	for attempt := 0; attempt <= req.RetryPolicy.MaxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(req.Context, req.Method, req.URL, req.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		httpReq.Header.Set("User-Agent", c.config.UserAgent)
		if req.Body != nil {
			httpReq.Header.Set("Content-Type", "application/json")
		}

		for key, value := range req.Headers {
			httpReq.Header.Set(key, value)
		}

		resp, err := c.client.Do(httpReq)
		if err == nil {
			if c.shouldRetryByStatus(resp.StatusCode) && attempt < req.RetryPolicy.MaxRetries {
				log.C(req.Context).Infow("HTTP request failed with status, retrying",
					"status", resp.StatusCode,
					"attempt", attempt+1,
					"delay", delay)
				resp.Body.Close()
			} else {
				return resp, nil
			}
		} else {
			lastErr = err
			if !c.shouldRetry(err, attempt, req.RetryPolicy) {
				break
			}
			log.C(req.Context).Infow("HTTP request failed with error, retrying",
				"attempt", attempt+1,
				"error", err.Error(),
				"delay", delay)
		}

		select {
		case <-req.Context.Done():
			return nil, req.Context.Err()
		case <-time.After(delay):
			delay = time.Duration(float64(delay) * req.RetryPolicy.RetryBackoff)
		}
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", req.RetryPolicy.MaxRetries+1, lastErr)
}

// DoWithJSONResponse 执行HTTP请求并返回处理后的JSON响应
func (c *Client) DoWithJSONResponse(req *Request) ([]byte, error) {
	// 执行HTTP请求
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 检查HTTP状态码
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP error: %d, response: %s", resp.StatusCode, string(respBody))
	}

	// 使用JSON响应处理器处理响应
	processor := NewJSONResponseProcessor()
	return processor.ProcessResponse(resp)
}

// shouldRetry 判断是否应该重试（基于错误）
func (c *Client) shouldRetry(err error, attempt int, policy *RetryPolicy) bool {
	if attempt >= policy.MaxRetries {
		return false
	}

	if netErr, ok := err.(net.Error); ok {
		return netErr.Temporary() || netErr.Timeout()
	}

	return false
}

// shouldRetryByStatus 判断是否应该根据状态码重试
func (c *Client) shouldRetryByStatus(statusCode int) bool {
	// 429 Too Many Requests
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	// 5xx Server Errors
	if statusCode >= 500 && statusCode <= 599 {
		return true
	}
	return false
}

// StreamResponse 流式响应处理
type StreamResponse struct {
	Data  []byte
	Done  bool
	Error error
}

// StreamRequest 流式请求
func (c *Client) StreamRequest(req *Request) (<-chan StreamResponse, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	stream := make(chan StreamResponse, 100)

	go func() {
		defer resp.Body.Close()
		defer close(stream)

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()

			// 过滤 SSE 注释行（以 : 开头）和空行，防止心跳等注释内容混入输出
			if strings.HasPrefix(line, ":") || line == "" {
				continue
			}

			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					stream <- StreamResponse{Done: true}
					return
				}

				stream <- StreamResponse{Data: []byte(data)}
			} else {
				stream <- StreamResponse{Data: []byte(line)}
			}
		}

		if err := scanner.Err(); err != nil {
			stream <- StreamResponse{Error: err}
		}
	}()

	return stream, nil
}

// Close 关闭客户端
func (c *Client) Close() {
	if c.client != nil {
		c.client.CloseIdleConnections()
	}
}
