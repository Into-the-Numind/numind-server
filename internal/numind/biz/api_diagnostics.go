package biz

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"numind-server/internal/pkg/log"
)

// APIDiagnostics API诊断工具
type APIDiagnostics struct {
	volcConfig     map[string]string
	aliConfig      map[string]string
	networkTimeout time.Duration
}

// NewAPIDiagnostics 创建API诊断工具
func NewAPIDiagnostics() *APIDiagnostics {
	return &APIDiagnostics{
		networkTimeout: 30 * time.Second,
	}
}

// DiagnoseAllAPIs 诊断所有API连接
func (d *APIDiagnostics) DiagnoseAllAPIs(ctx context.Context) error {
	log.C(ctx).Infow("🔍 开始API连接诊断")

	// 诊断火山引擎API
	if err := d.DiagnoseVolcAPI(ctx); err != nil {
		log.C(ctx).Errorw("❌ 火山引擎API诊断失败", "error", err.Error())
	}

	// 诊断阿里云API
	if err := d.DiagnoseAliAPI(ctx); err != nil {
		log.C(ctx).Errorw("❌ 阿里云API诊断失败", "error", err.Error())
	}

	// 网络连通性测试
	if err := d.TestNetworkConnectivity(ctx); err != nil {
		log.C(ctx).Errorw("❌ 网络连通性测试失败", "error", err.Error())
	}

	log.C(ctx).Infow("✅ API诊断完成")
	return nil
}

// DiagnoseVolcAPI 诊断火山引擎API
func (d *APIDiagnostics) DiagnoseVolcAPI(ctx context.Context) error {
	volcURL := "https://ark.cn-beijing.volces.com/api/v3"

	log.C(ctx).Infow("🔍 诊断火山引擎API", "url", volcURL)

	// 测试连接
	client := &http.Client{Timeout: d.networkTimeout}

	req, err := http.NewRequestWithContext(ctx, "HEAD", volcURL, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.C(ctx).Errorw("❌ 火山引擎API连接失败", "error", err.Error())
		return err
	}
	defer resp.Body.Close()

	log.C(ctx).Infow("✅ 火山引擎API连接成功", "status", resp.Status, "headers", resp.Header)
	return nil
}

// DiagnoseAliAPI 诊断阿里云API
func (d *APIDiagnostics) DiagnoseAliAPI(ctx context.Context) error {
	aliURL := "https://dashscope.aliyuncs.com"

	log.C(ctx).Infow("🔍 诊断阿里云API", "url", aliURL)

	// 测试连接
	client := &http.Client{Timeout: d.networkTimeout}

	req, err := http.NewRequestWithContext(ctx, "HEAD", aliURL, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.C(ctx).Errorw("❌ 阿里云API连接失败", "error", err.Error())
		return err
	}
	defer resp.Body.Close()

	log.C(ctx).Infow("✅ 阿里云API连接成功", "status", resp.Status, "headers", resp.Header)
	return nil
}

// TestNetworkConnectivity 测试网络连通性
func (d *APIDiagnostics) TestNetworkConnectivity(ctx context.Context) error {
	log.C(ctx).Infow("🔍 测试网络连通性")

	// 测试常见的DNS和网络连接
	testURLs := []string{
		"https://www.baidu.com",
		"https://www.google.com",
		"https://httpbin.org/get",
	}

	client := &http.Client{Timeout: 10 * time.Second}

	for _, url := range testURLs {
		req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
		if err != nil {
			log.C(ctx).Warnw("⚠️ 创建请求失败", "url", url, "error", err.Error())
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			log.C(ctx).Warnw("⚠️ 网络连接失败", "url", url, "error", err.Error())
			continue
		}
		resp.Body.Close()

		log.C(ctx).Infow("✅ 网络连接正常", "url", url, "status", resp.Status)
	}

	return nil
}
