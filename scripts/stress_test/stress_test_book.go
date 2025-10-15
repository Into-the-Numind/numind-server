package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// API端点配置
	baseURL       = "https://youshu.asia/dev"
	loginAPI      = "/v1/wechat/login"
	createBookAPI = "/v1/books"

	// 测试配置
	testCount       = 100
	timeout         = 120 * time.Second      // 请求超时时间（增加到120秒）
	requestInterval = 500 * time.Millisecond // 请求间隔（避免瞬间并发过高）
)

// WechatLoginRequest 微信登录请求
type WechatLoginRequest struct {
	Code      string `json:"code"`
	PhoneCode string `json:"phone_code"`
}

// WechatLoginResponse 微信登录响应
type WechatLoginResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		User        struct {
			ID       uint   `json:"id"`
			Nickname string `json:"nickname"`
		} `json:"user"`
	} `json:"data"`
}

// CreateBookRequest 创建书籍请求
type CreateBookRequest struct {
	Text       string `json:"text"`
	TemplateID string `json:"template_id"`
}

// CreateBookResponse 创建书籍响应
type CreateBookResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		ID     uint   `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	} `json:"data"`
}

// TestResult 测试结果统计
type TestResult struct {
	Total         int
	Success       int
	Failed        int
	TotalTime     time.Duration
	MinTime       time.Duration
	MaxTime       time.Duration
	AvgTime       time.Duration
	mu            sync.Mutex
	responseTimes []time.Duration
}

// AddResult 添加测试结果
func (tr *TestResult) AddResult(success bool, duration time.Duration) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	tr.Total++
	tr.responseTimes = append(tr.responseTimes, duration)
	tr.TotalTime += duration

	if success {
		tr.Success++
	} else {
		tr.Failed++
	}

	if tr.MinTime == 0 || duration < tr.MinTime {
		tr.MinTime = duration
	}
	if duration > tr.MaxTime {
		tr.MaxTime = duration
	}
}

// Calculate 计算统计数据
func (tr *TestResult) Calculate() {
	if tr.Total > 0 {
		tr.AvgTime = tr.TotalTime / time.Duration(tr.Total)
	}
}

// Print 打印测试结果
func (tr *TestResult) Print() {
	tr.Calculate()

	fmt.Println("\n" + strings.Repeat("=", 70))
	fmt.Println("压力测试结果统计")
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("总请求数:       %d\n", tr.Total)
	fmt.Printf("成功数:         %d (%.2f%%)\n", tr.Success, float64(tr.Success)/float64(tr.Total)*100)
	fmt.Printf("失败数:         %d (%.2f%%)\n", tr.Failed, float64(tr.Failed)/float64(tr.Total)*100)
	fmt.Println(strings.Repeat("-", 70))
	fmt.Printf("总耗时:         %v\n", tr.TotalTime)
	fmt.Printf("平均响应时间:   %v\n", tr.AvgTime)
	fmt.Printf("最短响应时间:   %v\n", tr.MinTime)
	fmt.Printf("最长响应时间:   %v\n", tr.MaxTime)
	fmt.Println(strings.Repeat("=", 70))
}

// generateRandomText 生成指定字数的随机测试文本
func generateRandomText(minChars, maxChars int) string {
	// 随机文本片段库（中文）
	paragraphs := []string{
		"在这个快节奏的时代，我们需要找到内心的平静。生活不仅仅是追求物质的满足，更重要的是精神层面的充实和成长。",
		"阅读是一扇通往世界的窗口，通过书籍我们可以了解不同的文化、思想和观点。每一本书都是一次心灵的旅行。",
		"科技的发展改变了我们的生活方式，但同时也带来了新的挑战。如何在数字时代保持人性的温度，是我们需要思考的问题。",
		"健康的生活方式包括合理的饮食、适量的运动和充足的睡眠。只有身体健康，我们才能更好地追求自己的梦想。",
		"艺术让生活更加丰富多彩，无论是音乐、绘画还是文学，都能给我们带来美的享受和心灵的慰藉。",
		"教育是改变命运的重要途径，知识不仅能够拓宽我们的视野，更能够培养我们的思维能力和创造力。",
		"环保是每个人的责任，保护地球就是保护我们自己的家园。让我们从身边的小事做起，为可持续发展贡献力量。",
		"人际关系是生活中不可或缺的一部分，真诚的沟通和相互理解是维系良好关系的关键。",
		"时间管理是现代人必备的技能，合理安排时间可以让我们的生活更加高效和充实。",
		"创新是推动社会进步的动力，只有不断尝试新事物，我们才能在变化中找到机遇。",
		"旅行能够开阔我们的眼界，体验不同的文化和风土人情。每一次旅行都是一次自我发现的过程。",
		"音乐具有治愈心灵的力量，一首好的歌曲能够唤起我们内心深处的情感共鸣。",
		"摄影是记录生活的艺术，通过镜头我们可以捕捉美好的瞬间，定格珍贵的回忆。",
		"美食不仅满足味蕾，更是一种文化的传承。每一道菜品背后都有独特的故事和情感。",
		"运动带来的不仅是身体的健康，还有精神的愉悦。坚持锻炼可以让我们保持积极向上的生活态度。",
		"阅读经典名著可以提升我们的文学素养，从中汲取智慧和人生经验。",
		"在大自然中漫步，感受四季的变化，可以让我们暂时忘却烦恼，重新找回内心的宁静。",
		"学习新技能是一个持续成长的过程，保持好奇心和求知欲能够让生活更加精彩。",
		"友谊是人生中宝贵的财富，真正的朋友会在你需要的时候给予支持和鼓励。",
		"冥想和正念练习可以帮助我们减轻压力，提高专注力，让心灵得到真正的放松。",
	}

	// 生成随机字数（在指定范围内）
	targetLength := minChars + rand.Intn(maxChars-minChars+1)

	var result strings.Builder
	currentLength := 0

	// 随机选择段落拼接，直到达到目标字数
	for currentLength < targetLength {
		// 随机选择一个段落
		paragraph := paragraphs[rand.Intn(len(paragraphs))]

		// 如果加上这个段落会超出目标字数，截取部分内容
		if currentLength+len([]rune(paragraph)) > targetLength {
			remaining := targetLength - currentLength
			runes := []rune(paragraph)
			if remaining <= len(runes) {
				result.WriteString(string(runes[:remaining]))
				break
			}
		}

		result.WriteString(paragraph)
		currentLength = len([]rune(result.String()))

		// 如果还没达到目标字数，添加换行符
		if currentLength < targetLength {
			result.WriteString("\n\n")
			currentLength += 2
		}
	}

	return result.String()
}

func main() {
	// 初始化随机数生成器
	rand.Seed(time.Now().UnixNano())

	fmt.Println("开始压力测试...")
	fmt.Printf("目标API: %s%s\n", baseURL, createBookAPI)
	fmt.Printf("测试次数: %d\n\n", testCount)

	// 步骤1: 获取登录token
	fmt.Println("步骤1: 获取登录token...")
	token, err := login()
	if err != nil {
		fmt.Printf("❌ 登录失败: %v\n", err)
		return
	}
	fmt.Printf("✓ 登录成功，获得token: %s...\n\n", token[:min(30, len(token))])

	// 步骤2: 执行压力测试
	fmt.Println("步骤2: 开始压力测试...")
	result := &TestResult{}

	// 创建进度显示
	progressChan := make(chan int, testCount)
	go showProgress(progressChan, testCount)

	startTime := time.Now()

	// 执行测试
	for i := 1; i <= testCount; i++ {
		reqStartTime := time.Now()

		// 生成测试数据（随机100-500字的文本）
		randomText := generateRandomText(100, 500)
		bookRequest := CreateBookRequest{
			Text:       randomText,
			TemplateID: "1", // 默认模板ID
		}

		// 发送创建book请求
		success, err := createBook(token, bookRequest)
		duration := time.Since(reqStartTime)

		result.AddResult(success, duration)

		if err != nil {
			fmt.Printf("\n请求 #%d 失败: %v\n", i, err)
		}

		progressChan <- i

		// 添加请求间隔，避免瞬间并发过高导致服务器压力过大
		if i < testCount {
			time.Sleep(requestInterval)
		}
	}

	close(progressChan)

	totalDuration := time.Since(startTime)
	fmt.Printf("\n\n总测试时间: %v\n", totalDuration)

	// 打印结果
	result.Print()

	// 计算QPS
	qps := float64(testCount) / totalDuration.Seconds()
	fmt.Printf("\nQPS (每秒请求数): %.2f\n", qps)
}

// login 执行微信登录并返回token
func login() (string, error) {
	client := &http.Client{
		Timeout: timeout,
	}

	// 准备登录请求
	loginReq := WechatLoginRequest{
		Code:      "98",
		PhoneCode: "",
	}

	jsonData, err := json.Marshal(loginReq)
	if err != nil {
		return "", fmt.Errorf("序列化登录请求失败: %w", err)
	}

	// 创建HTTP请求
	req, err := http.NewRequest("POST", baseURL+loginAPI, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("创建登录请求失败: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "StressTest/1.0")

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("发送登录请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取登录响应失败: %w", err)
	}

	// 解析响应
	var loginResp WechatLoginResponse
	if err := json.Unmarshal(body, &loginResp); err != nil {
		return "", fmt.Errorf("解析登录响应失败: %w, 响应内容: %s", err, string(body))
	}

	// 检查响应码
	if loginResp.Code != 0 {
		return "", fmt.Errorf("登录失败，错误码: %d, 错误信息: %s", loginResp.Code, loginResp.Message)
	}

	if loginResp.Data.AccessToken == "" {
		return "", fmt.Errorf("登录响应中未包含access_token")
	}

	return loginResp.Data.AccessToken, nil
}

// createBook 创建书籍
func createBook(token string, bookReq CreateBookRequest) (bool, error) {
	client := &http.Client{
		Timeout: timeout,
	}

	// 序列化请求数据
	jsonData, err := json.Marshal(bookReq)
	if err != nil {
		return false, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 创建HTTP请求
	req, err := http.NewRequest("POST", baseURL+createBookAPI, bytes.NewBuffer(jsonData))
	if err != nil {
		return false, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "StressTest/1.0")

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("读取响应失败: %w", err)
	}

	// 检查HTTP状态码
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP错误，状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var bookResp CreateBookResponse
	if err := json.Unmarshal(body, &bookResp); err != nil {
		return false, fmt.Errorf("解析响应失败: %w, 响应内容: %s", err, string(body))
	}

	// 检查业务响应码
	if bookResp.Code != 0 {
		return false, fmt.Errorf("业务错误，错误码: %d, 错误信息: %s", bookResp.Code, bookResp.Message)
	}

	return true, nil
}

// showProgress 显示进度
func showProgress(progressChan chan int, total int) {
	for current := range progressChan {
		percentage := float64(current) / float64(total) * 100
		fmt.Printf("\r进度: [%d/%d] %.2f%% ", current, total, percentage)
	}
}

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
